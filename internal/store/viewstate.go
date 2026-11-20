package store

import (
	"context"
	"database/sql"
	"errors"
)

// ViewState returns what was saved under key, and false when nothing was.
// The store doesn't read it: its shape belongs to the view that saved it.
func (s *Store) ViewState(ctx context.Context, key string) ([]byte, bool, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM view_state WHERE key = ?`, key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return []byte(data), true, nil
}

// SaveViewState replaces what is saved under key.
func (s *Store) SaveViewState(ctx context.Context, key string, data []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO view_state (key, data) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET data = excluded.data`, key, string(data))
	return err
}
