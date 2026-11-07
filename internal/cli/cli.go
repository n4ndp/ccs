// Package cli wires the ccs subcommands.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/n4ndp/ccs/internal/account"
	"github.com/n4ndp/ccs/internal/claude"
	"github.com/n4ndp/ccs/internal/ui"
)

// Version is set at build time with -ldflags "-X github.com/n4ndp/ccs/internal/cli.Version=...".
var Version = "dev"

// Deps are the collaborators the commands need; tests replace them.
type Deps struct {
	Store  *account.Store
	Claude claude.CLI
	In     io.Reader
	Out    io.Writer
	Err    io.Writer
	// Interactive reports whether stdin and stdout are terminals.
	Interactive bool
	// TUI opens the interactive interface; nil when unavailable.
	TUI func() error
}

// NewRoot builds the command tree.
func NewRoot(d Deps) *cobra.Command {
	root := &cobra.Command{
		Use:   "ccs",
		Short: "Switch between Claude Code accounts",
		Long: "ccs keeps several Claude Code logins and swaps the active one in place.\n" +
			"Running Claude Code sessions follow the switch without a restart.\n\n" +
			"Run `ccs` with no arguments in a terminal for the interactive view.",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if d.Interactive && d.TUI != nil {
				return d.TUI()
			}
			return printList(d, false)
		},
	}
	root.SetIn(d.In)
	root.SetOut(d.Out)
	root.SetErr(d.Err)
	names := completeNames(d.Store)

	var jsonOut bool
	list := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List saved accounts (● = active)", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return printList(d, jsonOut) },
	}
	list.Flags().BoolVar(&jsonOut, "json", false, "print JSON")

	var statusJSON bool
	status := &cobra.Command{
		Use: "status", Short: "Check every account's login with `claude auth status`", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return printStatus(d, statusJSON) },
	}
	status.Flags().BoolVar(&statusJSON, "json", false, "print JSON")

	current := &cobra.Command{
		Use: "current", Short: "Print the active account name", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			name := d.Store.ActiveName()
			if name == "" {
				return errors.New("the active login is not saved (run `ccs save <name>`)")
			}
			fmt.Fprintln(d.Out, name)
			return nil
		},
	}

	use := &cobra.Command{
		Use: "use [name]", Short: "Make an account active (running sessions follow)",
		Args: cobra.MaximumNArgs(1), ValidArgsFunction: names,
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				if d.Interactive && d.TUI != nil {
					return d.TUI()
				}
				return errors.New("missing account name (see `ccs list`)")
			}
			res, err := d.Store.Use(args[0])
			if err != nil {
				return err
			}
			printSwitch(d, res)
			return nil
		},
	}

	next := &cobra.Command{
		Use: "next", Short: "Switch to the next account (alphabetical, wraps around)", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			res, err := d.Store.Next()
			if err != nil {
				return err
			}
			printSwitch(d, res)
			return nil
		},
	}

	var saveForce bool
	save := &cobra.Command{
		Use: "save <name>", Short: "Save the currently logged-in account", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			acc, err := d.Store.Save(args[0], saveForce)
			if err != nil {
				return err
			}
			fmt.Fprintf(d.Out, "%s Saved %s as %s\n", ui.OK.Render("✓"), acc.Email(), ui.Bold.Render(acc.Name))
			return nil
		},
	}
	save.Flags().BoolVar(&saveForce, "force", false, "replace a different account saved under that name")

	var addEmail string
	var addForce bool
	add := &cobra.Command{
		Use:   "add <name>",
		Short: "Log in to a new account and save it (the active login is untouched)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := account.ValidateName(args[0]); err != nil {
				return err
			}
			dir, err := runLogin(d, addEmail)
			if err != nil {
				return err
			}
			acc, err := d.Store.Add(args[0], dir, addForce)
			if err != nil {
				return err
			}
			fmt.Fprintf(d.Out, "%s Added %s as %s\n", ui.OK.Render("✓"), acc.Email(), ui.Bold.Render(acc.Name))
			return nil
		},
	}
	add.Flags().StringVar(&addEmail, "email", "", "pre-fill the email on the login page")
	add.Flags().BoolVar(&addForce, "force", false, "replace a different account saved under that name")

	relogin := &cobra.Command{
		Use: "relogin <name>", Short: "Log in again to renew a saved account's tokens",
		Args: cobra.ExactArgs(1), ValidArgsFunction: names,
		RunE: func(_ *cobra.Command, args []string) error {
			saved, err := d.Store.Get(args[0])
			if err != nil {
				return err
			}
			dir, err := runLogin(d, saved.Email())
			if err != nil {
				return err
			}
			acc, err := d.Store.Relogin(args[0], dir)
			if err != nil {
				return err
			}
			fmt.Fprintf(d.Out, "%s Renewed %s (%s)\n", ui.OK.Render("✓"), ui.Bold.Render(acc.Name), acc.Email())
			return nil
		},
	}

	rename := &cobra.Command{
		Use: "rename <old> <new>", Short: "Rename a saved account", Args: cobra.ExactArgs(2), ValidArgsFunction: names,
		RunE: func(_ *cobra.Command, args []string) error {
			if err := d.Store.Rename(args[0], args[1]); err != nil {
				return err
			}
			fmt.Fprintf(d.Out, "%s Renamed %s → %s\n", ui.OK.Render("✓"), args[0], ui.Bold.Render(args[1]))
			return nil
		},
	}

	var yes bool
	remove := &cobra.Command{
		Use: "remove <name>", Aliases: []string{"rm"}, Short: "Forget a saved account (the live login is untouched)",
		Args: cobra.ExactArgs(1), ValidArgsFunction: names,
		RunE: func(_ *cobra.Command, args []string) error {
			acc, err := d.Store.Get(args[0])
			if err != nil {
				return err
			}
			if !yes {
				if !d.Interactive {
					return errors.New("refusing to remove without confirmation; pass -y")
				}
				if !confirm(d, fmt.Sprintf("Remove %s (%s)?", acc.Name, acc.Email())) {
					return errors.New("cancelled")
				}
			}
			if err := d.Store.Remove(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(d.Out, "%s Removed %s\n", ui.OK.Render("✓"), acc.Name)
			return nil
		},
	}
	remove.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")

	importOrca := &cobra.Command{
		Use: "import-orca", Short: "Import the accounts saved by the Orca app", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			imported, err := d.Store.ImportOrca()
			if err != nil {
				return err
			}
			if len(imported) == 0 {
				fmt.Fprintln(d.Out, "Nothing new to import.")
			}
			for _, acc := range imported {
				fmt.Fprintf(d.Out, "%s Imported %s as %s\n", ui.OK.Render("✓"), acc.Email(), ui.Bold.Render(acc.Name))
			}
			return nil
		},
	}

	root.AddCommand(list, status, current, use, next, save, add, relogin, rename, remove, importOrca)
	return root
}

// Execute runs ccs with the real environment.
func Execute(tui func(Deps) func() error) int {
	d := Deps{
		Store:       account.New(account.PathsFromEnv(os.Getenv)),
		Claude:      claude.Default(),
		In:          os.Stdin,
		Out:         os.Stdout,
		Err:         os.Stderr,
		Interactive: isTerminal(os.Stdin) && isTerminal(os.Stdout),
	}
	if tui != nil {
		d.TUI = tui(d)
	}
	if err := NewRoot(d).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, ui.Err.Render("ccs: ")+err.Error())
		return 1
	}
	return 0
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func completeNames(store *account.Store) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		accounts, _ := store.List()
		out := make([]string, 0, len(accounts))
		for _, a := range accounts {
			out = append(out, a.Name+"\t"+a.Email())
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

// runLogin runs `claude auth login` in a fresh isolated config dir and returns that dir.
func runLogin(d Deps, email string) (string, error) {
	dir, err := d.Store.NewLoginDir()
	if err != nil {
		return "", err
	}
	fmt.Fprintln(d.Err, ui.Faint.Render("Opening Claude Code login (your active account stays untouched)…"))
	cmd := d.Claude.LoginCmd(dir, email)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = d.In, d.Err, d.Err
	if err := cmd.Run(); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("claude auth login failed: %w", err)
	}
	return dir, nil
}

func confirm(d Deps, question string) bool {
	fmt.Fprintf(d.Err, "%s [y/N] ", question)
	line, _ := bufio.NewReader(d.In).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes" || answer == "s" || answer == "si" || answer == "sí"
}

func printSwitch(d Deps, res account.SwitchResult) {
	if res.AlreadyActive {
		fmt.Fprintf(d.Out, "%s %s is already active\n", ui.ActiveDot(true), ui.Bold.Render(res.Account.Name))
		return
	}
	fmt.Fprintf(d.Out, "%s Switched to %s %s\n", ui.ActiveDot(true), ui.Bold.Render(res.Account.Name), ui.Faint.Render(res.Account.Email()))
}

type listRow struct {
	Name   string `json:"name"`
	Email  string `json:"email"`
	Org    string `json:"org,omitempty"`
	UUID   string `json:"accountUuid"`
	Active bool   `json:"active"`
}

func printList(d Deps, asJSON bool) error {
	accounts, err := d.Store.List()
	if err != nil {
		return err
	}
	if asJSON {
		rows := make([]listRow, 0, len(accounts))
		for _, a := range accounts {
			rows = append(rows, listRow{a.Name, a.Email(), a.OrgName(), a.UUID(), a.Active})
		}
		return writeJSON(d.Out, rows)
	}
	if len(accounts) == 0 {
		fmt.Fprintln(d.Out, "No saved accounts yet. Run `ccs add <name>` to log in and save one.")
		return nil
	}
	width := 0
	for _, a := range accounts {
		width = max(width, lipgloss.Width(a.Name))
	}
	for _, a := range accounts {
		fmt.Fprintf(d.Out, "%s %s  %s\n", ui.ActiveDot(a.Active), pad(nameStyle(a.Active).Render(a.Name), width), ui.Faint.Render(a.Email()))
	}
	if d.Store.LiveUUID() != "" && d.Store.ActiveName() == "" {
		fmt.Fprintln(d.Out, ui.Warn.Render("\nThe active login is not saved. Run `ccs save <name>`."))
	}
	return nil
}

func nameStyle(active bool) lipgloss.Style {
	if active {
		return ui.Bold
	}
	return lipgloss.NewStyle()
}

func pad(s string, width int) string {
	return s + strings.Repeat(" ", max(0, width-lipgloss.Width(s)))
}

// StatusRow is one account's checked login state.
type StatusRow struct {
	Name      string `json:"name"`
	Email     string `json:"email"`
	Active    bool   `json:"active"`
	LoggedIn  bool   `json:"loggedIn"`
	Plan      string `json:"plan,omitempty"`
	ExpiresAt string `json:"loginExpiresAt,omitempty"`
	Error     string `json:"error,omitempty"`
}

// CheckAll checks every account concurrently.
func CheckAll(store *account.Store, c claude.CLI) ([]StatusRow, error) {
	accounts, err := store.List()
	if err != nil {
		return nil, err
	}
	rows := make([]StatusRow, len(accounts))
	var wg sync.WaitGroup
	for i, a := range accounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows[i] = CheckOne(store, c, a)
		}()
	}
	wg.Wait()
	return rows, nil
}

// CheckOne checks a single account.
func CheckOne(store *account.Store, c claude.CLI, a account.Account) StatusRow {
	row := StatusRow{Name: a.Name, Email: a.Email(), Active: a.Active}
	st, err := store.Check(a.Name, func(dir string) (claude.Status, error) {
		return c.Status(context.Background(), dir)
	})
	if err != nil {
		row.Error = err.Error()
		return row
	}
	row.LoggedIn, row.Plan, row.ExpiresAt = st.LoggedIn, st.Plan(), st.LoginExpiresAt
	return row
}

func printStatus(d Deps, asJSON bool) error {
	rows, err := CheckAll(d.Store, d.Claude)
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSON(d.Out, rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(d.Out, "No saved accounts yet. Run `ccs add <name>` to log in and save one.")
		return nil
	}
	width := 0
	for _, r := range rows {
		width = max(width, lipgloss.Width(r.Name))
	}
	now := time.Now()
	for _, r := range rows {
		fmt.Fprintf(d.Out, "%s %s  %s  %s\n", ui.ActiveDot(r.Active), pad(nameStyle(r.Active).Render(r.Name), width), StatusBadge(r, now), ui.Faint.Render(r.Email))
	}
	return nil
}

// StatusBadge renders a row's login state in a few characters.
func StatusBadge(r StatusRow, now time.Time) string {
	switch {
	case r.Error != "":
		return ui.Err.Render("✗ error")
	case !r.LoggedIn:
		return ui.Err.Render("✗ logged out")
	}
	expires, _ := time.Parse(time.RFC3339Nano, r.ExpiresAt)
	parts := []string{ui.OK.Render("✓")}
	if p := ui.Plan(r.Plan); p != "" {
		parts = append(parts, p)
	}
	if e := ui.Expiry(expires, now); e != "" {
		parts = append(parts, e)
	}
	return strings.Join(parts, " ")
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
