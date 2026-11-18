package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// WriteDefaultIfMissing writes DefaultTOML() to path when no file exists
// there, and reports whether it did. An existing file is never opened for
// writing. The content goes to a temp file first and is hard-linked into
// place, so a half-written config can't appear and, when two processes
// race, exactly one of them creates the file.
func WriteDefaultIfMissing(path string) (created bool, err error) {
	if _, err := os.Lstat(path); err == nil {
		return false, nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("create config directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return false, fmt.Errorf("create temp config: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(DefaultTOML()); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("write temp config: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return false, fmt.Errorf("chmod temp config: %w", err)
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("create config: %w", err)
	}
	return true, nil
}
