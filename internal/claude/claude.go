// Package claude drives the Claude Code CLI (`claude auth ...`).
package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Status mirrors `claude auth status --json`.
type Status struct {
	LoggedIn         bool   `json:"loggedIn"`
	AuthMethod       string `json:"authMethod"`
	Email            string `json:"email"`
	OrgName          string `json:"orgName"`
	SubscriptionType string `json:"subscriptionType"`
	PlanTier         string `json:"planTier"`
	LoginExpiresAt   string `json:"loginExpiresAt"`
	ConfigDirectory  string `json:"configDirectory"`
}

// Plan returns the subscription plan ("pro", "max", ...), or "".
func (s Status) Plan() string {
	if s.PlanTier != "" {
		return s.PlanTier
	}
	return s.SubscriptionType
}

// ExpiresAt parses loginExpiresAt; zero when absent or unparsable.
func (s Status) ExpiresAt() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s.LoginExpiresAt)
	return t
}

// CLI runs the `claude` binary.
type CLI struct{ Bin string }

// Default uses $CCS_CLAUDE_BIN or `claude` from PATH.
func Default() CLI {
	if bin := os.Getenv("CCS_CLAUDE_BIN"); bin != "" {
		return CLI{Bin: bin}
	}
	return CLI{Bin: "claude"}
}

// env returns the process environment with CLAUDE_CONFIG_DIR forced to configDir.
// An empty configDir inherits the caller's environment (the live login).
func env(configDir string) []string {
	base := os.Environ()
	if configDir == "" {
		return base
	}
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if !strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			out = append(out, kv)
		}
	}
	return append(out, "CLAUDE_CONFIG_DIR="+configDir)
}

// LoginCmd builds `claude auth login` writing into configDir. The caller attaches the terminal.
func (c CLI) LoginCmd(configDir, email string) *exec.Cmd {
	args := []string{"auth", "login"}
	if email != "" {
		args = append(args, "--email", email)
	}
	cmd := exec.Command(c.Bin, args...)
	cmd.Env = env(configDir)
	return cmd
}

// Status runs `claude auth status --json` against configDir ("" = live login).
func (c CLI) Status(ctx context.Context, configDir string) (Status, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Bin, "auth", "status", "--json")
	cmd.Env = env(configDir)
	out, err := cmd.Output()
	var st Status
	if jsonErr := json.Unmarshal(out, &st); jsonErr == nil {
		return st, nil // `auth status` may exit non-zero when logged out but still print JSON
	}
	if err != nil {
		return Status{}, fmt.Errorf("claude auth status: %w", err)
	}
	return Status{}, fmt.Errorf("claude auth status: unexpected output %q", truncate(string(out), 120))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
