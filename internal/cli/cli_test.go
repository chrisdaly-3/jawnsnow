package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/github"
)

// isolate points HOME and XDG_DATA_HOME at a temp dir so startup never
// touches the real config or database.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	return home
}

func noClient() (github.Client, error) { return nil, nil }

type fakeClient struct {
	viewer string
	err    error
}

func (f fakeClient) Viewer(context.Context) (string, error) { return f.viewer, f.err }
func (fakeClient) Search(context.Context, string) ([]github.Fingerprint, github.RateLimit, error) {
	return nil, github.RateLimit{}, nil
}
func (fakeClient) Fingerprints(context.Context, []string) ([]github.Fingerprint, github.RateLimit, error) {
	return nil, github.RateLimit{}, nil
}
func (fakeClient) Details(context.Context, []string) ([]github.Snapshot, github.RateLimit, error) {
	return nil, github.RateLimit{}, nil
}

func hasBanner(banners []string, sub string) bool {
	for _, b := range banners {
		if strings.Contains(b, sub) {
			return true
		}
	}
	return false
}

func writeConfig(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "jawnsnow")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStartupMissingConfigUsesDefaults(t *testing.T) {
	isolate(t)
	deps, cleanup, err := buildDeps(os.Getenv, noClient)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if deps.Config.Mode.Default != config.Defaults().Mode.Default || len(deps.Config.Feeds) != len(config.Defaults().Feeds) {
		t.Fatalf("config is not the defaults: %+v", deps.Config)
	}
	if len(deps.Banners) == 0 || !strings.Contains(deps.Banners[0], "No config at") {
		t.Fatalf("Banners[0] should say there is no config, got %q", deps.Banners)
	}
	if !hasBanner(deps.Banners, `gh is not logged in — run "gh auth login"; polling is off`) {
		t.Fatalf("missing gh banner in %q", deps.Banners)
	}
	if deps.Client != nil || deps.Reloads != nil || deps.Store == nil || deps.Now == nil {
		t.Fatalf("unexpected deps: client=%v reloads=%v store=%v", deps.Client, deps.Reloads, deps.Store)
	}
}

func TestStartupWithConfigWatchesAndFindsViewer(t *testing.T) {
	home := isolate(t)
	writeConfig(t, home, "")
	deps, cleanup, err := buildDeps(os.Getenv, func() (github.Client, error) { return fakeClient{viewer: "octo"}, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if deps.Viewer != "octo" || deps.Client == nil || deps.Reloads == nil {
		t.Fatalf("viewer=%q client=%v reloads=%v", deps.Viewer, deps.Client, deps.Reloads)
	}
	if hasBanner(deps.Banners, "No config at") {
		t.Fatalf("unexpected banners %q", deps.Banners)
	}
}

func TestStartupViewerErrorTurnsPollingOff(t *testing.T) {
	isolate(t)
	deps, cleanup, err := buildDeps(os.Getenv, func() (github.Client, error) {
		return fakeClient{err: context.DeadlineExceeded}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if deps.Client != nil || !hasBanner(deps.Banners, "polling is off") {
		t.Fatalf("client=%v banners=%q", deps.Client, deps.Banners)
	}
}

func TestStartupInvalidConfigExits1(t *testing.T) {
	home := isolate(t)
	writeConfig(t, home, "[github]\npoll_interval = \"10s\"\n")
	var out, errb bytes.Buffer
	if code := Run(nil, &out, &errb); code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr %q", code, errb.String())
	}
	if !strings.Contains(errb.String(), "poll_interval") {
		t.Fatalf("stderr should name poll_interval, got %q", errb.String())
	}
}

func TestHelpPrintsUsageToStdout(t *testing.T) {
	for _, a := range []string{"help", "-h", "--help"} {
		var out, errb bytes.Buffer
		if code := Run([]string{a}, &out, &errb); code != 0 || !strings.Contains(out.String(), "usage: snow") {
			t.Fatalf("%s: code=%d out=%q", a, code, out.String())
		}
	}
}

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
