// Command snowdemo runs snow's interface over the fake data in
// internal/demo, to record the README's demo with vhs
// (docs/demo/demo.tape). It is a development tool and not part of snow.
//
// It reads no config file and opens no real store: it works in a temporary
// directory, set as HOME and XDG_DATA_HOME, that it removes on exit. It
// never runs gh or claude and makes no network call.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/chrisdaly-3/jawnsnow/internal/demo"
	"github.com/chrisdaly-3/jawnsnow/internal/review"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/theme"
	"github.com/chrisdaly-3/jawnsnow/internal/tui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "snowdemo: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	tmp, err := os.MkdirTemp("", "snowdemo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	// Anything that looks for the user's files finds the temporary ones.
	for _, k := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME"} {
		if err := os.Setenv(k, tmp); err != nil {
			return err
		}
	}

	cfg := demo.Config()
	th, err := theme.New(cfg.Theme)
	if err != nil {
		return err
	}
	dataDir := filepath.Join(tmp, "jawnsnow")
	st, err := store.Open(filepath.Join(dataDir, "jawnsnow.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	now := time.Now()
	if err := demo.Seed(context.Background(), st, now); err != nil {
		return err
	}

	deps := tui.Deps{
		Config: cfg, Theme: th, Store: st, Now: time.Now,
		Client:   demo.NewClient(now),
		Poster:   demo.NewPoster(),
		Reviewer: &demo.Reviewer{Store: st},
		Claude:   review.ClaudeInfo{Found: true, LoggedIn: true, Version: "demo", AuthMethod: "claude.ai"},
		DataDir:  dataDir,
	}
	_, err = tea.NewProgram(tui.New(deps)).Run()
	return err
}
