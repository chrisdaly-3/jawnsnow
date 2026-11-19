package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// CostFilter narrows CostRows. An empty slice matches any value; a zero
// From or To leaves that end of the range open.
type CostFilter struct {
	From, To                                                time.Time // [From, To)
	Authors, Repos, Models, Efforts, Tiers, Modes, Outcomes []string
}

// CostRows returns the ledger rows the filter matches, ordered by At.
func (s *Store) CostRows(ctx context.Context, f CostFilter) ([]CostRow, error) {
	var where []string
	var args []any
	if !f.From.IsZero() {
		where = append(where, `at >= ?`)
		args = append(args, ts(f.From))
	}
	if !f.To.IsZero() {
		where = append(where, `at < ?`)
		args = append(args, ts(f.To))
	}
	for _, in := range []struct {
		field string
		vals  []string
	}{
		{"Author", f.Authors}, {"Repo", f.Repos}, {"Model", f.Models}, {"Effort", f.Efforts},
		{"Tier", f.Tiers}, {"Mode", f.Modes}, {"Outcome", f.Outcomes},
	} {
		if len(in.vals) == 0 {
			continue
		}
		where = append(where, `json_extract(data, '$.`+in.field+`') IN (`+placeholders(len(in.vals))+`)`)
		for _, v := range in.vals {
			args = append(args, v)
		}
	}
	q := `SELECT data FROM cost_ledger`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CostRow
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var c CostRow
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			return nil, fmt.Errorf("decode cost row: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// LogFilter narrows ReviewLog. Text matches the repo, number, title or
// author, ignoring case; the other fields match exactly when set.
type LogFilter struct {
	Text, Outcome string
	PRID          string
	Limit         int
}

// LogEntry is one review with its PR's title and finding counts. Review's
// Findings is left empty; use Reviews for those.
type LogEntry struct {
	Review             Review
	Title              string // blank when the PR is no longer tracked
	Findings, Blocking int
}

const defaultLogLimit = 500

// ReviewLog returns the reviews the filter matches, newest first.
func (s *Store) ReviewLog(ctx context.Context, f LogFilter) ([]LogEntry, error) {
	// instr, not LIKE, so a % or _ in the text matches itself.
	where := []string{`1 = 1`}
	var args []any
	if f.Text != "" {
		where = append(where, `(instr(lower(json_extract(r.data, '$.Repo')), lower(?1)) > 0
			OR instr(CAST(json_extract(r.data, '$.Number') AS TEXT), lower(?1)) > 0
			OR instr(lower(json_extract(r.data, '$.Author')), lower(?1)) > 0
			OR instr(lower(COALESCE(json_extract(p.data, '$.PR.Title'), '')), lower(?1)) > 0)`)
		args = append(args, f.Text)
	}
	if f.Outcome != "" {
		where = append(where, `r.outcome = ?`)
		args = append(args, f.Outcome)
	}
	if f.PRID != "" {
		where = append(where, `r.pr_id = ?`)
		args = append(args, f.PRID)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = defaultLogLimit
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT r.data, COALESCE(json_extract(p.data, '$.PR.Title'), ''),
			(SELECT COUNT(*) FROM findings f WHERE f.review_id = r.id),
			(SELECT COUNT(*) FROM findings f WHERE f.review_id = r.id AND json_extract(f.data, '$.Blocking') = 1)
		FROM reviews r LEFT JOIN prs p ON p.id = r.pr_id
		WHERE `+strings.Join(where, ` AND `)+`
		ORDER BY r.started_at DESC, r.id LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogEntry
	for rows.Next() {
		var data string
		var e LogEntry
		if err := rows.Scan(&data, &e.Title, &e.Findings, &e.Blocking); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(data), &e.Review); err != nil {
			return nil, fmt.Errorf("decode review: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PRStat is the review count and total spend of one PR.
type PRStat struct {
	Reviews int
	CostUSD float64
	// Unknown is how many of the PR's ledger rows ended with no reported
	// cost; CostUSD leaves them out.
	Unknown int
}

// PRReviewStats returns each PR's review count and spend, keyed by PR ID.
// Spend sums every ledger row, continuation runs included. A row that ended
// with no reported cost adds nothing.
func (s *Store) PRReviewStats(ctx context.Context) (map[string]PRStat, error) {
	out := map[string]PRStat{}
	rows, err := s.db.QueryContext(ctx, `SELECT pr_id, COUNT(*) FROM reviews GROUP BY pr_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var st PRStat
		if err := rows.Scan(&id, &st.Reviews); err != nil {
			rows.Close()
			return nil, err
		}
		out[id] = st
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}

	rows, err = s.db.QueryContext(ctx, `SELECT pr_id,
			COALESCE(SUM(CASE WHEN COALESCE(json_extract(data, '$.CostUnknown'), 0) = 0 THEN cost_usd END), 0),
			COALESCE(SUM(COALESCE(json_extract(data, '$.CostUnknown'), 0) != 0), 0)
		FROM cost_ledger GROUP BY pr_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var cost float64
		var unknown int
		if err := rows.Scan(&id, &cost, &unknown); err != nil {
			return nil, err
		}
		st := out[id]
		st.CostUSD, st.Unknown = cost, unknown
		out[id] = st
	}
	return out, rows.Err()
}
