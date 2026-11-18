// Package store keeps tracked PRs in SQLite so the wall survives a restart.
package store

import (
	"database/sql"
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

// Open creates the database and its parent directories if needed, and
// migrates it to the current schema. It refuses a database written by a
// newer snow.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// One connection keeps the per-connection pragmas in force and
	// serializes writes.
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	for _, p := range []string{`PRAGMA journal_mode=WAL`, `PRAGMA busy_timeout=5000`} {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}
	var cur int
	err := s.db.QueryRow(`SELECT version FROM schema_version`).Scan(&cur)
	fresh := err == sql.ErrNoRows
	if err != nil && !fresh {
		return fmt.Errorf("read schema version: %w", err)
	}
	if cur > len(migrations) {
		return fmt.Errorf("database schema v%d is newer than this snow (v%d); upgrade snow", cur, len(migrations))
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
