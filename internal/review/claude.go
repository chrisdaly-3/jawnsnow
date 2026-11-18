package review

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	// detectWaitDelay bounds how long Wait lingers on output pipes after the
	// process is killed, which a wrapper's forked child would otherwise hold.
	detectWaitDelay = time.Second
	// detectMaxOutput caps what detection keeps of a command's stdout.
	detectMaxOutput = 64 << 10
)

// detectTimeout bounds all of DetectClaude; tests shorten it.
var detectTimeout = 10 * time.Second

// cappedWriter keeps the first max bytes written and drops the rest without
// failing the writer.
type cappedWriter struct {
	buf []byte
	max int
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if room := w.max - len(w.buf); room > 0 {
		w.buf = append(w.buf, p[:min(len(p), room)]...)
	}
	return len(p), nil
}

// runCapped runs path with args under ctx and returns up to detectMaxOutput
// bytes of stdout.
func runCapped(ctx context.Context, path string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = detectWaitDelay
	out := &cappedWriter{max: detectMaxOutput}
	cmd.Stdout = out
	err := cmd.Run()
	return out.buf, err
}

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
	ctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()
	path, err := exec.LookPath(binary)
	if err != nil {
		return ClaudeInfo{Err: err.Error()}
	}
	info := ClaudeInfo{Found: true, Path: path}

	out, err := runCapped(ctx, path, "--version")
	if err != nil {
		info.Err = fmt.Sprintf("claude --version: %v", err)
		return info
	}
	// "2.1.327 (Claude Code)"
	info.Version, _, _ = strings.Cut(strings.TrimSpace(string(out)), " ")

	out, err = runCapped(ctx, path, "auth", "status")
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
	case c.Err != "":
		return "claude is not working: " + c.Err
	case !c.LoggedIn:
		return `claude is not logged in — run "claude auth login"`
	}
	return ""
}
