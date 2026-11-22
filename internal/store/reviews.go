package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

type Assumption struct{ Claim, Evidence string }

type Finding struct {
	ID              int64
	ReviewID, Path  string
	Line, StartLine int
	Category        string
	Blocking        bool
	Body            string
	Unanchored      bool
	Ledger          string // "new" | "addressed" | "outstanding" | "answered"
	ThreadID        string // set by plan 3 when posted
}

type Review struct {
	ID, PRID, Repo                        string
	Number                                int
	Author                                string
	HeadOID, BaseOID, MergeBaseOID        string
	Tier, PassKind, Mode, Model, Effort   string
	SessionID                             string
	Verdict, VerdictReason, Understanding string
	Note                                  string
	Assumptions                           []Assumption
	Outcome                               string // "complete" | "incomplete" | "failed"
	Error                                 string
	Raw                                   json.RawMessage `json:",omitempty"` // omitempty keeps a nil Raw from becoming "null"
	CostUSD                               float64         // the session's final reported total
	CostUnknown                           bool            // a run ended with no reported cost; CostUSD leaves it out
	StartedAt, EndedAt                    time.Time
	Findings                              []Finding `json:"-"` // stored in the findings table
	// CostRuns counts the review's ledger rows; reads fill it in.
	CostRuns CostRuns `json:"-"`
}

// CostRuns is how many of a review's runs reported a cost, a real $0
// included, and how many ended with none.
type CostRuns struct{ Known, Unknown int }

// costRunsSQL counts the ledger rows of the review whose ID is r.id.
const costRunsSQL = `(SELECT COALESCE(SUM(COALESCE(json_extract(c.data, '$.CostUnknown'), 0) = 0), 0) FROM cost_ledger c WHERE c.review_id = r.id),
	(SELECT COALESCE(SUM(COALESCE(json_extract(c.data, '$.CostUnknown'), 0) != 0), 0) FROM cost_ledger c WHERE c.review_id = r.id)`

type CostRow struct {
	At                                                                         time.Time
	ReviewID, PRID, Repo, Author, Tier, PassKind, Mode, Model, Effort, Outcome string
	CostUSD                                                                    float64
	CostUnknown                                                                bool // the run ended with no result line, so CostUSD is not its cost
	TokensIn, TokensOut, TokensCached                                          int
	Duration                                                                   time.Duration
}

type Slot struct {
	Repo, Path string
	PRNumber   int
	HeadOID    string
	LastUsed   time.Time
	Busy       bool
}

// ts renders t as UTC RFC 3339 with a fixed nine-digit fraction. A fixed
// width matters: RFC3339Nano trims trailing zeros, and "…:05Z" would then
// sort after "…:05.5Z" as plain strings.
func ts(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
}

// SaveReview upserts r and replaces its findings, in one transaction.
func (s *Store) SaveReview(ctx context.Context, r Review) error {
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encode review %s: %w", r.ID, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `INSERT INTO reviews (id, pr_id, started_at, mode, outcome, data)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET pr_id=excluded.pr_id, started_at=excluded.started_at,
			mode=excluded.mode, outcome=excluded.outcome, data=excluded.data`,
		r.ID, r.PRID, ts(r.StartedAt), r.Mode, r.Outcome, string(data)); err != nil {
		return fmt.Errorf("save review %s: %w", r.ID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM findings WHERE review_id = ?`, r.ID); err != nil {
		return fmt.Errorf("clear findings of %s: %w", r.ID, err)
	}
	for _, f := range r.Findings {
		fd, err := json.Marshal(f)
		if err != nil {
			return fmt.Errorf("encode finding of %s: %w", r.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO findings (review_id, data) VALUES (?, ?)`,
			r.ID, string(fd)); err != nil {
			return fmt.Errorf("save finding of %s: %w", r.ID, err)
		}
	}
	return tx.Commit()
}

// Reviews returns prID's reviews with their findings, newest StartedAt first.
func (s *Store) Reviews(ctx context.Context, prID string) ([]Review, error) {
	return s.queryReviews(ctx, `SELECT r.data, `+costRunsSQL+` FROM reviews r WHERE r.pr_id = ? ORDER BY r.started_at DESC, r.id`, prID)
}

// ReviewIDsOf returns the IDs of every review of PR number in repo, sorted.
// It looks the PR up by repo and number because a PR that has stopped
// being tracked may have no stored node ID left to look it up by.
func (s *Store) ReviewIDsOf(ctx context.Context, repo string, number int) ([]string, error) {
	return queryStrings(ctx, s.db, `SELECT id FROM reviews
		WHERE json_extract(data, '$.Repo') = ? AND json_extract(data, '$.Number') = ?
		ORDER BY id`, repo, number)
}

// LatestReview returns prID's newest review, whatever its outcome.
func (s *Store) LatestReview(ctx context.Context, prID string) (Review, bool, error) {
	return s.latestReview(ctx, prID, "")
}

// LatestCompleted returns prID's newest review whose outcome is complete or
// incomplete; failed runs are skipped.
func (s *Store) LatestCompleted(ctx context.Context, prID string) (Review, bool, error) {
	return s.latestReview(ctx, prID, ` AND r.outcome IN ('complete', 'incomplete')`)
}

// latestReview returns prID's newest review that also meets cond, a SQL
// condition on r appended to the WHERE clause.
func (s *Store) latestReview(ctx context.Context, prID, cond string) (Review, bool, error) {
	rs, err := s.queryReviews(ctx, `SELECT r.data, `+costRunsSQL+` FROM reviews r
		WHERE r.pr_id = ?`+cond+`
		ORDER BY r.started_at DESC, r.id LIMIT 1`, prID)
	if err != nil || len(rs) == 0 {
		return Review{}, false, err
	}
	return rs[0], true, nil
}

// queryReviews decodes the reviews the query selects, as data and the two
// costRunsSQL counts, then loads their findings. The reviews are read to
// completion first: the pool holds one connection, so a second query can't
// run while rows are open.
func (s *Store) queryReviews(ctx context.Context, query string, args ...any) ([]Review, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var out []Review
	for rows.Next() {
		var data string
		var runs CostRuns
		if err := rows.Scan(&data, &runs.Known, &runs.Unknown); err != nil {
			rows.Close()
			return nil, err
		}
		var r Review
		if err := json.Unmarshal([]byte(data), &r); err != nil {
			rows.Close()
			return nil, fmt.Errorf("decode review: %w", err)
		}
		r.CostRuns = runs
		out = append(out, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(out))
	for i, r := range out {
		ids[i] = r.ID
	}
	byReview, err := s.findingsOf(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Findings = byReview[out[i].ID]
	}
	return out, nil
}

// findingsBatch caps the review IDs bound to one findings query, well
// under SQLite's limit on bound parameters.
const findingsBatch = 500

// findingsOf returns the findings of each of reviewIDs, keyed by review ID
// and in the order they were saved, with one query per findingsBatch
// reviews rather than one per review.
func (s *Store) findingsOf(ctx context.Context, reviewIDs []string) (map[string][]Finding, error) {
	out := make(map[string][]Finding, len(reviewIDs))
	for batch := range slices.Chunk(reviewIDs, findingsBatch) {
		args := make([]any, len(batch))
		for i, id := range batch {
			args[i] = id
		}
		rows, err := s.db.QueryContext(ctx, `SELECT id, review_id, data FROM findings
			WHERE review_id IN (`+placeholders(len(batch))+`) ORDER BY id`, args...)
		if err != nil {
			return nil, err
		}
		if err := scanFindings(rows, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// scanFindings decodes rows of (id, review_id, data) into out and closes
// rows.
func scanFindings(rows *sql.Rows, out map[string][]Finding) error {
	defer rows.Close()
	for rows.Next() {
		var id int64
		var reviewID, data string
		if err := rows.Scan(&id, &reviewID, &data); err != nil {
			return err
		}
		var f Finding
		if err := json.Unmarshal([]byte(data), &f); err != nil {
			return fmt.Errorf("decode finding %d: %w", id, err)
		}
		f.ID, f.ReviewID = id, reviewID
		out[reviewID] = append(out[reviewID], f)
	}
	return rows.Err()
}

// AddCost appends a row to the cost ledger.
func (s *Store) AddCost(ctx context.Context, c CostRow) error {
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode cost row: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO cost_ledger (at, review_id, pr_id, cost_usd, data)
		VALUES (?, ?, ?, ?, ?)`, ts(c.At), c.ReviewID, c.PRID, c.CostUSD, string(data))
	return err
}

// SpentSince sums the ledger's cost from t onward.
func (s *Store) SpentSince(ctx context.Context, t time.Time) (float64, error) {
	var sum sql.NullFloat64
	if err := s.db.QueryRowContext(ctx, `SELECT SUM(cost_usd) FROM cost_ledger WHERE at >= ?`, ts(t)).Scan(&sum); err != nil {
		return 0, err
	}
	return sum.Float64, nil
}

// AutoReviewsSince counts prID's reviews started at or after t with Mode "auto".
func (s *Store) AutoReviewsSince(ctx context.Context, prID string, t time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM reviews
		WHERE pr_id = ? AND mode = 'auto' AND started_at >= ?`, prID, ts(t)).Scan(&n)
	return n, err
}

// Slots returns repo's worktree slots ordered by path.
func (s *Store) Slots(ctx context.Context, repo string) ([]Slot, error) {
	return queryJSON[Slot](ctx, s.db, "slot", `SELECT data FROM worktree_slots WHERE repo = ? ORDER BY path`, repo)
}

// SaveSlot upserts a worktree slot keyed by its path.
func (s *Store) SaveSlot(ctx context.Context, sl Slot) error {
	data, err := json.Marshal(sl)
	if err != nil {
		return fmt.Errorf("encode slot %s: %w", sl.Path, err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO worktree_slots (path, repo, data) VALUES (?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET repo=excluded.repo, data=excluded.data`,
		sl.Path, sl.Repo, string(data))
	return err
}

// SlotRepos lists, sorted, every repo that has a stored slot.
func (s *Store) SlotRepos(ctx context.Context) ([]string, error) {
	return queryStrings(ctx, s.db, `SELECT DISTINCT repo FROM worktree_slots ORDER BY repo`)
}

// DeleteSlot removes the slot at path; an unknown path is ignored.
func (s *Store) DeleteSlot(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM worktree_slots WHERE path = ?`, path)
	return err
}
