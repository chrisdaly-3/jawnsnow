// Package store keeps tracked PRs in SQLite so the wall survives a restart.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// migrations is ordered; entry i upgrades the schema from version i to i+1.
// Append new entries, never edit shipped ones.
var migrations = []string{
	`CREATE TABLE prs (
		id TEXT PRIMARY KEY,
		repo TEXT,
		number INTEGER,
		place TEXT,
		status TEXT,
		updated_at TEXT,
		data TEXT
	)`,
	`CREATE TABLE reviews (
		id TEXT PRIMARY KEY,
		pr_id TEXT,
		started_at TEXT,
		mode TEXT,
		outcome TEXT,
		data TEXT
	)`,
	`CREATE TABLE findings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		review_id TEXT,
		data TEXT
	)`,
	`CREATE TABLE cost_ledger (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		at TEXT,
		review_id TEXT,
		pr_id TEXT,
		cost_usd REAL,
		data TEXT
	)`,
	`CREATE TABLE worktree_slots (
		path TEXT PRIMARY KEY,
		repo TEXT,
		data TEXT
	)`,
	// The lookups every drawer open, poll evaluation and spend total make.
	`CREATE INDEX findings_review_id ON findings (review_id);
	CREATE INDEX reviews_pr_id_started_at ON reviews (pr_id, started_at);
	CREATE INDEX cost_ledger_at ON cost_ledger (at)`,
	// Reviews posted to GitHub; the thread IDs live in the findings' data.
	`CREATE TABLE posted_reviews (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		review_id TEXT,
		at TEXT,
		data TEXT
	);
	CREATE INDEX posted_reviews_review_id ON posted_reviews (review_id)`,
	// Each review's cost runs, counted on every review read.
	`CREATE INDEX cost_ledger_review_id ON cost_ledger (review_id)`,
	// What a view remembers across restarts, by view.
	`CREATE TABLE view_state (
		key TEXT PRIMARY KEY,
		data TEXT
	)`,
}

type Store struct {
	db *sql.DB
}

// DataPath is $XDG_DATA_HOME/jawnsnow/jawnsnow.db, falling back to
// $HOME/.local/share.
func DataPath() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".local", "share")
	}
	return filepath.Join(base, "jawnsnow", "jawnsnow.db")
}

// dsnOptions apply to every connection the pool opens: write-ahead
// logging, a wait of up to 5s for another process's lock, and BEGIN
// IMMEDIATE, so a transaction takes the write lock when it starts and two
// processes can't both read a value and then race to write it.
const dsnOptions = "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_txlock=immediate"

// Open creates the database and its parent directories if needed, and
// migrates it to the current schema. It refuses a database written by a
// newer snow.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	db, err := sql.Open("sqlite", path+dsnOptions)
	if err != nil {
		return nil, err
	}
	// One connection serializes writes.
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// migrate brings the schema up to date in one transaction. The version is
// read inside it, under the write lock, so a second snow opening the same
// database waits and then finds nothing left to do.
func (s *Store) migrate() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}
	var cur int
	err = tx.QueryRow(`SELECT version FROM schema_version`).Scan(&cur)
	fresh := errors.Is(err, sql.ErrNoRows)
	if err != nil && !fresh {
		return fmt.Errorf("read schema version: %w", err)
	}
	switch {
	case cur > len(migrations):
		return fmt.Errorf("database schema v%d is newer than this snow (v%d); upgrade snow", cur, len(migrations))
	case cur == len(migrations):
		return tx.Commit()
	}
	for i := cur; i < len(migrations); i++ {
		if _, err := tx.Exec(migrations[i]); err != nil {
			return fmt.Errorf("migrate to v%d: %w", i+1, err)
		}
	}
	if fresh {
		_, err = tx.Exec(`INSERT INTO schema_version (version) VALUES (?)`, len(migrations))
	} else {
		_, err = tx.Exec(`UPDATE schema_version SET version = ?`, len(migrations))
	}
	if err != nil {
		return fmt.Errorf("record schema version: %w", err)
	}
	return tx.Commit()
}
