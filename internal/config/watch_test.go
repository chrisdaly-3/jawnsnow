package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func nextReload(t *testing.T, w *Watcher) Reload {
	t.Helper()
	select {
	case r := <-w.Events():
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("no reload within 2s")
		return Reload{}
	}
}

func TestWatcherReloadsAndReportsErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	w, err := NewWatcher(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	if err := os.WriteFile(path, []byte("[github]\npoll_interval = \"2m\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := nextReload(t, w)
	if r.Err != nil {
		t.Fatalf("Err = %v", r.Err)
	}
	if got := r.Config.GitHub.PollInterval.Duration; got != 2*time.Minute {
		t.Errorf("PollInterval = %v, want 2m", got)
	}

	if err := os.WriteFile(path, []byte("[[["), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := nextReload(t, w); r.Err == nil {
		t.Error("Err = nil for invalid TOML")
	}
}
