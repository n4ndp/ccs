package account

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/n4ndp/ccs/internal/claude"
)

const tmpDir = ".tmp"

var ErrWrongAccount = errors.New("the login belongs to a different account")

// NewLoginDir creates a private, empty Claude config dir inside the store. Running
// `claude auth login` with CLAUDE_CONFIG_DIR pointing here never touches the live login.
func (s *Store) NewLoginDir() (string, error) {
	root := filepath.Join(s.Paths.Store, tmpDir)
	if err := privateDir(s.Paths.Store); err != nil {
		return "", err
	}
	if err := privateDir(root); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(root, "login-")
	if err != nil {
		return "", err
	}
	return dir, os.Chmod(dir, 0o700)
}

func readDirLogin(dir string) (Account, error) {
	return readLogin(filepath.Join(dir, ".credentials.json"), filepath.Join(dir, ".claude.json"))
}

// Add saves the login `claude auth login` wrote into dir, then removes dir.
func (s *Store) Add(name, dir string, force bool) (Account, error) {
	defer os.RemoveAll(dir)
	if err := ValidateName(name); err != nil {
		return Account{}, err
	}
	acc, err := readDirLogin(dir)
	if err != nil {
		return Account{}, fmt.Errorf("login not completed: %w", err)
	}
	unlock, err := s.lock()
	if err != nil {
		return Account{}, err
	}
	defer unlock()
	return s.store(name, acc, force)
}

// Relogin replaces a saved account's tokens with a fresh login of the same account.
// When that account is active, the live files are refreshed too.
func (s *Store) Relogin(name, dir string) (Account, error) {
	defer os.RemoveAll(dir)
	fresh, err := readDirLogin(dir)
	if err != nil {
		return Account{}, fmt.Errorf("login not completed: %w", err)
	}
	unlock, err := s.lock()
	if err != nil {
		return Account{}, err
	}
	defer unlock()
	saved, err := s.Get(name)
	if err != nil {
		return Account{}, err
	}
	if saved.UUID() != fresh.UUID() {
		return Account{}, fmt.Errorf("%w: %q is %s, but you logged in as %s", ErrWrongAccount, name, saved.Email(), fresh.Email())
	}
	if err := s.write(name, fresh); err != nil {
		return Account{}, err
	}
	if saved.Active {
		if err := s.writeLive(fresh); err != nil {
			return Account{}, err
		}
	}
	fresh.Name, fresh.Active = name, saved.Active
	return fresh, nil
}

// Check reports an account's login status through `claude auth status`.
//
// The active account is checked against the live files: checking a copy could rotate the
// refresh token and invalidate the one running sessions use. Inactive accounts are checked
// in a temporary copy, and any token Claude Code refreshed there is saved back.
func (s *Store) Check(name string, status func(configDir string) (claude.Status, error)) (claude.Status, error) {
	acc, err := s.Get(name)
	if err != nil {
		return claude.Status{}, err
	}
	if acc.Active {
		return status("")
	}
	dir, err := s.NewLoginDir()
	if err != nil {
		return claude.Status{}, err
	}
	defer os.RemoveAll(dir)
	state, err := json.Marshal(map[string]any{"oauthAccount": acc.OAuth})
	if err != nil {
		return claude.Status{}, err
	}
	if err := writeAtomic(filepath.Join(dir, ".credentials.json"), acc.Credentials, 0o600); err != nil {
		return claude.Status{}, err
	}
	if err := writeAtomic(filepath.Join(dir, ".claude.json"), state, 0o600); err != nil {
		return claude.Status{}, err
	}
	st, statusErr := status(dir)
	refreshed, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	if err != nil || bytes.Equal(refreshed, acc.Credentials) {
		return st, statusErr
	}
	after, ok := parseOAuth(refreshed)
	if !ok || after.AccessToken == "" || after.RefreshToken == "" {
		// Claude Code wipes the tokens when the server rejects the refresh token.
		// Keep the saved copy (a transient failure must not destroy it) and report it.
		st.LoggedIn = false
		return st, statusErr
	}
	if before, _ := parseOAuth(acc.Credentials); after.ExpiresAt >= before.ExpiresAt {
		if err := s.saveRefreshed(name, acc.UUID(), refreshed); err != nil {
			return st, err
		}
	}
	return st, statusErr
}

type oauthTokens struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    int64  `json:"expiresAt"`
}

func parseOAuth(raw []byte) (oauthTokens, bool) {
	var v struct {
		ClaudeAiOauth *oauthTokens `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(raw, &v); err != nil || v.ClaudeAiOauth == nil {
		return oauthTokens{}, false
	}
	return *v.ClaudeAiOauth, true
}

func (s *Store) saveRefreshed(name, uuid string, creds []byte) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	current, err := s.Get(name)
	if err != nil || current.UUID() != uuid || current.Active {
		return nil // renamed, replaced or activated meanwhile: keep what is there
	}
	return writeAtomic(filepath.Join(s.Paths.Store, name, credentialsFile), creds, 0o600)
}
