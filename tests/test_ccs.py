"""End-to-end tests for ccs. Every test runs against a throwaway HOME with fake credentials."""

import json
import os
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

CCS = Path(__file__).resolve().parent.parent / "ccs"


def account(uuid, email):
    return {"accountUuid": uuid, "emailAddress": email, "organizationUuid": "org-" + uuid}


def creds(token):
    return {"claudeAiOauth": {"accessToken": "at-" + token, "refreshToken": "rt-" + token}}


class CcsTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.home = Path(self.tmp.name)
        self.claude = self.home / ".claude"
        self.claude.mkdir()
        self.store = self.home / ".claude-accounts"
        self.login(account("uuid-a", "a@example.com"), creds("a1"))

    def tearDown(self):
        self.tmp.cleanup()

    # helpers -----------------------------------------------------------
    def login(self, oauth_account, credentials):
        """Simulate `claude /login`: rewrite the live credentials and identity."""
        (self.claude / ".credentials.json").write_text(json.dumps(credentials))
        state_path = self.home / ".claude.json"
        state = json.loads(state_path.read_text()) if state_path.exists() else {"numStartups": 7}
        state["oauthAccount"] = oauth_account
        state_path.write_text(json.dumps(state))

    def run_ccs(self, *args):
        env = {k: v for k, v in os.environ.items() if k not in ("CLAUDE_CONFIG_DIR", "CCS_HOME")}
        env["HOME"] = str(self.home)
        return subprocess.run([sys.executable, str(CCS), *args], env=env, capture_output=True, text=True)

    def live_creds(self):
        return json.loads((self.claude / ".credentials.json").read_text())

    def live_state(self):
        return json.loads((self.home / ".claude.json").read_text())

    def save_two_accounts(self):
        self.assertEqual(self.run_ccs("save", "work").returncode, 0)
        self.login(account("uuid-b", "b@example.com"), creds("b1"))
        self.assertEqual(self.run_ccs("save", "personal").returncode, 0)

    # tests -------------------------------------------------------------
    def test_save_stores_account_privately(self):
        result = self.run_ccs("save", "work")
        self.assertEqual(result.returncode, 0, result.stderr)
        saved = self.store / "work"
        self.assertEqual(json.loads((saved / "credentials.json").read_text()), creds("a1"))
        self.assertEqual(json.loads((saved / "account.json").read_text())["accountUuid"], "uuid-a")
        self.assertEqual(stat.S_IMODE(saved.stat().st_mode), 0o700)
        self.assertEqual(stat.S_IMODE((saved / "credentials.json").stat().st_mode), 0o600)

    def test_list_marks_active_account(self):
        self.save_two_accounts()
        result = self.run_ccs("list")
        self.assertEqual(result.returncode, 0, result.stderr)
        lines = result.stdout.splitlines()
        self.assertTrue(any(l.startswith("*") and "personal" in l and "b@example.com" in l for l in lines), lines)
        self.assertTrue(any(l.startswith(" ") and "work" in l for l in lines), lines)

    def test_use_switches_credentials_and_identity(self):
        self.save_two_accounts()
        result = self.run_ccs("use", "work")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.live_creds(), creds("a1"))
        state = self.live_state()
        self.assertEqual(state["oauthAccount"]["accountUuid"], "uuid-a")
        self.assertEqual(state["numStartups"], 7, "unrelated .claude.json fields must survive")
        self.assertEqual(stat.S_IMODE((self.claude / ".credentials.json").stat().st_mode), 0o600)

    def test_use_syncs_refreshed_token_back_before_switching(self):
        self.save_two_accounts()
        # A running session refreshes the active account's token on disk.
        (self.claude / ".credentials.json").write_text(json.dumps(creds("b2")))
        self.assertEqual(self.run_ccs("use", "work").returncode, 0)
        saved_b = json.loads((self.store / "personal" / "credentials.json").read_text())
        self.assertEqual(saved_b, creds("b2"), "the refreshed token must not be lost")
        self.assertEqual(self.run_ccs("use", "personal").returncode, 0)
        self.assertEqual(self.live_creds(), creds("b2"))

    def test_use_refuses_when_current_account_is_unsaved(self):
        self.assertEqual(self.run_ccs("save", "work").returncode, 0)
        self.login(account("uuid-c", "c@example.com"), creds("c1"))
        result = self.run_ccs("use", "work")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("ccs save", result.stderr)
        self.assertEqual(self.live_creds(), creds("c1"), "live credentials must stay untouched")

    def test_use_unknown_account_fails_without_changes(self):
        self.assertEqual(self.run_ccs("save", "work").returncode, 0)
        result = self.run_ccs("use", "nope")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.live_creds(), creds("a1"))

    def test_use_writes_a_backup(self):
        self.save_two_accounts()
        self.assertEqual(self.run_ccs("use", "work").returncode, 0)
        backups = list((self.store / ".backups").iterdir())
        self.assertEqual(len(backups), 1)
        self.assertEqual(json.loads((backups[0] / "credentials.json").read_text()), creds("b1"))

    def test_save_refuses_to_overwrite_a_different_account(self):
        self.assertEqual(self.run_ccs("save", "work").returncode, 0)
        self.login(account("uuid-b", "b@example.com"), creds("b1"))
        result = self.run_ccs("save", "work")
        self.assertNotEqual(result.returncode, 0)
        saved = json.loads((self.store / "work" / "account.json").read_text())
        self.assertEqual(saved["accountUuid"], "uuid-a")

    def test_save_rejects_unsafe_names(self):
        result = self.run_ccs("save", "../evil")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.home / "evil").exists())

    def test_remove_deletes_saved_account(self):
        self.save_two_accounts()
        self.assertEqual(self.run_ccs("remove", "work").returncode, 0)
        self.assertFalse((self.store / "work").exists())

    def test_import_orca_prefers_live_token_for_active_account(self):
        orca = self.home / ".config" / "orca" / "claude-accounts"
        for folder, acc, cred in (
            ("id-1", account("uuid-a", "alpha@example.com"), creds("a0-stale")),
            ("id-2", account("uuid-b", "beta@example.com"), creds("b1")),
        ):
            auth = orca / folder / "auth"
            auth.mkdir(parents=True)
            (auth / "oauth-account.json").write_text(json.dumps(acc))
            (auth / ".credentials.json").write_text(json.dumps(cred))
        result = self.run_ccs("import-orca")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads((self.store / "alpha" / "credentials.json").read_text()), creds("a1"))
        self.assertEqual(json.loads((self.store / "beta" / "credentials.json").read_text()), creds("b1"))
        # Re-running is a no-op.
        self.assertEqual(self.run_ccs("import-orca").returncode, 0)
        self.assertEqual(sorted(p.name for p in self.store.iterdir() if not p.name.startswith(".")), ["alpha", "beta"])

    def test_respects_claude_config_dir(self):
        custom = self.home / "custom"
        custom.mkdir()
        (custom / ".credentials.json").write_text(json.dumps(creds("x1")))
        (custom / ".claude.json").write_text(json.dumps({"oauthAccount": account("uuid-x", "x@example.com")}))
        env = {k: v for k, v in os.environ.items() if k != "CCS_HOME"}
        env.update(HOME=str(self.home), CLAUDE_CONFIG_DIR=str(custom))
        result = subprocess.run([sys.executable, str(CCS), "save", "x"], env=env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads((self.store / "x" / "credentials.json").read_text()), creds("x1"))


if __name__ == "__main__":
    unittest.main()
