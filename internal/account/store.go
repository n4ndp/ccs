// Package account stores Claude Code logins and swaps the active one in place.
//
// Claude Code reads its login from two files: the credentials file (OAuth tokens)
// and the "oauthAccount" entry of .claude.json (identity). Switching rewrites both,
// so running Claude Code sessions follow the switch without a restart.
package account

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"syscall"
	"time"
)

const (
	credentialsFile = "credentials.json"
	accountFile     = "account.json"
	backupsDir      = ".backups"
	maxBackups      = 20
)

var (
	ErrNoLogin       = errors.New("no Claude Code login found; run `claude` and log in, or `ccs add <name>`")
	ErrNotFound      = errors.New("no saved account with that name")
	ErrInvalidName   = errors.New("invalid account name: use letters, digits, '.', '_' or '-' (max 64)")
	ErrNameTaken     = errors.New("that name already holds a different account")
	ErrAlreadySaved  = errors.New("this login is already saved under another name")
	ErrActiveUnsaved = errors.New("the active login is not saved and would be lost; run `ccs save <name>` first")
	ErrNoAccounts    = errors.New("no saved accounts")
)

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidateName rejects names that could escape the store or hide as dotfiles.
func ValidateName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	return nil
}

// Paths locates the live Claude Code files and the account store.
type Paths struct {
	Home            string
	ConfigDir       string
	LiveCredentials string
	LiveState       string
	Store           string
}

// PathsFromEnv honours CLAUDE_CONFIG_DIR (like Claude Code) and CCS_HOME.
func PathsFromEnv(getenv func(string) string) Paths {
	home := getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	p := Paths{Home: home, ConfigDir: filepath.Join(home, ".claude"), Store: filepath.Join(home, ".claude-accounts")}
	p.LiveState = filepath.Join(home, ".claude.json")
	if dir := getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		p.ConfigDir = dir
		p.LiveState = filepath.Join(dir, ".claude.json")
	}
	p.LiveCredentials = filepath.Join(p.ConfigDir, ".credentials.json")
	if dir := getenv("CCS_HOME"); dir != "" {
		p.Store = dir
	}
	return p
}

// Account is one saved login.
type Account struct {
	Name        string          `json:"name"`
	Active      bool            `json:"active"`
	OAuth       map[string]any  `json:"-"`
	Credentials json.RawMessage `json:"-"`
}

func (a Account) str(key string) string {
	s, _ := a.OAuth[key].(string)
	return s
}

func (a Account) UUID() string    { return a.str("accountUuid") }
func (a Account) Email() string   { return a.str("emailAddress") }
func (a Account) OrgName() string { return a.str("organizationName") }

// SwitchResult reports what Use or Next did.
type SwitchResult struct {
	Account       Account
	AlreadyActive bool
	Backup        string
}

// Store manages saved accounts.
type Store struct {
	Paths Paths
	now   func() time.Time
}

func New(p Paths) *Store { return &Store{Paths: p, now: time.Now} }

// --- io helpers --------------------------------------------------------------

func readJSON(path string, v any) (bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return false, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	return true, nil
}

// writeAtomic writes via temp file + fsync + rename so readers never see a partial file.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func privateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

// lock serialises every mutation of the store and the live files.
func (s *Store) lock() (func(), error) {
	if err := privateDir(s.Paths.Store); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(s.Paths.Store, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// --- live login ----------------------------------------------------------------

func (s *Store) liveState() (map[string]json.RawMessage, error) {
	state := map[string]json.RawMessage{}
	if _, err := readJSON(s.Paths.LiveState, &state); err != nil {
		return nil, err
	}
	return state, nil
}

// readLogin reads credentials + identity from a Claude config layout.
func readLogin(credentialsPath, statePath string) (Account, error) {
	creds, err := os.ReadFile(credentialsPath)
	if errors.Is(err, os.ErrNotExist) {
		return Account{}, ErrNoLogin
	}
	if err != nil {
		return Account{}, err
	}
	if !json.Valid(creds) {
		return Account{}, fmt.Errorf("%s is not valid JSON", credentialsPath)
	}
	var state struct {
		OAuthAccount map[string]any `json:"oauthAccount"`
	}
	if _, err := readJSON(statePath, &state); err != nil {
		return Account{}, err
	}
	acc := Account{OAuth: state.OAuthAccount, Credentials: creds}
	if acc.UUID() == "" {
		return Account{}, ErrNoLogin
	}
	return acc, nil
}

// Live returns the login Claude Code is currently using.
func (s *Store) Live() (Account, error) {
	return readLogin(s.Paths.LiveCredentials, s.Paths.LiveState)
}

// LiveUUID returns the active account UUID, or "" when nobody is logged in.
func (s *Store) LiveUUID() string {
	var state struct {
		OAuthAccount map[string]any `json:"oauthAccount"`
	}
	if _, err := readJSON(s.Paths.LiveState, &state); err != nil {
		return ""
	}
	uuid, _ := state.OAuthAccount["accountUuid"].(string)
	return uuid
}

func (s *Store) writeLive(acc Account) error {
	state, err := s.liveState()
	if err != nil {
		return err
	}
	identity, err := json.Marshal(acc.OAuth)
	if err != nil {
		return err
	}
	state["oauthAccount"] = identity
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	perm := os.FileMode(0o600)
	if info, err := os.Stat(s.Paths.LiveState); err == nil {
		perm = info.Mode().Perm()
	}
	if err := writeAtomic(s.Paths.LiveCredentials, acc.Credentials, 0o600); err != nil {
		return err
	}
	return writeAtomic(s.Paths.LiveState, raw, perm)
}

// --- saved accounts --------------------------------------------------------------

// Names lists saved account names in alphabetical order.
func (s *Store) Names() ([]string, error) {
	entries, err := os.ReadDir(s.Paths.Store)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() || ValidateName(e.Name()) != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.Paths.Store, e.Name(), accountFile)); err == nil {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// Get loads one saved account.
func (s *Store) Get(name string) (Account, error) {
	if err := ValidateName(name); err != nil {
		return Account{}, err
	}
	dir := filepath.Join(s.Paths.Store, name)
	acc := Account{Name: name}
	ok, err := readJSON(filepath.Join(dir, accountFile), &acc.OAuth)
	if err != nil {
		return Account{}, err
	}
	creds, credErr := os.ReadFile(filepath.Join(dir, credentialsFile))
	if !ok || credErr != nil || acc.UUID() == "" {
		return Account{}, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	acc.Credentials = creds
	acc.Active = acc.UUID() == s.LiveUUID()
	return acc, nil
}

// List loads every saved account, marking the active one.
func (s *Store) List() ([]Account, error) {
	names, err := s.Names()
	if err != nil {
		return nil, err
	}
	accounts := make([]Account, 0, len(names))
	for _, n := range names {
		acc, err := s.Get(n)
		if err != nil {
			continue
		}
		accounts = append(accounts, acc)
	}
	return accounts, nil
}

func (s *Store) findByUUID(uuid string) string {
	accounts, _ := s.List()
	for _, a := range accounts {
		if a.UUID() == uuid {
			return a.Name
		}
	}
	return ""
}

// ActiveName returns the saved name of the live login, or "" if it is not saved.
func (s *Store) ActiveName() string {
	uuid := s.LiveUUID()
	if uuid == "" {
		return ""
	}
	return s.findByUUID(uuid)
}

func (s *Store) write(name string, acc Account) error {
	dir := filepath.Join(s.Paths.Store, name)
	if err := privateDir(dir); err != nil {
		return err
	}
	identity, err := json.MarshalIndent(acc.OAuth, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dir, credentialsFile), acc.Credentials, 0o600); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, accountFile), identity, 0o600)
}

// store saves acc under name, refusing to clobber a different account unless force.
func (s *Store) store(name string, acc Account, force bool) (Account, error) {
	if err := ValidateName(name); err != nil {
		return Account{}, err
	}
	if existing := s.findByUUID(acc.UUID()); existing != "" && existing != name {
		return Account{}, fmt.Errorf("%w: %q", ErrAlreadySaved, existing)
	}
	if current, err := s.Get(name); err == nil && current.UUID() != acc.UUID() && !force {
		return Account{}, fmt.Errorf("%w: %q holds %s (use --force to replace it)", ErrNameTaken, name, current.Email())
	}
	if err := s.write(name, acc); err != nil {
		return Account{}, err
	}
	acc.Name = name
	acc.Active = acc.UUID() == s.LiveUUID()
	return acc, nil
}

// Save stores the live login under name.
func (s *Store) Save(name string, force bool) (Account, error) {
	unlock, err := s.lock()
	if err != nil {
		return Account{}, err
	}
	defer unlock()
	live, err := s.Live()
	if err != nil {
		return Account{}, err
	}
	return s.store(name, live, force)
}

// Use makes name the live login. Before switching it saves the live token back to its
// account (a running session may have rotated it) and backs up the live files.
func (s *Store) Use(name string) (SwitchResult, error) {
	unlock, err := s.lock()
	if err != nil {
		return SwitchResult{}, err
	}
	defer unlock()
	return s.use(name)
}

func (s *Store) use(name string) (SwitchResult, error) {
	target, err := s.Get(name)
	if err != nil {
		return SwitchResult{}, err
	}
	if active := s.LiveUUID(); active != "" {
		if active == target.UUID() {
			return SwitchResult{Account: target, AlreadyActive: true}, nil
		}
		activeName := s.findByUUID(active)
		if activeName == "" {
			return SwitchResult{}, ErrActiveUnsaved
		}
		live, err := s.Live()
		if err != nil {
			return SwitchResult{}, err
		}
		if err := s.write(activeName, live); err != nil {
			return SwitchResult{}, err
		}
	}
	backup, err := s.backup()
	if err != nil {
		return SwitchResult{}, err
	}
	if err := s.writeLive(target); err != nil {
		return SwitchResult{}, err
	}
	target.Active = true
	return SwitchResult{Account: target, Backup: backup}, nil
}

// Next switches to the account after the active one, alphabetically, wrapping around.
func (s *Store) Next() (SwitchResult, error) {
	unlock, err := s.lock()
	if err != nil {
		return SwitchResult{}, err
	}
	defer unlock()
	names, err := s.Names()
	if err != nil {
		return SwitchResult{}, err
	}
	if len(names) == 0 {
		return SwitchResult{}, ErrNoAccounts
	}
	next := names[0]
	if active := s.ActiveName(); active != "" {
		for i, n := range names {
			if n == active {
				next = names[(i+1)%len(names)]
			}
		}
	}
	return s.use(next)
}

func (s *Store) backup() (string, error) {
	root := filepath.Join(s.Paths.Store, backupsDir)
	dir := filepath.Join(root, s.now().Format("20060102-150405.000000"))
	if err := privateDir(dir); err != nil {
		return "", err
	}
	for src, dst := range map[string]string{s.Paths.LiveCredentials: credentialsFile, s.Paths.LiveState: "claude.json"} {
		raw, err := os.ReadFile(src)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, dst), raw, 0o600); err != nil {
			return "", err
		}
	}
	entries, _ := os.ReadDir(root)
	for i := 0; i < len(entries)-maxBackups; i++ {
		_ = os.RemoveAll(filepath.Join(root, entries[i].Name()))
	}
	return dir, nil
}

// Remove forgets a saved account. The live login is never touched.
func (s *Store) Remove(name string) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := s.Get(name); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.Paths.Store, name))
}

// Rename changes a saved account's name.
func (s *Store) Rename(from, to string) error {
	if err := ValidateName(to); err != nil {
		return err
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := s.Get(from); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(s.Paths.Store, to)); err == nil {
		return fmt.Errorf("%w: %q", ErrNameTaken, to)
	}
	return os.Rename(filepath.Join(s.Paths.Store, from), filepath.Join(s.Paths.Store, to))
}
