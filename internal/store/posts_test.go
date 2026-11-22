package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func seedReview(t *testing.T, s *Store, id, prID string, findings int) Review {
	t.Helper()
	r := sampleReview(id, prID, t0, findings)
	if err := s.SaveReview(context.Background(), r); err != nil {
		t.Fatalf("SaveReview: %v", err)
	}
	got, err := s.Reviews(context.Background(), prID)
	if err != nil || len(got) == 0 {
		t.Fatalf("Reviews: %v %v", got, err)
	}
	for _, g := range got {
		if g.ID == id {
			return g
		}
	}
	t.Fatalf("review %s not found", id)
	return Review{}
}

func TestRecordPostSetsThreadIDs(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	r := seedReview(t, s, "r1", "pr1", 3)
	ids := []int64{r.Findings[0].ID, r.Findings[1].ID}
	p := Posted{ReviewID: "r1", GitHubReviewID: "PRR_1", URL: "https://x/1", Event: "COMMENT",
		CommitOID: "h1", FindingIDs: ids, At: t0}
	threads := map[int64]string{ids[0]: "T_a", ids[1]: "T_b"}
	if err := s.RecordPost(ctx, p, threads); err != nil {
		t.Fatalf("RecordPost: %v", err)
	}
	got, err := s.Reviews(ctx, "pr1")
	if err != nil {
		t.Fatal(err)
	}
	fs := got[0].Findings
	if fs[0].ThreadID != "T_a" || fs[1].ThreadID != "T_b" || fs[2].ThreadID != "" {
		t.Errorf("thread IDs = %q %q %q", fs[0].ThreadID, fs[1].ThreadID, fs[2].ThreadID)
	}
	if fs[0].Path != "a.go" || fs[0].Line != 10 || !fs[0].Blocking {
		t.Errorf("rewrite lost other fields: %+v", fs[0])
	}
	posted, err := s.PostedFor(ctx, "r1")
	if err != nil || len(posted) != 1 {
		t.Fatalf("PostedFor = %v, %v", posted, err)
	}
	if g := posted[0]; g.GitHubReviewID != "PRR_1" || g.URL != "https://x/1" || g.Event != "COMMENT" ||
		g.CommitOID != "h1" || len(g.FindingIDs) != 2 || !g.At.Equal(t0) {
		t.Errorf("posted = %+v", g)
	}
}

func TestRecordPostIsAtomic(t *testing.T) {
	t.Run("bad finding ID", func(t *testing.T) {
		s, _ := openTemp(t)
		ctx := context.Background()
		r := seedReview(t, s, "r1", "pr1", 2)
		good := r.Findings[0].ID
		p := Posted{ReviewID: "r1", GitHubReviewID: "PRR_1", At: t0, FindingIDs: []int64{good}}
		if err := s.RecordPost(ctx, p, map[int64]string{good: "T_a", 9999: "T_bad"}); err == nil {
			t.Fatal("want an error for an unknown finding ID")
		}
		assertNothingRecorded(t, s)
	})
	t.Run("finding of another review", func(t *testing.T) {
		s, _ := openTemp(t)
		ctx := context.Background()
		seedReview(t, s, "r1", "pr1", 1)
		o := seedReview(t, s, "r2", "pr2", 1)
		p := Posted{ReviewID: "r1", At: t0}
		if err := s.RecordPost(ctx, p, map[int64]string{o.Findings[0].ID: "T"}); err == nil {
			t.Fatal("want an error for a finding of another review")
		}
		assertNothingRecorded(t, s)
	})
	t.Run("closed database", func(t *testing.T) {
		s, _ := openTemp(t)
		seedReview(t, s, "r1", "pr1", 1)
		s.Close()
		if err := s.RecordPost(context.Background(), Posted{ReviewID: "r1", At: t0}, nil); err == nil {
			t.Fatal("want an error on a closed database")
		}
	})
}

func assertNothingRecorded(t *testing.T, s *Store) {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM posted_reviews`).Scan(&n); err != nil || n != 0 {
		t.Errorf("posted_reviews rows = %d err=%v, want 0", n, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM findings WHERE data LIKE '%"ThreadID":"T%'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("findings with a thread ID = %d err=%v, want 0", n, err)
	}
}

func TestPostedForNewestFirst(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	seedReview(t, s, "r1", "pr1", 0)
	// Inserted out of order; times differ below the second so the fixed-width format matters.
	for _, p := range []Posted{
		{ReviewID: "r1", GitHubReviewID: "mid", At: t0.Add(time.Second)},
		{ReviewID: "r1", GitHubReviewID: "old", At: t0},
		{ReviewID: "r1", GitHubReviewID: "new", At: t0.Add(time.Second + 500*time.Millisecond)},
		{ReviewID: "other", GitHubReviewID: "x", At: t0.Add(time.Hour)},
	} {
		if err := s.RecordPost(ctx, p, nil); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.PostedFor(ctx, "r1")
	if err != nil || len(got) != 3 {
		t.Fatalf("PostedFor = %v, %v", got, err)
	}
	if got[0].GitHubReviewID != "new" || got[1].GitHubReviewID != "mid" || got[2].GitHubReviewID != "old" {
		t.Errorf("order = %s %s %s", got[0].GitHubReviewID, got[1].GitHubReviewID, got[2].GitHubReviewID)
	}
}

func TestMigratesV6Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snow.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) < 7 {
		t.Fatalf("want at least 7 migrations, have %d", len(migrations))
	}
	stmts := []string{`CREATE TABLE schema_version (version INTEGER)`, `INSERT INTO schema_version (version) VALUES (6)`}
	stmts = append(stmts, migrations[:6]...)
	stmts = append(stmts,
		`INSERT INTO reviews (id, pr_id, started_at, mode, outcome, data) VALUES ('r1', 'pr1', '2026-11-18T12:00:00.000000000Z', 'manual', 'complete', '{"ID":"r1","PRID":"pr1"}')`,
		`INSERT INTO findings (review_id, data) VALUES ('r1', '{"Path":"a.go"}')`,
		`INSERT INTO cost_ledger (at, review_id, pr_id, cost_usd, data) VALUES ('2026-11-18T12:00:00.000000000Z', 'r1', 'pr1', 1.5, '{}')`)
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open v6 database: %v", err)
	}
	defer s.Close()
	var v int
	if err := s.db.QueryRow(`SELECT version FROM schema_version`).Scan(&v); err != nil || v != len(migrations) {
		t.Fatalf("schema version = %d err=%v, want %d", v, err, len(migrations))
	}
	for tbl, want := range map[string]int{"reviews": 1, "findings": 1, "cost_ledger": 1, "posted_reviews": 0} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + tbl).Scan(&n); err != nil || n != want {
			t.Errorf("%s rows = %d err=%v, want %d", tbl, n, err, want)
		}
	}
	// The migrated database takes a post.
	if err := s.RecordPost(context.Background(), Posted{ReviewID: "r1", At: t0}, nil); err != nil {
		t.Errorf("RecordPost on migrated database: %v", err)
	}
}

func TestPostedReviewIDs(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if got, err := s.PostedReviewIDs(ctx); err != nil || len(got) != 0 {
		t.Fatalf("empty store: %v, %v", got, err)
	}
	seedReview(t, s, "r1", "pr1", 0)
	seedReview(t, s, "r2", "pr1", 1)
	seedReview(t, s, "r3", "pr1", 2)
	// r1 is posted twice and must appear once; r3 is never posted.
	for _, p := range []Posted{
		{ReviewID: "r1", GitHubReviewID: "a", At: t0},
		{ReviewID: "r1", GitHubReviewID: "b", At: t0.Add(time.Second)},
		{ReviewID: "r2", GitHubReviewID: "c", At: t0},
	} {
		if err := s.RecordPost(ctx, p, nil); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.PostedReviewIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got["r1"] || !got["r2"] || got["r3"] {
		t.Errorf("PostedReviewIDs = %v, want r1 and r2", got)
	}
}

// The record is written as soon as the post lands; thread IDs follow in a
// second write once they are looked up.
func TestSetThreadIDsAfterRecord(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	r := seedReview(t, s, "r1", "pr1", 3)
	o := seedReview(t, s, "r2", "pr2", 1)
	if err := s.RecordPost(ctx, Posted{ReviewID: "r1", GitHubReviewID: "PRR_1", At: t0}, nil); err != nil {
		t.Fatal(err)
	}
	a, b := r.Findings[0].ID, r.Findings[2].ID
	if err := s.SetThreadIDs(ctx, "r1", map[int64]string{a: "T_a", b: "T_b"}); err != nil {
		t.Fatalf("SetThreadIDs: %v", err)
	}
	got, err := s.Reviews(ctx, "pr1")
	if err != nil {
		t.Fatal(err)
	}
	fs := got[0].Findings
	if fs[0].ThreadID != "T_a" || fs[1].ThreadID != "" || fs[2].ThreadID != "T_b" {
		t.Errorf("thread IDs = %q %q %q", fs[0].ThreadID, fs[1].ThreadID, fs[2].ThreadID)
	}
	if fs[0].Path != "a.go" || fs[0].Line != 10 || !fs[0].Blocking {
		t.Errorf("rewrite lost other fields: %+v", fs[0])
	}

	// A finding of another review fails the whole write.
	if err := s.SetThreadIDs(ctx, "r1", map[int64]string{r.Findings[1].ID: "T_c", o.Findings[0].ID: "T_x"}); err == nil {
		t.Fatal("want an error for another review's finding")
	}
	got, _ = s.Reviews(ctx, "pr1")
	if tid := got[0].Findings[1].ThreadID; tid != "" {
		t.Errorf("a failed write set thread %q", tid)
	}
}

func TestLatestPostsOfKeepsNewestPerReview(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	seedReview(t, s, "r1", "pr1", 0)
	seedReview(t, s, "r2", "pr1", 0)
	seedReview(t, s, "r3", "pr2", 0)
	for _, p := range []Posted{
		{ReviewID: "r1", GitHubReviewID: "old", At: t0},
		{ReviewID: "r1", GitHubReviewID: "new", At: t0.Add(time.Second)},
		{ReviewID: "r3", GitHubReviewID: "elsewhere", At: t0.Add(time.Hour)},
	} {
		if err := s.RecordPost(ctx, p, nil); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.LatestPostsOf(ctx, "pr1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["r1"].GitHubReviewID != "new" {
		t.Errorf("LatestPostsOf = %+v, want only r1's newest", got)
	}
}
