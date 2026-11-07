package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n4ndp/ccs/internal/account"
	"github.com/n4ndp/ccs/internal/claude"
)

// fakeClaudeScript emulates `claude auth status` and `claude auth login` inside CLAUDE_CONFIG_DIR.
const fakeClaudeScript = `#!/bin/sh
dir="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
case "$1 $2" in
"auth status")
  if [ -f "$dir/.credentials.json" ]; then li=true; else li=false; fi
  printf '{"loggedIn":%s,"subscriptionType":"max","loginExpiresAt":"2099-01-01T00:00:00Z"}' "$li" ;;
"auth login")
  mkdir -p "$dir"
  printf '{"claudeAiOauth":{"refreshToken":"rt-new"}}' > "$dir/.credentials.json"
  printf '{"oauthAccount":{"accountUuid":"uuid-new","emailAddress":"%s"}}' "${4:-new@example.com}" > "$dir/.claude.json" ;;
*) exit 3 ;;
esac
`

type env struct {
	t     *testing.T
	home  string
	store *account.Store
	bin   string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CCS_HOME", "")
	t.Setenv("NO_COLOR", "1")
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, []byte(fakeClaudeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, home: home, store: account.New(account.PathsFromEnv(os.Getenv)), bin: bin}
	e.login("uuid-a", "a@example.com", "a1")
	return e
}

func (e *env) login(uuid, email, token string) {
	e.t.Helper()
	_ = os.MkdirAll(filepath.Join(e.home, ".claude"), 0o700)
	_ = os.WriteFile(filepath.Join(e.home, ".claude", ".credentials.json"), []byte(`{"claudeAiOauth":{"refreshToken":"rt-`+token+`"}}`), 0o600)
	_ = os.WriteFile(filepath.Join(e.home, ".claude.json"), []byte(`{"oauthAccount":{"accountUuid":"`+uuid+`","emailAddress":"`+email+`"}}`), 0o600)
}

func (e *env) run(args ...string) (string, string, error) {
	e.t.Helper()
	var out, errOut bytes.Buffer
	root := NewRoot(Deps{Store: e.store, Claude: claude.CLI{Bin: e.bin}, In: strings.NewReader(""), Out: &out, Err: &errOut})
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func (e *env) mustRun(args ...string) string {
	e.t.Helper()
	out, errOut, err := e.run(args...)
	if err != nil {
		e.t.Fatalf("ccs %v: %v\n%s", args, err, errOut)
	}
	return out
}

func TestListJSON(t *testing.T) {
	e := newEnv(t)
	e.mustRun("save", "work")
	e.login("uuid-b", "b@example.com", "b1")
	e.mustRun("save", "personal")
	var got []struct {
		Name   string `json:"name"`
		Email  string `json:"email"`
		Active bool   `json:"active"`
	}
	if err := json.Unmarshal([]byte(e.mustRun("list", "--json")), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "personal" || !got[0].Active || got[1].Email != "a@example.com" {
		t.Fatalf("list = %+v", got)
	}
}

func TestUseCurrentAndNext(t *testing.T) {
	e := newEnv(t)
	e.mustRun("save", "work")
	e.login("uuid-b", "b@example.com", "b1")
	e.mustRun("save", "personal")
	if out := e.mustRun("use", "work"); !strings.Contains(out, "work") {
		t.Fatalf("use output = %q", out)
	}
	if out := e.mustRun("current"); strings.TrimSpace(out) != "work" {
		t.Fatalf("current = %q", out)
	}
	e.mustRun("next")
	if out := e.mustRun("current"); strings.TrimSpace(out) != "personal" {
		t.Fatalf("current after next = %q", out)
	}
}

func TestUseWithoutNameNeedsTerminal(t *testing.T) {
	e := newEnv(t)
	e.mustRun("save", "work")
	if _, _, err := e.run("use"); err == nil {
		t.Fatal("use without a name and without a TTY must fail")
	}
}

func TestErrorsAreReported(t *testing.T) {
	e := newEnv(t)
	_, _, err := e.run("use", "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("err = %v", err)
	}
}

func TestAddRunsClaudeLoginInIsolation(t *testing.T) {
	e := newEnv(t)
	e.mustRun("save", "work")
	e.mustRun("add", "new", "--email", "n@example.com")
	acc, err := e.store.Get("new")
	if err != nil || acc.Email() != "n@example.com" {
		t.Fatalf("added = %+v err = %v", acc, err)
	}
	live, _ := e.store.Live()
	if live.UUID() != "uuid-a" {
		t.Fatal("add must not change the live login")
	}
}

func TestStatusJSON(t *testing.T) {
	e := newEnv(t)
	e.mustRun("save", "work")
	e.login("uuid-b", "b@example.com", "b1")
	e.mustRun("save", "personal")
	var got []struct {
		Name     string `json:"name"`
		LoggedIn bool   `json:"loggedIn"`
		Plan     string `json:"plan"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal([]byte(e.mustRun("status", "--json")), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("status = %+v", got)
	}
	for _, s := range got {
		if !s.LoggedIn || s.Plan != "max" || s.Error != "" {
			t.Fatalf("status row = %+v", s)
		}
	}
}

func TestRenameAndRemove(t *testing.T) {
	e := newEnv(t)
	e.mustRun("save", "work")
	e.mustRun("rename", "work", "job")
	if _, _, err := e.run("remove", "job"); err == nil {
		t.Fatal("remove without -y and without a TTY must refuse")
	}
	e.mustRun("remove", "job", "-y")
	if names, _ := e.store.Names(); len(names) != 0 {
		t.Fatalf("names = %v", names)
	}
}

func TestNoArgsWithoutTerminalLists(t *testing.T) {
	e := newEnv(t)
	e.mustRun("save", "work")
	if out := e.mustRun(); !strings.Contains(out, "work") || !strings.Contains(out, "a@example.com") {
		t.Fatalf("output = %q", out)
	}
}
