package account

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fixture is a throwaway HOME with a fake Claude Code login.
type fixture struct {
	t     *testing.T
	home  string
	store *Store
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, home: home, store: New(PathsFromEnv(func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	}))}
	f.login("uuid-a", "a@example.com", "a1")
	return f
}

func oauth(uuid, email string) map[string]any {
	return map[string]any{"accountUuid": uuid, "emailAddress": email, "organizationUuid": "org-" + uuid}
}

func creds(token string) string {
	return `{"claudeAiOauth":{"accessToken":"at-` + token + `","refreshToken":"rt-` + token + `"}}`
}

// login simulates `claude /login`: it rewrites the live credentials and identity.
func (f *fixture) login(uuid, email, token string) {
	f.t.Helper()
	writeFile(f.t, f.store.Paths.LiveCredentials, creds(token))
	state := map[string]any{"numStartups": 7}
	if raw, err := os.ReadFile(f.store.Paths.LiveState); err == nil {
		_ = json.Unmarshal(raw, &state)
	}
	state["oauthAccount"] = oauth(uuid, email)
	raw, _ := json.Marshal(state)
	writeFile(f.t, f.store.Paths.LiveState, string(raw))
}

func (f *fixture) save(name string) {
	f.t.Helper()
	if _, err := f.store.Save(name, false); err != nil {
		f.t.Fatalf("save %s: %v", name, err)
	}
}

func (f *fixture) saveTwo() {
	f.save("work")
	f.login("uuid-b", "b@example.com", "b1")
	f.save("personal")
}

func (f *fixture) liveCreds() string {
	f.t.Helper()
	return compact(f.t, readFile(f.t, f.store.Paths.LiveCredentials))
}

func (f *fixture) savedCreds(name string) string {
	f.t.Helper()
	return compact(f.t, readFile(f.t, filepath.Join(f.store.Paths.Store, name, credentialsFile)))
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func compact(t *testing.T, s string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("invalid json %q: %v", s, err)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestSaveStoresAccountPrivately(t *testing.T) {
	f := newFixture(t)
	f.save("work")
	dir := filepath.Join(f.store.Paths.Store, "work")
	if got := f.savedCreds("work"); got != compact(t, creds("a1")) {
		t.Fatalf("credentials = %s", got)
	}
	if mode(t, dir) != 0o700 || mode(t, filepath.Join(dir, credentialsFile)) != 0o600 {
		t.Fatalf("modes = %v %v", mode(t, dir), mode(t, filepath.Join(dir, credentialsFile)))
	}
}

func TestListMarksActiveAccount(t *testing.T) {
	f := newFixture(t)
	f.saveTwo()
	accounts, err := f.store.List()
	if err != nil {
		t.Fatal(err)
	}
	active := map[string]bool{}
	for _, a := range accounts {
		active[a.Name] = a.Active
	}
	if len(accounts) != 2 || !active["personal"] || active["work"] {
		t.Fatalf("accounts = %+v", accounts)
	}
}

func TestUseSwitchesCredentialsAndIdentity(t *testing.T) {
	f := newFixture(t)
	f.saveTwo()
	if _, err := f.store.Use("work"); err != nil {
		t.Fatal(err)
	}
	if f.liveCreds() != compact(t, creds("a1")) {
		t.Fatalf("live creds = %s", f.liveCreds())
	}
	var state map[string]any
	_ = json.Unmarshal([]byte(readFile(t, f.store.Paths.LiveState)), &state)
	if state["oauthAccount"].(map[string]any)["accountUuid"] != "uuid-a" {
		t.Fatalf("state = %v", state)
	}
	if state["numStartups"] != float64(7) {
		t.Fatal("unrelated .claude.json fields must survive")
	}
	if mode(t, f.store.Paths.LiveCredentials) != 0o600 {
		t.Fatal("live credentials must stay 0600")
	}
}

func TestUseSyncsRefreshedTokenBack(t *testing.T) {
	f := newFixture(t)
	f.saveTwo()
	writeFile(t, f.store.Paths.LiveCredentials, creds("b2")) // a running session refreshed the token
	if _, err := f.store.Use("work"); err != nil {
		t.Fatal(err)
	}
	if f.savedCreds("personal") != compact(t, creds("b2")) {
		t.Fatal("the refreshed token must not be lost")
	}
	if _, err := f.store.Use("personal"); err != nil {
		t.Fatal(err)
	}
	if f.liveCreds() != compact(t, creds("b2")) {
		t.Fatalf("live creds = %s", f.liveCreds())
	}
}

func TestUseFailures(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(f *fixture)
		target  string
		wantErr error
		live    string
	}{
		{
			name:    "active login unsaved",
			setup:   func(f *fixture) { f.save("work"); f.login("uuid-c", "c@example.com", "c1") },
			target:  "work",
			wantErr: ErrActiveUnsaved,
			live:    creds("c1"),
		},
		{
			name:    "unknown account",
			setup:   func(f *fixture) { f.save("work") },
			target:  "nope",
			wantErr: ErrNotFound,
			live:    creds("a1"),
		},
		{
			name:    "unsafe name",
			setup:   func(f *fixture) {},
			target:  "../evil",
			wantErr: ErrInvalidName,
			live:    creds("a1"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			tt.setup(f)
			_, err := f.store.Use(tt.target)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if f.liveCreds() != compact(t, tt.live) {
				t.Fatal("live credentials must stay untouched")
			}
		})
	}
}

func TestUseAlreadyActiveIsNoop(t *testing.T) {
	f := newFixture(t)
	f.saveTwo()
	res, err := f.store.Use("personal")
	if err != nil || !res.AlreadyActive {
		t.Fatalf("res = %+v err = %v", res, err)
	}
}

func TestUseWritesBackup(t *testing.T) {
	f := newFixture(t)
	f.saveTwo()
	if _, err := f.store.Use("work"); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(f.store.Paths.Store, backupsDir))
	if len(entries) != 1 {
		t.Fatalf("backups = %d", len(entries))
	}
	got := readFile(t, filepath.Join(f.store.Paths.Store, backupsDir, entries[0].Name(), credentialsFile))
	if compact(t, got) != compact(t, creds("b1")) {
		t.Fatalf("backup = %s", got)
	}
}

func TestSaveFailures(t *testing.T) {
	t.Run("different account under the same name", func(t *testing.T) {
		f := newFixture(t)
		f.save("work")
		f.login("uuid-b", "b@example.com", "b1")
		if _, err := f.store.Save("work", false); !errors.Is(err, ErrNameTaken) {
			t.Fatalf("err = %v", err)
		}
		if _, err := f.store.Save("work", true); err != nil {
			t.Fatalf("force: %v", err)
		}
	})
	t.Run("same login under another name", func(t *testing.T) {
		f := newFixture(t)
		f.save("work")
		if _, err := f.store.Save("other", false); !errors.Is(err, ErrAlreadySaved) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no live login", func(t *testing.T) {
		f := newFixture(t)
		_ = os.Remove(f.store.Paths.LiveCredentials)
		if _, err := f.store.Save("work", false); !errors.Is(err, ErrNoLogin) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRemoveAndRename(t *testing.T) {
	f := newFixture(t)
	f.saveTwo()
	if err := f.store.Rename("work", "job"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Rename("job", "personal"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("rename onto existing: %v", err)
	}
	if err := f.store.Remove("job"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Remove("job"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second remove: %v", err)
	}
	names, _ := f.store.Names()
	if len(names) != 1 || names[0] != "personal" {
		t.Fatalf("names = %v", names)
	}
}

func TestNextRotatesAlphabetically(t *testing.T) {
	f := newFixture(t)
	f.saveTwo() // active: personal; order: personal, work
	res, err := f.store.Next()
	if err != nil || res.Account.Name != "work" {
		t.Fatalf("next = %+v err = %v", res, err)
	}
	res, err = f.store.Next()
	if err != nil || res.Account.Name != "personal" {
		t.Fatalf("next wraps = %+v err = %v", res, err)
	}
}

func TestImportOrca(t *testing.T) {
	f := newFixture(t)
	orca := filepath.Join(f.home, ".config", "orca")
	for _, acc := range []struct{ id, uuid, email, token string }{
		{"id-1", "uuid-a", "alpha@example.com", "a0-stale"},
		{"id-2", "uuid-b", "beta@example.com", "b1"},
	} {
		raw, _ := json.Marshal(oauth(acc.uuid, acc.email))
		writeFile(t, filepath.Join(orca, "claude-accounts", acc.id, "auth", "oauth-account.json"), string(raw))
		writeFile(t, filepath.Join(orca, "claude-accounts", acc.id, "auth", ".credentials.json"), creds(acc.token))
	}
	snapshot, _ := json.Marshal(map[string]any{
		"credentialsJson":    creds("s1"),
		"configOauthAccount": oauth("uuid-s", "boss@company.com"),
	})
	writeFile(t, filepath.Join(orca, "claude-runtime-auth", "system-default-auth.json"), string(snapshot))

	imported, err := f.store.ImportOrca()
	if err != nil || len(imported) != 3 {
		t.Fatalf("imported = %v err = %v", imported, err)
	}
	if f.savedCreds("alpha") != compact(t, creds("a1")) {
		t.Fatal("the active account must import the live (freshest) token")
	}
	if f.savedCreds("boss") != compact(t, creds("s1")) || f.savedCreds("beta") != compact(t, creds("b1")) {
		t.Fatal("unexpected imported credentials")
	}
	again, err := f.store.ImportOrca()
	if err != nil || len(again) != 0 {
		t.Fatalf("re-import = %v err = %v", again, err)
	}
}

func TestPathsRespectClaudeConfigDir(t *testing.T) {
	p := PathsFromEnv(func(k string) string {
		return map[string]string{"HOME": "/h", "CLAUDE_CONFIG_DIR": "/c", "CCS_HOME": "/s"}[k]
	})
	want := Paths{Home: "/h", ConfigDir: "/c", LiveCredentials: "/c/.credentials.json", LiveState: "/c/.claude.json", Store: "/s"}
	if p != want {
		t.Fatalf("paths = %+v", p)
	}
	p = PathsFromEnv(func(k string) string { return map[string]string{"HOME": "/h"}[k] })
	if p.LiveState != "/h/.claude.json" || p.Store != "/h/.claude-accounts" || p.ConfigDir != "/h/.claude" {
		t.Fatalf("default paths = %+v", p)
	}
}

func TestValidateName(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{"work", true}, {"a.b_c-1", true}, {"", false}, {"../x", false}, {".hidden", false}, {"a b", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateName(tt.name); (err == nil) != tt.ok {
				t.Fatalf("ValidateName(%q) = %v", tt.name, err)
			}
		})
	}
}
