// Package tui is the interactive account picker shown by `ccs` in a terminal.
package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/n4ndp/ccs/internal/account"
	"github.com/n4ndp/ccs/internal/claude"
	"github.com/n4ndp/ccs/internal/cli"
	"github.com/n4ndp/ccs/internal/ui"
)

type mode int

const (
	modeList mode = iota
	modeAdd
	modeRename
	modeConfirmDelete
)

type (
	statusMsg    struct{ row cli.StatusRow }
	loginDoneMsg struct {
		dir, name string
		relogin   bool
		err       error
	}
)

// Model is the Bubble Tea model.
type Model struct {
	store  *account.Store
	claude claude.CLI
	check  func(account.Account) cli.StatusRow

	accounts []account.Account
	status   map[string]cli.StatusRow
	checking map[string]bool
	cursor   int
	mode     mode
	input    textinput.Model
	spinner  spinner.Model
	flash    string
	flashErr bool
	showHelp bool
	width    int
}

// New builds the model and loads the saved accounts.
func New(store *account.Store, c claude.CLI) Model {
	in := textinput.New()
	in.CharLimit = 64
	in.Prompt = "› "
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(lipgloss.NewStyle().Foreground(ui.Accent)))
	m := Model{
		store:    store,
		claude:   c,
		status:   map[string]cli.StatusRow{},
		checking: map[string]bool{},
		input:    in,
		spinner:  sp,
	}
	m.check = func(a account.Account) cli.StatusRow { return cli.CheckOne(store, c, a) }
	m.reload("")
	return m
}

// Run opens the TUI on the real terminal.
func Run(store *account.Store, c claude.CLI) error {
	_, err := tea.NewProgram(New(store, c), tea.WithAltScreen()).Run()
	return err
}

// reload re-reads the accounts, keeping the cursor on focus (or the active account).
func (m *Model) reload(focus string) {
	accounts, err := m.store.List()
	if err != nil {
		m.setFlash(err.Error(), true)
	}
	m.accounts = accounts
	if focus == "" {
		focus = m.store.ActiveName()
	}
	m.cursor = min(m.cursor, max(0, len(accounts)-1))
	for i, a := range accounts {
		if a.Name == focus {
			m.cursor = i
		}
	}
}

func (m Model) selected() account.Account {
	if len(m.accounts) == 0 {
		return account.Account{}
	}
	return m.accounts[m.cursor]
}

func (m *Model) setFlash(msg string, isErr bool) { m.flash, m.flashErr = msg, isErr }

// checkAll starts an async status check for every account.
func (m *Model) checkAll() tea.Cmd {
	cmds := []tea.Cmd{m.spinner.Tick}
	for _, a := range m.accounts {
		cmds = append(cmds, m.checkOne(a))
	}
	return tea.Batch(cmds...)
}

func (m *Model) checkOne(a account.Account) tea.Cmd {
	m.checking[a.Name] = true
	check := m.check
	return func() tea.Msg { return statusMsg{check(a)} }
}

func (m Model) Init() tea.Cmd { return m.checkAll() }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case spinner.TickMsg:
		if len(m.checking) == 0 {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case statusMsg:
		delete(m.checking, msg.row.Name)
		m.status[msg.row.Name] = msg.row
		return m, nil
	case loginDoneMsg:
		return m.finishLogin(msg)
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		switch m.mode {
		case modeAdd, modeRename:
			return m.updatePrompt(msg)
		case modeConfirmDelete:
			return m.updateConfirm(msg)
		}
		return m.updateList(msg)
	}
	return m, nil
}

func (m Model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		return m, tea.Quit
	case "up", "k":
		m.cursor = max(0, m.cursor-1)
	case "down", "j":
		m.cursor = min(max(0, len(m.accounts)-1), m.cursor+1)
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		m.cursor = max(0, len(m.accounts)-1)
	case "?":
		m.showHelp = !m.showHelp
	case "enter", " ":
		if len(m.accounts) == 0 {
			return m, nil
		}
		res, err := m.store.Use(m.selected().Name)
		return m.afterSwitch(res, err)
	case "n":
		res, err := m.store.Next()
		return m.afterSwitch(res, err)
	case "s":
		m.setFlash("Checking accounts…", false)
		return m, m.checkAll()
	case "a":
		m.mode = modeAdd
		m.input.SetValue("")
		m.input.Placeholder = "name for the new account"
		m.setFlash("", false)
		return m, m.input.Focus()
	case "r":
		if len(m.accounts) == 0 {
			return m, nil
		}
		m.mode = modeRename
		m.input.SetValue(m.selected().Name)
		m.input.Placeholder = "new name"
		m.input.CursorEnd()
		m.setFlash("", false)
		return m, m.input.Focus()
	case "d", "x", "delete":
		if len(m.accounts) > 0 {
			m.mode = modeConfirmDelete
		}
	case "l":
		if len(m.accounts) == 0 {
			return m, nil
		}
		a := m.selected()
		return m.startLogin(a.Name, a.Email(), true)
	}
	return m, nil
}

func (m Model) afterSwitch(res account.SwitchResult, err error) (tea.Model, tea.Cmd) {
	if err != nil {
		m.setFlash(err.Error(), true)
		return m, nil
	}
	m.reload(res.Account.Name)
	if res.AlreadyActive {
		m.setFlash(res.Account.Name+" is already active", false)
		return m, nil
	}
	m.setFlash("Switched to "+res.Account.Name+" · running sessions follow", false)
	return m, m.checkAll()
}

func (m Model) updatePrompt(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.input.Blur()
		m.setFlash("Cancelled", false)
		return m, nil
	case "enter":
		name := strings.TrimSpace(m.input.Value())
		if err := account.ValidateName(name); err != nil {
			m.setFlash(err.Error(), true)
			return m, nil
		}
		m.input.Blur()
		current := m.mode
		m.mode = modeList
		if current == modeRename {
			from := m.selected().Name
			if name == from {
				return m, nil
			}
			if err := m.store.Rename(from, name); err != nil {
				m.setFlash(err.Error(), true)
				return m, nil
			}
			if row, ok := m.status[from]; ok {
				row.Name = name
				m.status[name] = row
				delete(m.status, from)
			}
			m.reload(name)
			m.setFlash("Renamed "+from+" → "+name, false)
			return m, nil
		}
		if _, err := m.store.Get(name); err == nil {
			m.setFlash(fmt.Sprintf("%q already exists", name), true)
			m.mode = modeAdd
			return m, m.input.Focus()
		}
		return m.startLogin(name, "", false)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.mode = modeList
	if s := strings.ToLower(msg.String()); s != "y" && s != "s" {
		m.setFlash("Kept "+m.selected().Name, false)
		return m, nil
	}
	name := m.selected().Name
	if err := m.store.Remove(name); err != nil {
		m.setFlash(err.Error(), true)
		return m, nil
	}
	delete(m.status, name)
	m.reload(m.store.ActiveName())
	m.setFlash("Removed "+name, false)
	return m, nil
}

// startLogin suspends the TUI and runs `claude auth login` in an isolated config dir.
func (m Model) startLogin(name, email string, relogin bool) (tea.Model, tea.Cmd) {
	dir, err := m.store.NewLoginDir()
	if err != nil {
		m.setFlash(err.Error(), true)
		return m, nil
	}
	cmd := m.claude.LoginCmd(dir, email)
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return loginDoneMsg{dir: dir, name: name, relogin: relogin, err: err}
	})
}

func (m Model) finishLogin(msg loginDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		_ = os.RemoveAll(msg.dir)
		m.setFlash("Login failed: "+msg.err.Error(), true)
		return m, nil
	}
	var (
		acc account.Account
		err error
	)
	if msg.relogin {
		acc, err = m.store.Relogin(msg.name, msg.dir)
	} else {
		acc, err = m.store.Add(msg.name, msg.dir, false)
	}
	if err != nil {
		m.setFlash(err.Error(), true)
		return m, nil
	}
	m.reload(acc.Name)
	verb := "Added "
	if msg.relogin {
		verb = "Renewed "
	}
	m.setFlash(verb+acc.Name+" ("+acc.Email()+")", false)
	return m, tea.Batch(m.spinner.Tick, m.checkOne(m.selected()))
}

// --- view ----------------------------------------------------------------------

var (
	frame  = lipgloss.NewStyle().Padding(1, 2)
	cursor = lipgloss.NewStyle().Foreground(ui.Accent).Bold(true)
	keyCap = lipgloss.NewStyle().Foreground(ui.Accent)
)

func (m Model) View() string {
	var b strings.Builder
	b.WriteString(ui.Title.Render("ccs") + ui.Faint.Render("  Claude Code accounts") + "\n\n")

	if len(m.accounts) == 0 {
		b.WriteString(ui.Faint.Render("No saved accounts yet. Press ") + keyCap.Render("a") + ui.Faint.Render(" to log in and add one.") + "\n")
	}
	nameW, emailW := 0, 0
	for _, a := range m.accounts {
		nameW = max(nameW, lipgloss.Width(a.Name))
		emailW = max(emailW, lipgloss.Width(a.Email()))
	}
	now := time.Now()
	for i, a := range m.accounts {
		pointer := "  "
		if i == m.cursor {
			pointer = cursor.Render("▸ ")
		}
		name := pad(a.Name, nameW)
		if a.Active {
			name = ui.Bold.Render(name)
		}
		b.WriteString(pointer + ui.ActiveDot(a.Active) + " " + name + "  " + ui.Faint.Render(pad(a.Email(), emailW)) + "  " + m.badge(a, now) + "\n")
	}

	b.WriteString("\n")
	switch m.mode {
	case modeAdd:
		b.WriteString(ui.Bold.Render("Add account") + ui.Faint.Render("  Claude Code login opens next; your active account stays untouched") + "\n")
		b.WriteString(m.input.View() + "\n" + hint("enter", "log in", "esc", "cancel") + "\n")
	case modeRename:
		b.WriteString(ui.Bold.Render("Rename "+m.selected().Name) + "\n")
		b.WriteString(m.input.View() + "\n" + hint("enter", "rename", "esc", "cancel") + "\n")
	case modeConfirmDelete:
		a := m.selected()
		b.WriteString(ui.Warn.Render(fmt.Sprintf("Remove %s (%s)? ", a.Name, a.Email())) + hint("y", "yes", "any key", "no") + "\n")
	default:
		if m.flash != "" {
			style := ui.OK
			icon := "✓ "
			if m.flashErr {
				style, icon = ui.Err, "✗ "
			}
			b.WriteString(style.Render(icon+m.flash) + "\n")
		}
		if m.showHelp {
			b.WriteString("\n" + m.helpView() + "\n")
		} else {
			b.WriteString(hint("↑↓", "move", "enter", "use", "n", "next", "a", "add", "?", "more", "q", "quit") + "\n")
		}
	}
	return frame.Render(b.String())
}

func (m Model) badge(a account.Account, now time.Time) string {
	if m.checking[a.Name] {
		return m.spinner.View() + ui.Faint.Render(" checking")
	}
	row, ok := m.status[a.Name]
	if !ok {
		return ""
	}
	if !row.LoggedIn && row.Error == "" {
		return ui.Err.Render("✗ logged out") + ui.Faint.Render(" · press l to log in again")
	}
	return cli.StatusBadge(row, now)
}

func (m Model) helpView() string {
	rows := [][2]string{
		{"↑/k ↓/j", "move"}, {"enter", "use the selected account"}, {"n", "switch to the next account"},
		{"a", "add an account (opens Claude Code login)"}, {"l", "log in again (renew tokens)"},
		{"r", "rename"}, {"d", "remove (asks first)"}, {"s", "refresh login status"},
		{"?", "toggle this help"}, {"q", "quit"},
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString("  " + keyCap.Render(pad(r[0], 9)) + " " + ui.Faint.Render(r[1]) + "\n")
	}
	b.WriteString(ui.Faint.Render("\n  Switching rewrites Claude Code's live login, so running sessions follow."))
	return b.String()
}

func hint(pairs ...string) string {
	parts := make([]string, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, keyCap.Render(pairs[i])+" "+ui.Faint.Render(pairs[i+1]))
	}
	return strings.Join(parts, ui.Faint.Render(" · "))
}

func pad(s string, w int) string { return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s))) }
