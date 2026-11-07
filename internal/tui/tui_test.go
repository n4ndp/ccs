package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/n4ndp/ccs/internal/account"
	"github.com/n4ndp/ccs/internal/claude"
	"github.com/n4ndp/ccs/internal/cli"
)

func login(t *testing.T, home, uuid, email string) {
	t.Helper()
	_ = os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"claudeAiOauth":{"refreshToken":"rt-`+uuid+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"oauthAccount":{"accountUuid":"`+uuid+`","emailAddress":"`+email+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// newModel returns a model over two saved accounts: "personal" (active) and "work".
func newModel(t *testing.T) (Model, *account.Store) {
	t.Helper()
	home := t.TempDir()
	store := account.New(account.PathsFromEnv(func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	}))
	login(t, home, "uuid-a", "a@example.com")
	if _, err := store.Save("work", false); err != nil {
		t.Fatal(err)
	}
	login(t, home, "uuid-b", "b@example.com")
	if _, err := store.Save("personal", false); err != nil {
		t.Fatal(err)
	}
	m := New(store, claude.CLI{Bin: "false"})
	m.check = func(a account.Account) cli.StatusRow {
		return cli.StatusRow{Name: a.Name, Email: a.Email(), Active: a.Active, LoggedIn: true, Plan: "pro"}
	}
	return m, store
}

// send feeds msg to the model and runs the resulting command chain synchronously,
// skipping batches of async checks and ticks.
func send(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, cmd := m.Update(msg)
	m = next.(Model)
	for i := 0; cmd != nil && i < 5; i++ {
		out := cmd()
		switch out.(type) {
		case nil, tea.BatchMsg, tea.QuitMsg:
			return m
		}
		next, cmd = m.Update(out)
		m = next.(Model)
	}
	return m
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func typeText(t *testing.T, m Model, s string) Model {
	for _, r := range s {
		m = send(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

func TestStartsOnActiveAccount(t *testing.T) {
	m, _ := newModel(t)
	if m.selected().Name != "personal" {
		t.Fatalf("cursor on %q", m.selected().Name)
	}
}

func TestNavigationIsBounded(t *testing.T) {
	m, _ := newModel(t)
	for _, k := range []string{"down", "down", "j", "down"} {
		m = send(t, m, key(k))
	}
	if m.cursor != 1 {
		t.Fatalf("cursor = %d", m.cursor)
	}
	for _, k := range []string{"up", "k", "up"} {
		m = send(t, m, key(k))
	}
	if m.cursor != 0 {
		t.Fatalf("cursor = %d", m.cursor)
	}
}

func TestEnterSwitchesAccount(t *testing.T) {
	m, store := newModel(t)
	m = send(t, m, key("down")) // work
	m = send(t, m, key("enter"))
	if store.ActiveName() != "work" {
		t.Fatalf("active = %q", store.ActiveName())
	}
	if !strings.Contains(m.flash, "work") || m.flashErr {
		t.Fatalf("flash = %q", m.flash)
	}
	if !m.accounts[m.cursor].Active {
		t.Fatal("the switched account must be marked active")
	}
}

func TestNextRotates(t *testing.T) {
	m, store := newModel(t)
	m = send(t, m, key("n"))
	if store.ActiveName() != "work" {
		t.Fatalf("active = %q", store.ActiveName())
	}
}

func TestDeleteNeedsConfirmation(t *testing.T) {
	m, store := newModel(t)
	m = send(t, m, key("down"))
	m = send(t, m, key("d"))
	if m.mode != modeConfirmDelete {
		t.Fatalf("mode = %v", m.mode)
	}
	m = send(t, m, key("n"))
	if names, _ := store.Names(); len(names) != 2 {
		t.Fatal("answering no must keep the account")
	}
	m = send(t, m, key("d"))
	m = send(t, m, key("y"))
	if names, _ := store.Names(); len(names) != 1 || names[0] != "personal" {
		t.Fatalf("names = %v", names)
	}
	if len(m.accounts) != 1 || m.cursor != 0 {
		t.Fatalf("accounts = %d cursor = %d", len(m.accounts), m.cursor)
	}
}

func TestRenameThroughPrompt(t *testing.T) {
	m, store := newModel(t)
	m = send(t, m, key("down"))
	m = send(t, m, key("r"))
	if m.mode != modeRename {
		t.Fatalf("mode = %v", m.mode)
	}
	m.input.SetValue("")
	m = typeText(t, m, "job")
	m = send(t, m, key("enter"))
	if _, err := store.Get("job"); err != nil {
		t.Fatalf("rename failed: %v (flash %q)", err, m.flash)
	}
	if m.mode != modeList {
		t.Fatalf("mode = %v", m.mode)
	}
}

func TestEscCancelsPrompt(t *testing.T) {
	m, store := newModel(t)
	m = send(t, m, key("a"))
	if m.mode != modeAdd {
		t.Fatalf("mode = %v", m.mode)
	}
	m = typeText(t, m, "new")
	m = send(t, m, key("esc"))
	if m.mode != modeList {
		t.Fatalf("mode = %v", m.mode)
	}
	if names, _ := store.Names(); len(names) != 2 {
		t.Fatal("cancelled add must not save anything")
	}
}

func TestStatusMessagesFillRows(t *testing.T) {
	m, _ := newModel(t)
	m = send(t, m, statusMsg{cli.StatusRow{Name: "work", LoggedIn: false}})
	if row, ok := m.status["work"]; !ok || row.LoggedIn {
		t.Fatalf("status = %+v", m.status)
	}
	if !strings.Contains(m.View(), "logged out") {
		t.Fatal("view must show the logged-out badge")
	}
}

func TestViewListsAccounts(t *testing.T) {
	m, _ := newModel(t)
	m = send(t, m, statusMsg{cli.StatusRow{Name: "personal", Active: true, LoggedIn: true, Plan: "max", ExpiresAt: "2099-01-01T00:00:00Z"}})
	m = send(t, m, statusMsg{cli.StatusRow{Name: "work", LoggedIn: false}})
	view := m.View()
	t.Log("\n" + view)
	for _, want := range []string{"personal", "work", "a@example.com", "b@example.com", "enter"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view is missing %q:\n%s", want, view)
		}
	}
}

func TestQuit(t *testing.T) {
	m, _ := newModel(t)
	_, cmd := m.Update(key("q"))
	if cmd == nil {
		t.Fatal("q must quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q must return tea.Quit")
	}
}
