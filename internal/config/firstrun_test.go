package config

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func noTempFiles(t *testing.T, dir string) {
	t.Helper()
	left, err := filepath.Glob(filepath.Join(dir, ".config-*.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("leftover temp files: %v", left)
	}
}

func TestWriteDefaultIfMissingCreates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	path := filepath.Join(dir, "config.toml")
	created, err := WriteDefaultIfMissing(path)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v, want true, nil", created, err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, DefaultTOML()) {
		t.Errorf("file does not match DefaultTOML()")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}
	noTempFiles(t, dir)
}

func TestWriteDefaultIfMissingNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("x = 1"), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err := WriteDefaultIfMissing(path)
	if err != nil || created {
		t.Fatalf("created=%v err=%v, want false, nil", created, err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "x = 1" {
		t.Errorf("content changed to %q", got)
	}
	noTempFiles(t, dir)
}

func TestWriteDefaultIfMissingConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	const n = 8
	results := make([]bool, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = WriteDefaultIfMissing(path)
		}()
	}
	wg.Wait()
	creators := 0
	for i := range n {
		if errs[i] != nil {
			t.Errorf("call %d: %v", i, errs[i])
		}
		if results[i] {
			creators++
		}
	}
	if creators != 1 {
		t.Errorf("%d calls reported creating the file, want 1", creators)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, DefaultTOML()) {
		t.Errorf("final file does not match DefaultTOML()")
	}
	noTempFiles(t, dir)
}

func TestWriteDefaultIfMissingUnwritableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	path := filepath.Join(dir, "config.toml")
	created, err := WriteDefaultIfMissing(path)
	if err == nil || created {
		t.Fatalf("created=%v err=%v, want false and an error", created, err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("config file exists after failure: %v", statErr)
	}
	noTempFiles(t, dir)
}
