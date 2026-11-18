package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	calls  *int
}

func (f fakeClient) Viewer(context.Context) (string, error) {
	if f.calls != nil {
		*f.calls++
	}
	return f.viewer, f.err
}
func (fakeClient) Search(context.Context, string) ([]github.Fingerprint, github.RateLimit, error) {
	return nil, github.RateLimit{}, nil
}
func (fakeClient) Fingerprints(context.Context, []string) ([]github.Fingerprint, []github.SkippedPR, github.RateLimit, error) {
	return nil, nil, github.RateLimit{}, nil
}
func (fakeClient) Details(context.Context, string, []string) ([]github.Snapshot, []github.SkippedPR, github.RateLimit, error) {
	return nil, nil, github.RateLimit{}, nil
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

func TestStartupCreatesDefaultConfig(t *testing.T) {
	home := isolate(t)
	deps, cleanup, err := buildDeps(os.Getenv, noClient)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	got, err := os.ReadFile(filepath.Join(home, ".config", "jawnsnow", "config.toml"))
	if err != nil {
		t.Fatalf("config was not created: %v", err)
	}
	if !bytes.Equal(got, config.DefaultTOML()) {
		t.Errorf("created file is not the default config")
	}
	// The TUI clears this banner once a config file reloads.
	if !strings.HasPrefix(deps.ConfigBanner, "Created ~/.config/jawnsnow/config.toml") || hasBanner(deps.Banners, "Created") {
		t.Fatalf("ConfigBanner = %q (banners %q)", deps.ConfigBanner, deps.Banners)
	}
	if deps.Config.Mode.Default != config.Defaults().Mode.Default || len(deps.Config.Feeds) != len(config.Defaults().Feeds) {
		t.Fatalf("config is not the defaults: %+v", deps.Config)
	}
	if !hasBanner(deps.Banners, `gh is not logged in — run "gh auth login"; polling is off`) {
		t.Fatalf("missing gh banner in %q", deps.Banners)
	}
	if deps.Client != nil || deps.Reloads == nil || deps.Store == nil || deps.Now == nil {
		t.Fatalf("unexpected deps: client=%v reloads=%v store=%v", deps.Client, deps.Reloads, deps.Store)
	}
}

func TestStartupKeepsExistingConfig(t *testing.T) {
	home := isolate(t)
	body := "[github]\npoll_interval = \"2m\"\n"
	writeConfig(t, home, body)
	deps, cleanup, err := buildDeps(os.Getenv, noClient)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	got, _ := os.ReadFile(filepath.Join(home, ".config", "jawnsnow", "config.toml"))
	if string(got) != body {
		t.Errorf("existing config was rewritten: %q", got)
	}
	if deps.ConfigBanner != "" || hasBanner(deps.Banners, "Created") {
		t.Errorf("unexpected banners %q %q", deps.ConfigBanner, deps.Banners)
	}
	if deps.Config.GitHub.PollInterval.Duration != 2*time.Minute {
		t.Errorf("poll interval = %v, want 2m", deps.Config.GitHub.PollInterval)
	}
}

func TestStartupCreateFailureFallsBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	home := isolate(t)
	cfgParent := filepath.Join(home, ".config")
	if err := os.Mkdir(cfgParent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfgParent, 0o755) })
	deps, cleanup, err := buildDeps(os.Getenv, noClient)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !strings.Contains(deps.ConfigBanner, "could not create it") || !strings.Contains(deps.ConfigBanner, "No config at ~/.config/jawnsnow/config.toml") {
		t.Fatalf("ConfigBanner = %q", deps.ConfigBanner)
	}
	if deps.Config.Mode.Default != config.Defaults().Mode.Default {
		t.Fatalf("config is not the defaults")
	}
}

func TestStartupWithConfigWatches(t *testing.T) {
	home := isolate(t)
	writeConfig(t, home, "")
	deps, cleanup, err := buildDeps(os.Getenv, func() (github.Client, error) { return fakeClient{viewer: "octo"}, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if deps.Client == nil || deps.Reloads == nil {
		t.Fatalf("client=%v reloads=%v", deps.Client, deps.Reloads)
	}
	if deps.ConfigBanner != "" || hasBanner(deps.Banners, "No config at") {
		t.Fatalf("unexpected banners %q %q", deps.ConfigBanner, deps.Banners)
	}
}

// The TUI looks the viewer up itself and retries, so a network blip at
// launch neither blocks the first frame nor turns polling off.
func TestStartupLeavesTheViewerLookupToTheTUI(t *testing.T) {
	isolate(t)
	var calls int
	deps, cleanup, err := buildDeps(os.Getenv, func() (github.Client, error) {
		return fakeClient{err: context.DeadlineExceeded, calls: &calls}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if calls != 0 {
		t.Errorf("startup called Viewer %d times", calls)
	}
	if deps.Client == nil || hasBanner(deps.Banners, "polling is off") {
		t.Fatalf("client=%v banners=%q, want the client kept", deps.Client, deps.Banners)
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

func TestUsageExplainsBareSnowAndHelp(t *testing.T) {
	var out, errb bytes.Buffer
	Run([]string{"help"}, &out, &errb)
	s := out.String()
	for _, want := range []string{"With no command, snow starts the interface", "  help ", "  config default ", "  version "} {
		if !strings.Contains(s, want) {
			t.Errorf("usage missing %q:\n%s", want, s)
		}
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
