package config

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

const debounce = 200 * time.Millisecond

// Reload is the result of re-reading the config file after a change. When Err
// is set, Config is not safe to use: keep the previous one.
type Reload struct {
	Config   Config
	Warnings []string
	Err      error
}

// Watcher reloads the config file whenever it changes.
type Watcher struct {
	path   string
	fsw    *fsnotify.Watcher
	events chan Reload
	done   chan struct{}
	closed chan struct{}
}

// NewWatcher starts watching path. It watches the file's directory, since
// editors replace files by renaming, so the directory must exist.
func NewWatcher(path string) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("watch config: %w", err)
	}
	dir := filepath.Dir(path)
	if err := fsw.Add(dir); err != nil {
		_ = fsw.Close()
		return nil, fmt.Errorf("watch config directory %s: %w", dir, err)
	}
	w := &Watcher{
		path:   path,
		fsw:    fsw,
		events: make(chan Reload, 1),
		done:   make(chan struct{}),
		closed: make(chan struct{}),
	}
	go w.run()
	return w, nil
}

// Events delivers one Reload per burst of changes to the config file.
func (w *Watcher) Events() <-chan Reload { return w.events }

// Close stops watching and waits for the watcher goroutine to exit.
func (w *Watcher) Close() error {
	select {
	case <-w.done:
	default:
		close(w.done)
	}
	err := w.fsw.Close()
	<-w.closed
	return err
}

func (w *Watcher) run() {
	defer close(w.closed)
	base := filepath.Base(w.path)
	timer := time.NewTimer(debounce)
	timer.Stop()
	defer timer.Stop()
	for {
		select {
		case <-w.done:
			return
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			if filepath.Base(ev.Name) == base {
				timer.Reset(debounce)
			}
		case _, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
		case <-timer.C:
			cfg, warnings, err := Load(w.path)
			if err == nil {
				err = cfg.Validate()
			}
			select {
			case w.events <- Reload{Config: cfg, Warnings: warnings, Err: err}:
			case <-w.done:
				return
			}
		}
	}
}
