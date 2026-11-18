package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
)

func TestUsageNamesSnow(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run([]string{"bogus"}, &out, &errb); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	s := errb.String()
	if !strings.Contains(s, "usage: snow") || strings.Contains(s, "jawnsnow ") {
		t.Fatalf("usage text should name snow, got %q", s)
	}
}

func TestCLIConfigDefault(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run([]string{"config", "default"}, &out, &errb); code != 0 {
		t.Fatalf("exit code = %d, stderr %q", code, errb.String())
	}
	if !bytes.Equal(out.Bytes(), config.DefaultTOML()) {
		t.Fatal("stdout differs from DefaultTOML()")
	}
}
