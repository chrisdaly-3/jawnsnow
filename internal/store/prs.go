package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
)

// LoadTracked returns every stored PR keyed by its GitHub node ID.
func (s *Store) LoadTracked(ctx context.Context) (map[string]tracker.Tracked, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, data FROM prs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]tracker.Tracked{}
	for rows.Next() {
		var id, data string
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		var t tracker.Tracked
		if err := json.Unmarshal([]byte(data), &t); err != nil {
			return nil, fmt.Errorf("decode PR %s: %w", id, err)
		}
		out[id] = t
	}
	return out, rows.Err()
}

// SaveTracked upserts all of ts in one transaction.
func (s *Store) SaveTracked(ctx context.Context, ts []tracker.Tracked) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO prs (id, repo, number, place, status, updated_at, data)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET repo=excluded.repo, number=excluded.number,
			place=excluded.place, status=excluded.status,
			updated_at=excluded.updated_at, data=excluded.data`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, t := range ts {
		data, err := json.Marshal(t)
		if err != nil {
			return fmt.Errorf("encode PR %s: %w", t.PR.ID, err)
		}
		if _, err := stmt.ExecContext(ctx, t.PR.ID, t.PR.Repo, t.PR.Number, string(t.Place),
			string(t.Status.Status), t.PR.UpdatedAt.UTC().Format(time.RFC3339), string(data)); err != nil {
			return fmt.Errorf("save PR %s: %w", t.PR.ID, err)
		}
	}
	return tx.Commit()
}

// DeleteTracked removes the PRs with the given IDs; unknown IDs are ignored.
func (s *Store) DeleteTracked(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM prs WHERE id IN (`+placeholders(len(ids))+`)`, args...); err != nil {
		return fmt.Errorf("delete PRs: %w", err)
	}
	return nil
}
