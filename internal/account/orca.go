package account

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func (s *Store) orcaRoot() string { return filepath.Join(s.Paths.Home, ".config", "orca") }

// orcaCandidates returns every login the Orca app keeps on disk: its per-account folders
// and the "System default" snapshot its copy-based switching takes of the original login.
func (s *Store) orcaCandidates() []Account {
	var found []Account
	dirs, _ := filepath.Glob(filepath.Join(s.orcaRoot(), "claude-accounts", "*", "auth"))
	for _, dir := range dirs {
		// oauth-account.json holds the identity object itself, not a .claude.json wrapper.
		var identity map[string]any
		if ok, _ := readJSON(filepath.Join(dir, "oauth-account.json"), &identity); !ok {
			continue
		}
		var creds json.RawMessage
		if ok, _ := readJSON(filepath.Join(dir, ".credentials.json"), &creds); !ok {
			continue
		}
		found = append(found, Account{OAuth: identity, Credentials: creds})
	}
	var snapshot struct {
		CredentialsJSON    string         `json:"credentialsJson"`
		ConfigOAuthAccount map[string]any `json:"configOauthAccount"`
	}
	if ok, _ := readJSON(filepath.Join(s.orcaRoot(), "claude-runtime-auth", "system-default-auth.json"), &snapshot); ok &&
		json.Valid([]byte(snapshot.CredentialsJSON)) {
		found = append(found, Account{OAuth: snapshot.ConfigOAuthAccount, Credentials: json.RawMessage(snapshot.CredentialsJSON)})
	}
	return found
}

// ImportOrca saves every Orca login not saved yet, named after its email.
func (s *Store) ImportOrca() ([]Account, error) {
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	live, liveErr := s.Live()
	var imported []Account
	for _, acc := range s.orcaCandidates() {
		if acc.UUID() == "" || s.findByUUID(acc.UUID()) != "" {
			continue
		}
		if liveErr == nil && live.UUID() == acc.UUID() {
			acc.Credentials = live.Credentials // Orca's copy may hold a rotated refresh token
		}
		base := acc.UUID()
		if email := acc.Email(); email != "" {
			base = strings.SplitN(email, "@", 2)[0]
		}
		base = strings.TrimLeft(unsafeChars.ReplaceAllString(base, "-"), ".-_")
		if base == "" {
			base = "account"
		}
		name := base
		for i := 2; ; i++ {
			if _, err := s.Get(name); err != nil {
				break
			}
			name = base + "-" + strconv.Itoa(i)
		}
		saved, err := s.store(name, acc, false)
		if err != nil {
			return imported, err
		}
		imported = append(imported, saved)
	}
	return imported, nil
}
