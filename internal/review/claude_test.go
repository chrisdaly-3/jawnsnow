package review

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeClaude writes a shell script that answers `--version` and
// `auth status` with the given outputs.
func fakeClaudeCLI(t *testing.T, version, auth string) string {
	t.Helper()
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  --version) printf '%s\\n' '" + version + "' ;;\n" +
		"  auth) printf '%s\\n' '" + auth + "' ;;\n" +
		"  *) exit 2 ;;\n" +
		"esac\n"
	p := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDetectClaude(t *testing.T) {
	t.Parallel()
	bin := fakeClaudeCLI(t, "2.1.327 (Claude Code)", `{"loggedIn": true, "authMethod": "claude.ai", "extra": 1}`)
	got := DetectClaude(context.Background(), bin)
	want := ClaudeInfo{Found: true, Path: bin, Version: "2.1.327", LoggedIn: true, AuthMethod: "claude.ai"}
	if got != want {
		t.Fatalf("DetectClaude = %+v, want %+v", got, want)
	}
	if r := got.Unusable(); r != "" {
		t.Errorf("Unusable = %q, want empty", r)
	}
}

func TestDetectClaudeLoggedOut(t *testing.T) {
	t.Parallel()
	bin := fakeClaudeCLI(t, "2.1.327 (Claude Code)", `{"loggedIn": false}`)
	got := DetectClaude(context.Background(), bin)
	if !got.Found || got.LoggedIn || got.Version != "2.1.327" {
		t.Fatalf("DetectClaude = %+v, want found, logged out", got)
	}
	if want := `claude is not logged in — run "claude auth login"`; got.Unusable() != want {
		t.Errorf("Unusable = %q, want %q", got.Unusable(), want)
	}
}

func TestDetectClaudeBadAuthJSON(t *testing.T) {
	t.Parallel()
	bin := fakeClaudeCLI(t, "2.1.327 (Claude Code)", `not json`)
	got := DetectClaude(context.Background(), bin)
	if !got.Found || got.LoggedIn || got.Err == "" {
		t.Fatalf("DetectClaude = %+v, want found, logged out, Err set", got)
	}
	if r := got.Unusable(); !strings.HasPrefix(r, "claude is not working: ") || strings.Contains(r, "not logged in") {
		t.Errorf("Unusable = %q, want a claude is not working reason", r)
	}
}

func TestDetectClaudeMissing(t *testing.T) {
	t.Parallel()
	got := DetectClaude(context.Background(), filepath.Join(t.TempDir(), "nope"))
	if got.Found || got.LoggedIn || got.Err == "" {
		t.Fatalf("DetectClaude = %+v, want not found with Err set", got)
	}
	if want := "claude not found on PATH"; got.Unusable() != want {
		t.Errorf("Unusable = %q, want %q", got.Unusable(), want)
	}
}

func TestDetectClaudeTimeout(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexec sleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	got := DetectClaude(ctx, p)
	if time.Since(start) > 3*time.Second {
		t.Fatal("DetectClaude ignored the context timeout")
	}
	if got.LoggedIn {
		t.Errorf("DetectClaude = %+v, want not logged in", got)
	}
}

func TestDetectClaudeForkingWrapperTimesOut(t *testing.T) {
	old := detectTimeout
	detectTimeout = 500 * time.Millisecond
	t.Cleanup(func() { detectTimeout = old })
	p := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nsleep 30 &\nwait\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got := DetectClaude(context.Background(), p)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("DetectClaude took %v with a forking wrapper", d)
	}
	if got.Err == "" || got.LoggedIn {
		t.Errorf("DetectClaude = %+v, want Err set and not logged in", got)
	}
}

func TestDetectClaudeTruncatesHugeOutput(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\ncase \"$1\" in\n  --version) echo '2.1.327 (Claude Code)' ;;\n  auth) echo '{\"loggedIn\": true}'; head -c 200000 /dev/zero | tr '\\0' x ;;\nesac\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	got := DetectClaude(context.Background(), p)
	if !got.Found || got.Version != "2.1.327" {
		t.Fatalf("DetectClaude = %+v, want found with version", got)
	}
	// The JSON is followed by junk, so parsing fails, but not by blocking or
	// a pipe error: the output was read and capped.
	if strings.Contains(got.Err, "broken pipe") || strings.Contains(got.Err, "signal") {
		t.Errorf("Err = %q, want output truncated without a process error", got.Err)
	}
	var w cappedWriter
	w.max = 4
	if n, err := w.Write([]byte("abcdefgh")); n != 8 || err != nil || string(w.buf) != "abcd" {
		t.Errorf("cappedWriter kept %q (n=%d err=%v), want abcd", w.buf, n, err)
	}
}
