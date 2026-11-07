# ccs — Claude Code account switcher

Keep several Claude Code logins on one machine and switch between them with one command.
Running Claude Code sessions follow the switch without a restart.

```console
$ ccs list
* personal  me@example.com
  work      me@company.com
$ ccs use work
Switched to 'work' - me@company.com.
```

## How it works

Claude Code reads its login from two files:

| File | Holds |
|---|---|
| `~/.claude/.credentials.json` | OAuth access and refresh tokens |
| `~/.claude.json` → `oauthAccount` | Identity shown by `/usage` and `/config` |

`ccs` stores a copy of both per account in `~/.claude-accounts/<name>/` (mode `700`/`600`).
`ccs use <name>` rewrites the live files atomically. Claude Code re-reads the credentials file
from disk, so an agent that is already running continues on the new account.

Before every switch, `ccs`:

1. Copies the live credentials back into the active account's folder, because a running session
   may have refreshed (rotated) its token. Without this the saved copy would go stale.
2. Writes a backup of both live files to `~/.claude-accounts/.backups/` (last 20 kept).
3. Refuses to switch if the active login is not saved, so no login is ever lost.

If `CLAUDE_CONFIG_DIR` is set, `ccs` uses `$CLAUDE_CONFIG_DIR/.credentials.json` and
`$CLAUDE_CONFIG_DIR/.claude.json` instead. Set `CCS_HOME` to move the account store.

## Commands

| Command | Effect |
|---|---|
| `ccs list` (or `ccs`) | List saved accounts; `*` marks the active one |
| `ccs current` | Print the active account name |
| `ccs save <name>` | Save the currently logged-in account as `<name>` |
| `ccs use <name>` | Make `<name>` the active account |
| `ccs remove <name>` | Forget a saved account (the live login is untouched) |
| `ccs import-orca` | Import accounts saved by the Orca app (`~/.config/orca/claude-accounts`) |

## Install

Requirements: Linux or macOS with credentials stored in `~/.claude/.credentials.json`, and Python 3.8+.
No other dependencies.

```bash
git clone https://github.com/n4ndp/ccs.git ~/Proyectos/ccs
~/Proyectos/ccs/install.sh
```

`install.sh` links `ccs` into `~/.local/bin` (override with `CCS_BIN_DIR`) and is safe to re-run.

## Add accounts

Saved credentials never live in this repository, so after a fresh install add each account once:

```bash
claude            # log in with account 1 (/login), then exit
ccs save personal
claude            # run /login with account 2, then exit
ccs save work
ccs list
```

Do **not** run `/logout` between accounts: it may invalidate the token you just saved.
Use `/login` to sign in with the next account instead.

## For AI agents: install checklist

Follow these steps exactly and report the output of each verification.

1. Clone and install:
   ```bash
   git clone https://github.com/n4ndp/ccs.git ~/Proyectos/ccs && ~/Proyectos/ccs/install.sh
   ```
2. Verify the binary: `ccs --version` prints `ccs <version>`. If `ccs` is not found, add
   `export PATH="$HOME/.local/bin:$PATH"` to the user's shell rc file and open a new shell.
3. Run the test suite (uses a throwaway HOME, never real credentials):
   `python3 -m unittest discover -s ~/Proyectos/ccs/tests` must end with `OK`.
4. If `~/.config/orca/claude-accounts` exists, run `ccs import-orca`. Otherwise tell the user to
   add accounts with the steps in "Add accounts" — logging in is interactive and needs the user.
5. Run `ccs list` and show the result.

Rules for agents:
- Never print, copy elsewhere, or commit the contents of `.credentials.json` or `~/.claude-accounts/`.
- Do not run `ccs use` on the user's behalf unless asked; it changes the account of running sessions.

## Caveats

- Switching is global: every Claude Code session using the same config dir moves to the new account.
- Using several accounts to stack usage limits may conflict with Anthropic's terms of service.
- Do not switch from two tools at once (for example `ccs` and Orca's account switcher): both
  rewrite the same files.

## Development

```bash
python3 -m unittest discover -s tests
```
