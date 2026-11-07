package claude

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeClaude writes a stub `claude` that echoes its CLAUDE_CONFIG_DIR into the status JSON.
func fakeClaude(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
if [ "$1 $2" = "auth status" ]; then
  printf '{"loggedIn":true,"email":"x@example.com","subscriptionType":"max","planTier":"max","loginExpiresAt":"2026-12-05T12:25:49.611Z","configDirectory":"%s"}' "${CLAUDE_CONFIG_DIR:-default}"
  exit 0
fi
exit 3
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestStatusPassesConfigDir(t *testing.T) {
	cli := CLI{Bin: fakeClaude(t)}
	tests := []struct {
		name, dir, want string
	}{
		{"isolated copy", "/tmp/acc", "/tmp/acc"},
		{"live files", "", "default"},
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := cli.Status(context.Background(), tt.dir)
			if err != nil {
				t.Fatal(err)
			}
			if !st.LoggedIn || st.Plan() != "max" || st.ConfigDirectory != tt.want {
				t.Fatalf("status = %+v", st)
			}
			if st.ExpiresAt().IsZero() {
				t.Fatal("loginExpiresAt must parse")
			}
		})
	}
}

func TestStatusReportsCommandFailure(t *testing.T) {
	cli := CLI{Bin: filepath.Join(t.TempDir(), "missing-claude")}
	if _, err := cli.Status(context.Background(), ""); err == nil {
		t.Fatal("a missing binary must be an error")
	}
}

func TestLoginCmd(t *testing.T) {
	cmd := CLI{Bin: "claude"}.LoginCmd("/tmp/login", "me@example.com")
	if !slices.Equal(cmd.Args[1:], []string{"auth", "login", "--email", "me@example.com"}) {
		t.Fatalf("args = %v", cmd.Args)
	}
	var dirs []string
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			dirs = append(dirs, kv)
		}
	}
	if !slices.Equal(dirs, []string{"CLAUDE_CONFIG_DIR=/tmp/login"}) {
		t.Fatalf("CLAUDE_CONFIG_DIR entries = %v", dirs)
	}
}
