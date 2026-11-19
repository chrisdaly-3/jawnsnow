package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Posted is one review sent to GitHub.
type Posted struct {
	ReviewID, GitHubReviewID, URL, Event, CommitOID string
	FindingIDs                                      []int64
	At                                              time.Time
}

// RecordPost stores p and sets the ThreadID of each finding in threads, in
// one transaction. A finding that doesn't exist, or belongs to another
// review, fails the whole call.
func (s *Store) RecordPost(ctx context.Context, p Posted, threads map[int64]string) error {
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("encode post of %s: %w", p.ReviewID, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `INSERT INTO posted_reviews (review_id, at, data) VALUES (?, ?, ?)`,
		p.ReviewID, ts(p.At), string(data)); err != nil {
		return fmt.Errorf("record post of %s: %w", p.ReviewID, err)
	}
	if err := setThreads(ctx, tx, p.ReviewID, threads); err != nil {
		return err
	}
	return tx.Commit()
}

// SetThreadIDs sets the ThreadID of each finding in threads, in one
// transaction, for a post already recorded. A finding that doesn't exist,
// or belongs to another review, fails the whole call.
func (s *Store) SetThreadIDs(ctx context.Context, reviewID string, threads map[int64]string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setThreads(ctx, tx, reviewID, threads); err != nil {
		return err
	}
	return tx.Commit()
}

func setThreads(ctx context.Context, tx *sql.Tx, reviewID string, threads map[int64]string) error {
	for id, thread := range threads {
		res, err := tx.ExecContext(ctx, `UPDATE findings SET data = json_set(data, '$.ThreadID', ?)
			WHERE id = ? AND review_id = ?`, thread, id, reviewID)
		if err != nil {
			return fmt.Errorf("set thread of finding %d: %w", id, err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return fmt.Errorf("set thread of finding %d: no such finding in review %s", id, reviewID)
		}
	}
	return nil
}

// PostedFor returns reviewID's posts, newest first.
func (s *Store) PostedFor(ctx context.Context, reviewID string) ([]Posted, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM posted_reviews
		WHERE review_id = ? ORDER BY at DESC, id DESC`, reviewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Posted
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var p Posted
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			return nil, fmt.Errorf("decode post: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PostedReviewIDs is the set of review IDs that were posted at least once.
func (s *Store) PostedReviewIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT review_id FROM posted_reviews`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
