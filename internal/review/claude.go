package review

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// ClaudeInfo is what detection learned about the claude CLI.
type ClaudeInfo struct {
	Found      bool
	Path       string
	Version    string
	LoggedIn   bool
	AuthMethod string
	// Err says why detection could not tell, or why it failed.
	Err string
}

// DetectClaude finds binary (default "claude") on PATH, reads its version
// and asks whether it is logged in. Both commands run under ctx, with no
// shell. A missing binary gives Found=false; auth output that isn't JSON
// gives LoggedIn=false with Err set.
func DetectClaude(ctx context.Context, binary string) ClaudeInfo {
	if binary == "" {
		binary = "claude"
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return ClaudeInfo{Err: err.Error()}
	}
	info := ClaudeInfo{Found: true, Path: path}

	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		info.Err = fmt.Sprintf("claude --version: %v", err)
		return info
	}
	// "2.1.327 (Claude Code)"
	info.Version, _, _ = strings.Cut(strings.TrimSpace(string(out)), " ")

	out, err = exec.CommandContext(ctx, path, "auth", "status").Output()
	if err != nil && len(out) == 0 {
		info.Err = fmt.Sprintf("claude auth status: %v", err)
		return info
	}
	var auth struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
	}
	if err := json.Unmarshal(out, &auth); err != nil {
		info.Err = fmt.Sprintf("claude auth status: unreadable output: %v", err)
		return info
	}
	info.LoggedIn, info.AuthMethod = auth.LoggedIn, auth.AuthMethod
	return info
}

// Unusable is why reviews can't run, or "" when claude is installed and
// logged in.
func (c ClaudeInfo) Unusable() string {
	switch {
	case !c.Found:
		return "claude not found on PATH"
	case !c.LoggedIn:
		return `claude is not logged in — run "claude auth login"`
	}
	return ""
}
