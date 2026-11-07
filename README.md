# ccs — Claude Code account switcher

Keep several Claude Code logins on one machine and switch between them in one keystroke.
Running Claude Code sessions follow the switch without a restart.

```
  ccs  Claude Code accounts

  ▸ ● personal  me@example.com    ✓ PRO valid 27d
    ○ work      me@company.com    ✓ MAX valid 18d
    ○ old       me@old.com        ✗ logged out · press l to log in again

  ↑↓ move · enter use · n next · a add · ? more · q quit
```

A single Go binary. Linux only for now (see [Platform support](#platform-support)).

## Usage

Run `ccs` in a terminal for the interactive view:

| Key | Action |
|---|---|
| `↑`/`k` `↓`/`j` | Move |
| `enter` | Use the selected account |
| `n` | Switch to the next account |
| `a` | Add an account: opens the Claude Code login, then saves it |
| `l` | Log in again to renew the selected account's tokens |
| `r` | Rename |
| `d` | Remove (asks first) |
| `s` | Refresh login status |
| `?` | Help |
| `q` | Quit |

Every action is also a subcommand, for scripts and agents:

| Command | Effect |
|---|---|
| `ccs list [--json]` | List saved accounts; `●` marks the active one |
| `ccs status [--json]` | Check every account's login (plan, expiry) through `claude auth status` |
| `ccs current` | Print the active account name |
| `ccs use <name>` | Make `<name>` the active account |
| `ccs next` | Switch to the next account (alphabetical, wraps around) |
| `ccs add <name> [--email you@x.com]` | Log in to a new account and save it |
| `ccs relogin <name>` | Log in again to renew a saved account |
| `ccs save <name>` | Save the account you are currently logged in with |
| `ccs rename <old> <new>` | Rename a saved account |
| `ccs remove <name> [-y]` | Forget a saved account (the live login is untouched) |
| `ccs import-orca` | Import the accounts saved by the Orca app |
| `ccs completion <zsh\|bash\|fish>` | Shell completion, including account names |

Typical day:

```bash
ccs add personal          # log in once per account
ccs add work --email me@company.com
ccs next                  # quota ran out? rotate
```

## How it works

Claude Code reads its login from two files:

| File | Holds |
|---|---|
| `~/.claude/.credentials.json` | OAuth access and refresh tokens |
| `~/.claude.json` → `oauthAccount` | Identity shown by `/usage` and `/config` |

`ccs` keeps a copy of both per account in `~/.claude-accounts/<name>/` (mode `700`/`600`) and
switching rewrites the live files atomically. Claude Code re-reads the credentials file from
disk, so a session that is already running continues on the new account.

- **Adding an account never touches the live login.** `ccs add` runs `claude auth login` with
  `CLAUDE_CONFIG_DIR` pointing at a private temporary folder and captures the result.
- **Rotated tokens are never lost.** Before every switch, the live credentials are copied back
  to the active account, because a running session may have refreshed them.
- **Nothing is overwritten blindly.** Every switch backs up the live files to
  `~/.claude-accounts/.backups/` (last 20 kept), and `ccs` refuses to switch away from a login
  that is not saved.
- **Status checks are safe.** The active account is checked against the live files. Inactive
  accounts are checked in a temporary copy; refreshed tokens are saved back only when they are
  valid and newer, so a rejected refresh never wipes a saved login.

`CLAUDE_CONFIG_DIR` is honoured like Claude Code does. `CCS_HOME` moves the account store and
`CCS_CLAUDE_BIN` points at a different `claude` binary.

## Install

Requirements: Linux, Go 1.24+, and Claude Code (`claude`) on `PATH`.

```bash
git clone https://github.com/n4ndp/ccs.git ~/Proyectos/ccs
~/Proyectos/ccs/install.sh
```

`install.sh` builds the binary into `~/.local/bin/ccs` (override with `CCS_BIN_DIR`) and is safe
to re-run; run it again after `git pull` to update.

## For AI agents: install checklist

Follow these steps exactly and report the output of each verification.

1. Check prerequisites: `go version` must print 1.24 or newer, and `claude --version` must work.
   If Go is missing, ask the user before installing it.
2. Clone and install:
   ```bash
   git clone https://github.com/n4ndp/ccs.git ~/Proyectos/ccs && ~/Proyectos/ccs/install.sh
   ```
   If the repository already exists, run `git -C ~/Proyectos/ccs pull && ~/Proyectos/ccs/install.sh`.
3. Verify the binary: `ccs --version`. If `ccs` is not found, add
   `export PATH="$HOME/.local/bin:$PATH"` to the user's shell rc file and open a new shell.
4. Run the tests (they use throwaway HOME directories, never real credentials):
   `cd ~/Proyectos/ccs && go test ./...` must report `ok` for every package.
5. Enable completion for the user's shell, for example for zsh:
   `ccs completion zsh > "${fpath[1]}/_ccs"`.
6. If `~/.config/orca` exists, run `ccs import-orca`. Then run `ccs status` and show the result.
7. Adding accounts is interactive (browser login), so hand over to the user:
   "Run `ccs` and press `a`, or `ccs add <name>`, once per account."

Rules for agents:
- Never print, copy elsewhere, or commit the contents of `.credentials.json` or `~/.claude-accounts/`.
- Do not run `ccs use` or `ccs next` unless asked: they change the account of running sessions.

## Platform support

Linux only. On macOS Claude Code stores credentials in the Keychain instead of
`~/.claude/.credentials.json`, which `ccs` does not support yet.

## Caveats

- Switching is global: every Claude Code session using the same config dir moves to the new account.
- Using several accounts to stack usage limits may conflict with Anthropic's terms of service.
- Do not switch from two tools at once (for example `ccs` and Orca's account switcher): both
  rewrite the same files.

## Development

```bash
go test ./...
go run . status
```

Layout: `internal/account` (store and switching), `internal/claude` (Claude Code CLI),
`internal/cli` (subcommands), `internal/tui` (interactive view), `internal/ui` (shared styles).
