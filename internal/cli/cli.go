// Package cli implements the snow command line.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/cli/go-gh/v2/pkg/auth"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/theme"
	"github.com/chrisdaly-3/jawnsnow/internal/tui"
)

const usage = `usage: snow [command]

With no command, snow starts the interface.

commands:
  config default  print the commented default config
  version         print the version
  help            print this help
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

// newGHClient returns a nil client when gh has no token for github.com.
func newGHClient() (github.Client, error) {
	if token, _ := auth.TokenForHost("github.com"); token == "" {
		return nil, nil
	}
	return github.NewGHClient()
}

// buildDeps assembles the TUI's dependencies. The returned cleanup closes
// the config watcher and then the store; it is safe to call even when err
// is non-nil (it is then a no-op).
func buildDeps(getenv func(string) string, newClient func() (github.Client, error)) (tui.Deps, func(), error) {
	noop := func() {}
	var banners []string
	var configBanner string

	path := config.Path()
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

	deps := tui.Deps{Config: cfg, Theme: th, Store: st, ConfigBanner: configBanner, Now: time.Now}
	var watcher *config.Watcher
	cleanup := func() {
		if watcher != nil {
			_ = watcher.Close()
		}
		_ = st.Close()
	}

	// The TUI looks up the gh user itself, so a failed lookup is retried
	// rather than turning polling off for the session.
	client, err := newClient()
	switch {
	case err != nil:
		banners = append(banners, fmt.Sprintf("gh client: %v; polling is off", err))
	case client == nil:
		banners = append(banners, `gh is not logged in — run "gh auth login"; polling is off`)
	default:
		deps.Client = client
	}

	// A missing config directory means creating the file failed, which the
	// banner above already explains, so it is not an error.
	if w, err := config.NewWatcher(path); err == nil {
		watcher = w
		deps.Reloads = w.Events()
	} else if !errors.Is(err, os.ErrNotExist) {
		banners = append(banners, fmt.Sprintf("config reload is off: %v", err))
	}

	deps.Banners = banners
	return deps, cleanup, nil
}

// tildePath shortens a path under home to ~/....
func tildePath(path, home string) string {
	if home != "" && strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}
