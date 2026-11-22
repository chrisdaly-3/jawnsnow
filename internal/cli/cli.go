// Package cli implements the snow command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/cli/go-gh/v2/pkg/auth"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/review"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/theme"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
	"github.com/chrisdaly-3/jawnsnow/internal/tui"
	"github.com/chrisdaly-3/jawnsnow/internal/workspace"
)

const (
	// claudeProbeTimeout bounds startup detection of the claude CLI.
	claudeProbeTimeout = 5 * time.Second
	// shutdownWait bounds how long quitting waits for reviews to save.
	shutdownWait = 10 * time.Second
)

// sweepBannerWait is how long startup waits for the worktree cleanup before
// moving on; its error shows as a banner only when it finishes in that time.
var sweepBannerWait = 2 * time.Second

const usage = `usage: snow [command]

With no command, snow starts the interface.

commands:
  config default  print the commented default config
  config path     print the config file snow reads (or would create)

  version         print the version
  help            print this help

The config file is $XDG_CONFIG_HOME/jawnsnow/config.toml when it exists (the
variable must be an absolute path), else $HOME/.config/jawnsnow/config.toml.
snow creates the first of those that applies when neither exists.
`

// Run executes the snow command with args (excluding the program name) and
// returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return runTUI(stderr)
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "version":
		fmt.Fprintln(stdout, "snow dev")
		return 0
	case "config":
		if len(args) == 2 && args[1] == "default" {
			if _, err := stdout.Write(config.DefaultTOML()); err != nil {
				fmt.Fprintf(stderr, "snow: write default config: %v\n", err)
				return 1
			}
			return 0
		}
		if len(args) == 2 && args[1] == "path" {
			// stdout is only the path, so "$(snow config path)" works.
			path, _, ignoredXDG := config.Resolve(os.Getenv, fileExists)
			if ignoredXDG != "" {
				fmt.Fprintf(stderr, "snow: %s\n", ignoredXDGNote(ignoredXDG))
			}
			fmt.Fprintln(stdout, path)
			return 0
		}
		fmt.Fprint(stderr, usage)
		return 2
	default:
		fmt.Fprintf(stderr, "snow: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

// runTUI starts the interface with the real config, store and gh login.
func runTUI(stderr io.Writer) int {
	deps, cleanup, err := buildDeps(os.Getenv, newGHClient)
	if err != nil {
		fmt.Fprintf(stderr, "snow: %v\n", err)
		return 1
	}
	defer cleanup()
	if _, err := tea.NewProgram(tui.New(deps)).Run(); err != nil {
		fmt.Fprintf(stderr, "snow: %v\n", err)
		return 1
	}
	return 0
}

// fileExists is config.Resolve's test for whether a config file is there.
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ignoredXDGNote says that a relative XDG_CONFIG_HOME was skipped, as the
// XDG spec requires.
func ignoredXDGNote(value string) string {
	return fmt.Sprintf("XDG_CONFIG_HOME is not an absolute path (%q), so it was ignored", value)
}

// newGHClient returns a nil client when gh has no token for github.com.
func newGHClient() (github.Client, error) {
	if token, _ := auth.TokenForHost("github.com"); token == "" {
		return nil, nil
	}
	return github.NewGHClient()
}

// newPoster builds the GitHub poster; tests replace it so they never build
// the real one.
var newPoster = github.NewGHPoster

// buildDeps assembles the TUI's dependencies. The returned cleanup closes
// the config watcher, then the store, then releases the data dir lock; it
// is safe to call even when err is non-nil (it is then a no-op).
func buildDeps(getenv func(string) string, newClient func() (github.Client, error)) (tui.Deps, func(), error) {
	noop := func() {}
	var banners []string
	var configBanner string

	path, _, ignoredXDG := config.Resolve(getenv, fileExists)
	if ignoredXDG != "" {
		banners = append(banners, "config: "+ignoredXDGNote(ignoredXDG))
	}
	shown := tildePath(path, getenv("HOME"))
	created, createErr := config.WriteDefaultIfMissing(path)
	switch {
	case createErr != nil:
		configBanner = fmt.Sprintf("No config at %s — using defaults (could not create it: %v). Run \"snow config default\" to start one.", shown, createErr)
	case created:
		configBanner = fmt.Sprintf("Created %s with the defaults — edit it any time; snow reloads on save.", shown)
	}
	cfg, warnings, err := config.Load(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// Creating the file failed above, and its banner says so.
	case err != nil:
		return tui.Deps{}, noop, err
	default:
		if err := cfg.Validate(); err != nil {
			return tui.Deps{}, noop, err
		}
		for _, w := range warnings {
			banners = append(banners, "config: "+w)
		}
	}

	th, err := theme.New(cfg.Theme)
	if err != nil {
		return tui.Deps{}, noop, err
	}

	st, err := store.Open(store.DataPath())
	if err != nil {
		return tui.Deps{}, noop, err
	}

	deps := tui.Deps{Config: cfg, Theme: th, Store: st, ConfigBanner: configBanner, ConfigPath: path, Now: time.Now}
	var watcher *config.Watcher
	cur := &currentConfig{cfg: cfg}

	// The TUI looks up the gh user itself, so a failed lookup is retried
	// rather than turning polling off for the session.
	client, err := newClient()
	switch {
	case err != nil:
		deps.LoginWarning = fmt.Sprintf(`gh client failed: %v — snow can't poll GitHub or run reviews. Run "gh auth login", then restart snow.`, err)
	case client == nil:
		deps.LoginWarning = `No gh login found — snow can't poll GitHub or run reviews. Run "gh auth login", then restart snow.`
	default:
		deps.Client = client
	}

	// The poster is the tool's only GitHub write; it needs the same gh
	// login as the client.
	switch deps.Client {
	case nil:
		deps.PostOff = "gh is not logged in"
	default:
		if p, err := newPoster(); err != nil {
			deps.PostOff = fmt.Sprintf("could not set up posting: %v", err)
		} else {
			deps.Poster = p
		}
	}

	// A missing config directory means creating the file failed, which the
	// banner above already explains, so it is not an error.
	stopTee := func() {}
	if w, err := config.NewWatcher(path); err == nil {
		watcher = w
		deps.Reloads, stopTee = cur.follow(w.Events())
	} else if !errors.Is(err, os.ErrNotExist) {
		banners = append(banners, fmt.Sprintf("config reload is off: %v", err))
	}

	dataDir := filepath.Dir(store.DataPath())
	deps.DataDir = dataDir
	unlockData, lockOff, lockHeld := lockDataDir(dataDir)
	// The post flow's double-post guard reads this process's memory as
	// well as the store, so only the snow holding the lock may post.
	if lockHeld && deps.Poster != nil {
		deps.Poster, deps.PostOff = nil, "another snow is running on this machine — post from that window"
	}
	var engine *review.Engine
	deps.Claude, deps.ReviewsOff = detectClaude()
	switch {
	case lockOff != "":
		deps.ReviewsOff = lockOff
	case deps.ReviewsOff != "":
	case deps.Client == nil:
		deps.ReviewsOff = `reviews need a gh login — run "gh auth login"`
	default:
		engine = &review.Engine{
			Config: cur.get, Client: deps.Client, Store: st,
			DataDir: dataDir, Claude: deps.Claude.Path, Viewer: viewerOf(deps.Client),
		}
	}
	switch {
	case deps.ReviewsOff == "":
	case deps.LoginWarning != "":
		// The warning already says it; the Agent view and space still show
		// ReviewsOff.
	case cfg.Mode.Default == "auto":
		// The TUI starts in watcher mode instead.
		banners = append(banners, "auto mode unavailable — reviews off: "+deps.ReviewsOff)
	default:
		banners = append(banners, "reviews off: "+deps.ReviewsOff)
	}

	// The sweep frees every stored slot, so it is only safe for the snow
	// holding the lock.
	sweepCtx, stopSweep := context.WithCancel(context.Background())
	sweepDone := make(chan struct{})
	var sweepErr error
	if lockOff == "" {
		go func() {
			defer close(sweepDone)
			sweepErr = sweepStartup(sweepCtx, st, cur.get, dataDir)
		}()
	} else {
		close(sweepDone)
	}
	if engine != nil {
		deps.Reviewer = &sweepGated{Engine: engine, ready: sweepDone}
	}
	select {
	case <-sweepDone:
		if sweepErr != nil {
			banners = append(banners, "worktree cleanup: "+sweepErr.Error())
		}
	case <-time.After(sweepBannerWait):
	}

	cleanup := func() {
		var w waiter
		if engine != nil {
			w = engine
		}
		shutdown(w, shutdownWait,
			func() { stopSweep(); <-sweepDone },
			stopTee,
			func() {
				if watcher != nil {
					_ = watcher.Close()
				}
			},
			func() { _ = st.Close() },
			unlockData)
	}

	deps.Banners = banners
	return deps, cleanup, nil
}

// lockDataDir takes the lock that lets one snow per data dir run reviews:
// two would free each other's busy worktree slots. It returns the unlock
// and, when the lock is not this process's, why reviews are off; held says
// another snow has it. The lock is an flock, so it goes when the process
// does, however it exits.
func lockDataDir(dir string) (unlock func(), off string, held bool) {
	noop := func() {}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return noop, fmt.Sprintf("can't lock the data directory: %v", err), false
	}
	f, err := os.OpenFile(filepath.Join(dir, "jawnsnow.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return noop, fmt.Sprintf("can't lock the data directory: %v", err), false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return noop, "another snow is running reviews on this machine", true
		}
		return noop, fmt.Sprintf("can't lock the data directory: %v", err), false
	}
	return func() { _ = f.Close() }, "", false
}

// detectClaude finds claude on PATH and asks whether it is usable. A missing
// claude still yields a non-zero ClaudeInfo, since the zero value tells the
// TUI that detection didn't run.
func detectClaude() (review.ClaudeInfo, string) {
	path, err := exec.LookPath("claude")
	if err != nil {
		info := review.ClaudeInfo{Err: "claude not found on PATH"}
		return info, info.Unusable()
	}
	ctx, cancel := context.WithTimeout(context.Background(), claudeProbeTimeout)
	defer cancel()
	info := review.DetectClaude(ctx, path)
	return info, info.Unusable()
}

// viewerOf looks the gh user up on first use and keeps it. A failed lookup
// is retried on the next call.
func viewerOf(c github.Client) func() string {
	var mu sync.Mutex
	var login string
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		if login == "" {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			login, _ = c.Viewer(ctx)
		}
		return login
	}
}

// currentConfig is the config the review engine reads. The TUI gets its own
// copy of each reload; follow keeps this one in step.
type currentConfig struct {
	mu  sync.Mutex
	cfg config.Config
}

func (c *currentConfig) get() config.Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

// follow records each good reload from in and passes every reload on. The
// returned stop ends the forwarding.
func (c *currentConfig) follow(in <-chan config.Reload) (<-chan config.Reload, func()) {
	out := make(chan config.Reload)
	done := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(out)
		for {
			select {
			case r, ok := <-in:
				if !ok {
					return
				}
				if r.Err == nil {
					c.mu.Lock()
					c.cfg = r.Config
					c.mu.Unlock()
				}
				select {
				case out <- r:
				case <-done:
					return
				}
			case <-done:
				return
			}
		}
	}()
	return out, func() { once.Do(func() { close(done) }) }
}

// sweepGated holds reviews back until the startup sweep is done, since the
// sweep frees every stored worktree slot.
type sweepGated struct {
	*review.Engine
	ready <-chan struct{}
}

func (g *sweepGated) Run(ctx context.Context, req review.Request, onEvent func(review.Event)) (store.Review, error) {
	select {
	case <-g.ready:
	case <-ctx.Done():
		return store.Review{}, ctx.Err()
	}
	return g.Engine.Run(ctx, req, onEvent)
}

type waiter interface {
	Wait(ctx context.Context) error
}

// shutdown waits up to d for in-flight reviews to save, then runs closers in
// order. The store must close last, after every review has saved.
func shutdown(w waiter, d time.Duration, closers ...func()) {
	if w != nil {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		_ = w.Wait(ctx)
		cancel()
	}
	for _, c := range closers {
		c()
	}
}

// sweepStartup clears what an earlier run left behind in each repo that has
// a PR on the wall: its idle worktree slots, and its refs for PRs no longer
// tracked. A repo with no mapped path and no clone from an earlier run has
// nothing of ours in it, so it is skipped rather than cloned; one whose
// mapped path is not a repository is skipped with an error.
func sweepStartup(ctx context.Context, st *store.Store, cfg func() config.Config, dataDir string) error {
	all, err := st.LoadTracked(ctx)
	if err != nil {
		return err
	}
	byRepo := map[string]map[int]bool{}
	for _, t := range all {
		if t.Place != tracker.PlaceWall {
			continue
		}
		if byRepo[t.PR.Repo] == nil {
			byRepo[t.PR.Repo] = map[int]bool{}
		}
		byRepo[t.PR.Repo][t.PR.Number] = true
	}
	data, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(data); err == nil {
		data = real
	}
	// Resolve would clone a repo it can't find; there is nothing to clone for.
	noClone := func(string) string { return "" }

	var errs []error
	for _, repo := range slices.Sorted(maps.Keys(byRepo)) {
		rc := cfg().RepoFor(repo)
		if _, err := os.Stat(workspace.ClonePath(data, repo)); rc.Path == "" && err != nil {
			continue
		}
		r, err := workspace.Resolve(ctx, repo, rc, data, noClone)
		if err != nil {
			errs = append(errs, err) // a mapped path that isn't a repo
			continue
		}
		if err := workspace.Sweep(ctx, r, workspace.PoolDir(data, repo), byRepo[repo], st); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", repo, err))
		}
	}
	return errors.Join(errs...)
}

// tildePath shortens a path under home to ~/....
func tildePath(path, home string) string {
	if home != "" && strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}
