package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
)

// isolate points HOME and XDG_DATA_HOME at a temp dir so startup never
// touches the real config or database.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	// An empty PATH keeps startup from finding, and running, the real claude.
	t.Setenv("PATH", t.TempDir())
	// Never build the real poster.
	old := newPoster
	newPoster = func() (github.Poster, error) { return fakePoster{}, nil }
	t.Cleanup(func() { newPoster = old })
	return home
}

type fakePoster struct{ github.Poster }

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
	if !strings.Contains(deps.LoginWarning, "No gh login found") {
		t.Fatalf("LoginWarning = %q", deps.LoginWarning)
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

func (fakeClient) ReviewContext(context.Context, string) (github.PRContext, github.RateLimit, error) {
	return github.PRContext{}, github.RateLimit{}, nil
}

// fakeClaude puts a claude on PATH that answers detection and nothing else.
func fakeClaude(t *testing.T, loggedIn bool) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  --version) echo '9.9.9 (Claude Code)' ;;\n" +
		"  auth) echo '{\"loggedIn\":" + map[bool]string{true: "true", false: "false"}[loggedIn] + ",\"authMethod\":\"test\"}' ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func withClient() (github.Client, error) { return fakeClient{viewer: "octo"}, nil }

func TestBuildDepsWiresReviewer(t *testing.T) {
	isolate(t)
	fakeClaude(t, true)
	deps, cleanup, err := buildDeps(os.Getenv, withClient)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if deps.Reviewer == nil || deps.ReviewsOff != "" {
		t.Fatalf("reviewer=%v off=%q", deps.Reviewer, deps.ReviewsOff)
	}
	if !deps.Claude.Found || !deps.Claude.LoggedIn || deps.Claude.Version != "9.9.9" {
		t.Fatalf("Claude = %+v", deps.Claude)
	}
}

func TestBuildDepsNoClaude(t *testing.T) {
	isolate(t)
	deps, cleanup, err := buildDeps(os.Getenv, withClient)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if deps.ReviewsOff != "claude not found on PATH" || deps.Reviewer != nil {
		t.Fatalf("off=%q reviewer=%v", deps.ReviewsOff, deps.Reviewer)
	}
	if !hasBanner(deps.Banners, "reviews off: claude not found on PATH") || hasBanner(deps.Banners, "auto mode") {
		t.Errorf("banners = %q, want \"reviews off: claude not found on PATH\"", deps.Banners)
	}
	// Never the zero value, which the TUI reads as "detection didn't run".
	if deps.Claude.Err != "claude not found on PATH" {
		t.Fatalf("Claude = %+v", deps.Claude)
	}
}

func TestBuildDepsClaudeLoggedOut(t *testing.T) {
	isolate(t)
	fakeClaude(t, false)
	deps, cleanup, err := buildDeps(os.Getenv, withClient)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !strings.Contains(deps.ReviewsOff, "not logged in") || deps.Reviewer != nil {
		t.Fatalf("off=%q reviewer=%v", deps.ReviewsOff, deps.Reviewer)
	}
}

// The engine fetches PR context through the client, so with no gh login
// there is nothing to review with.
func TestBuildDepsNoGHTurnsReviewsOff(t *testing.T) {
	isolate(t)
	fakeClaude(t, true)
	deps, cleanup, err := buildDeps(os.Getenv, noClient)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if deps.Reviewer != nil || !strings.Contains(deps.ReviewsOff, "gh") {
		t.Fatalf("off=%q reviewer=%v", deps.ReviewsOff, deps.Reviewer)
	}
}

// Two snows on one data dir would free each other's busy worktree slots
// and race on review saves, so only the first runs reviews.
func TestSecondSnowRunsNoReviews(t *testing.T) {
	isolate(t)
	fakeClaude(t, true)
	first, cleanupFirst, err := buildDeps(os.Getenv, withClient)
	if err != nil {
		t.Fatal(err)
	}
	if first.Reviewer == nil || first.ReviewsOff != "" {
		t.Fatalf("first: reviewer=%v off=%q", first.Reviewer, first.ReviewsOff)
	}
	second, cleanupSecond, err := buildDeps(os.Getenv, withClient)
	if err != nil {
		cleanupFirst()
		t.Fatal(err)
	}
	if second.Reviewer != nil || second.ReviewsOff != "another snow is running reviews on this machine" {
		t.Errorf("second: reviewer=%v off=%q", second.Reviewer, second.ReviewsOff)
	}
	cleanupSecond()
	cleanupFirst()

	// The lock goes with the first snow's shutdown.
	third, cleanupThird, err := buildDeps(os.Getenv, withClient)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupThird()
	if third.Reviewer == nil || third.ReviewsOff != "" {
		t.Errorf("after the first quit: reviewer=%v off=%q", third.Reviewer, third.ReviewsOff)
	}
}

func TestAutoDefaultFallsBackWithoutClaude(t *testing.T) {
	home := isolate(t)
	writeConfig(t, home, "[mode]\ndefault = \"auto\"\n")
	deps, cleanup, err := buildDeps(os.Getenv, withClient)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	// buildDeps leaves the configured mode alone; the TUI starts in watcher
	// mode because there is no reviewer.
	if deps.Config.Mode.Default != "auto" || deps.Reviewer != nil || deps.ReviewsOff == "" {
		t.Fatalf("mode=%q reviewer=%v off=%q", deps.Config.Mode.Default, deps.Reviewer, deps.ReviewsOff)
	}
	if !hasBanner(deps.Banners, "auto mode unavailable — reviews off: claude not found on PATH") {
		t.Errorf("banners = %q, want the auto fallback banner", deps.Banners)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func wallPR(repo string, n int) tracker.Tracked {
	return tracker.Tracked{
		PR:    github.Snapshot{ID: "PR_" + repo + "_" + string(rune('a'+n)), Repo: repo, Number: n},
		Place: tracker.PlaceWall,
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "jawnsnow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSweepStartupDropsRefsOfUntrackedPRs(t *testing.T) {
	st := openStore(t)
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "x")
	gitIn(t, repo, "update-ref", "refs/jawnsnow/5/head", "HEAD")
	gitIn(t, repo, "update-ref", "refs/jawnsnow/99/head", "HEAD")
	cfg := config.Defaults()
	cfg.Repos = map[string]config.Repo{"o/n": {Path: repo, Scope: "full"}}
	// A PR off the wall doesn't count as tracked, and doesn't trigger a sweep.
	done := wallPR("o/other", 1)
	done.Place = tracker.PlaceDone
	if err := st.SaveTracked(context.Background(), []tracker.Tracked{wallPR("o/n", 5), done}); err != nil {
		t.Fatal(err)
	}

	data := filepath.Join(t.TempDir(), "jawnsnow")
	if err := sweepStartup(context.Background(), st, func() config.Config { return cfg }, data); err != nil {
		t.Fatal(err)
	}
	refs := gitIn(t, repo, "for-each-ref", "--format=%(refname)", "refs/jawnsnow/")
	if refs != "refs/jawnsnow/5/head" {
		t.Fatalf("refs after sweep = %q, want only PR 5's", refs)
	}
}

func TestSweepStartupSkipsUnmappedRepoWithoutClone(t *testing.T) {
	st := openStore(t)
	if err := st.SaveTracked(context.Background(), []tracker.Tracked{wallPR("o/n", 5)}); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(t.TempDir(), "jawnsnow")
	cfg := config.Defaults()
	if err := sweepStartup(context.Background(), st, func() config.Config { return cfg }, data); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(data); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sweep created %s (stat err %v); it must not clone or create anything", data, err)
	}
}

func TestSweepStartupSkipsBadMappedPath(t *testing.T) {
	st := openStore(t)
	if err := st.SaveTracked(context.Background(), []tracker.Tracked{wallPR("o/n", 5)}); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(t.TempDir(), "jawnsnow")
	bad := filepath.Join(t.TempDir(), "typo")
	cfg := config.Defaults()
	cfg.Repos = map[string]config.Repo{"o/n": {Path: bad, Scope: "full"}}
	err := sweepStartup(context.Background(), st, func() config.Config { return cfg }, data)
	if want := `repos."o/n".path ` + bad + " is not a git repository"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("sweep err = %v, want it to name %q", err, want)
	}
	if _, err := os.Stat(filepath.Join(data, "clones")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("sweep made %s/clones (stat err %v)", data, err)
	}
}

type fakeWaiter struct{ order *[]string }

func (f fakeWaiter) Wait(ctx context.Context) error {
	<-ctx.Done()
	*f.order = append(*f.order, "wait")
	return ctx.Err()
}

func TestShutdownWaitsThenClosesInOrder(t *testing.T) {
	var order []string
	start := time.Now()
	shutdown(fakeWaiter{&order}, 50*time.Millisecond,
		func() { order = append(order, "watcher") },
		func() { order = append(order, "store") })
	if strings.Join(order, ",") != "wait,watcher,store" {
		t.Fatalf("order = %v", order)
	}
	if d := time.Since(start); d < 50*time.Millisecond || d > 2*time.Second {
		t.Fatalf("shutdown took %v, want about the 50ms wait", d)
	}
}

func TestNoGHLoginWarns(t *testing.T) {
	isolate(t)
	deps, cleanup, err := buildDeps(os.Getenv, noClient)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, want := range []string{"can't poll GitHub", "gh auth login"} {
		if !strings.Contains(deps.LoginWarning, want) {
			t.Errorf("LoginWarning = %q, want it to contain %q", deps.LoginWarning, want)
		}
	}
	if hasBanner(deps.Banners, "polling is off") || hasBanner(deps.Banners, "reviews off") || hasBanner(deps.Banners, "auto mode unavailable") {
		t.Errorf("banners repeat the login warning: %q", deps.Banners)
	}
	if deps.ReviewsOff == "" {
		t.Error("ReviewsOff is empty, want a reason kept for the Agent view")
	}
}

func TestGHClientErrorWarns(t *testing.T) {
	isolate(t)
	deps, cleanup, err := buildDeps(os.Getenv, func() (github.Client, error) { return nil, errors.New("boom") })
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	want := `gh client failed: boom — snow can't poll GitHub or run reviews. Run "gh auth login", then restart snow.`
	if deps.LoginWarning != want {
		t.Errorf("LoginWarning = %q, want %q", deps.LoginWarning, want)
	}
	if hasBanner(deps.Banners, "polling is off") || hasBanner(deps.Banners, "reviews off") {
		t.Errorf("banners repeat the login warning: %q", deps.Banners)
	}
}

func TestNoGHLoginWarnsOnceInAutoMode(t *testing.T) {
	home := isolate(t)
	writeConfig(t, home, "[mode]\ndefault = \"auto\"\n")
	deps, cleanup, err := buildDeps(os.Getenv, noClient)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if deps.LoginWarning == "" {
		t.Fatal("no LoginWarning")
	}
	if hasBanner(deps.Banners, "auto mode unavailable") || hasBanner(deps.Banners, "reviews off") {
		t.Errorf("banners repeat the login warning: %q", deps.Banners)
	}
}

func TestBuildDepsWiresPoster(t *testing.T) {
	isolate(t)
	deps, cleanup, err := buildDeps(os.Getenv, withClient)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if deps.Poster == nil || deps.PostOff != "" {
		t.Fatalf("poster=%v off=%q", deps.Poster, deps.PostOff)
	}
}

func TestBuildDepsNoGhNoPoster(t *testing.T) {
	isolate(t)
	deps, cleanup, err := buildDeps(os.Getenv, noClient)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if deps.Poster != nil || deps.PostOff != "gh is not logged in" {
		t.Fatalf("poster=%v off=%q", deps.Poster, deps.PostOff)
	}
}

func TestBuildDepsPosterErrorTurnsPostingOff(t *testing.T) {
	isolate(t)
	newPoster = func() (github.Poster, error) { return nil, errors.New("boom") }
	deps, cleanup, err := buildDeps(os.Getenv, withClient)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if deps.Poster != nil || deps.PostOff != "could not set up posting: boom" {
		t.Fatalf("poster=%v off=%q", deps.Poster, deps.PostOff)
	}
}

// The post flow's temp files go in the data dir the review engine uses.
func TestBuildDepsSetsDataDir(t *testing.T) {
	isolate(t)
	deps, cleanup, err := buildDeps(os.Getenv, withClient)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if want := filepath.Dir(store.DataPath()); deps.DataDir != want || want == "" {
		t.Fatalf("DataDir = %q, want %q", deps.DataDir, want)
	}
}
