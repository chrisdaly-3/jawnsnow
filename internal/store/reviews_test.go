package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 18, 12, 0, 0, 123456789, time.UTC)

func sampleReview(id, prID string, started time.Time, n int) Review {
	r := Review{
		ID: id, PRID: prID, Repo: "acme/widgets", Number: 7, Author: "sam",
		HeadOID: "h1", BaseOID: "b1", MergeBaseOID: "m1",
		Tier: "deep", PassKind: "first_pass", Mode: "manual",
		Model: "claude-opus-5-5", Effort: "high", SessionID: "sess-" + id,
		Verdict: "approve", VerdictReason: "looks fine", Understanding: "u", Note: "n",
		Assumptions: []Assumption{{Claim: "c", Evidence: "e"}},
		Outcome:     "complete", CostUSD: 1.25,
		StartedAt: started, EndedAt: started.Add(time.Minute),
	}
	for i := 0; i < n; i++ {
		r.Findings = append(r.Findings, Finding{
			Path: "a.go", Line: 10 + i, StartLine: 5, Category: "bug", Blocking: i == 0,
			Body: "body", Unanchored: i == 2, Ledger: "new", ThreadID: "",
		})
	}
	return r
}

// normReview drops the monotonic clock and Location a JSON round trip
// does not preserve, and the store-assigned finding ID and ReviewID.
func normReview(r Review) Review {
	r.StartedAt = r.StartedAt.UTC().Round(0)
	r.EndedAt = r.EndedAt.UTC().Round(0)
	fs := make([]Finding, len(r.Findings))
	for i, f := range r.Findings {
		f.ID, f.ReviewID = 0, ""
		fs[i] = f
	}
	r.Findings = fs
	if len(r.Findings) == 0 {
		r.Findings = nil
	}
	return r
}

func TestSaveAndLoadReviewWithFindings(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	older := sampleReview("r1", "pr1", t0, 3)
	older.Raw = []byte(`{"verdict":"approve"}`)
	newer := sampleReview("r2", "pr1", t0.Add(time.Hour), 0) // nil Raw
	other := sampleReview("r3", "pr2", t0.Add(2*time.Hour), 1)
	for _, r := range []Review{older, newer, other} {
		if err := s.SaveReview(ctx, r); err != nil {
			t.Fatalf("SaveReview %s: %v", r.ID, err)
		}
	}

	got, err := s.Reviews(ctx, "pr1")
	if err != nil {
		t.Fatalf("Reviews: %v", err)
	}
	if len(got) != 2 || got[0].ID != "r2" || got[1].ID != "r1" {
		t.Fatalf("want [r2 r1] newest first, got %+v", got)
	}
	if !reflect.DeepEqual(normReview(got[1]), normReview(older)) {
		t.Errorf("older round trip:\n got %+v\nwant %+v", normReview(got[1]), normReview(older))
	}
	if !reflect.DeepEqual(normReview(got[0]), normReview(newer)) {
		t.Errorf("newer round trip:\n got %+v\nwant %+v", normReview(got[0]), normReview(newer))
	}
	if got[0].Raw != nil {
		t.Errorf("nil Raw came back as %q", got[0].Raw)
	}
	if len(got[1].Findings) != 3 {
		t.Fatalf("want 3 findings, got %d", len(got[1].Findings))
	}
	for _, f := range got[1].Findings {
		if f.ID == 0 || f.ReviewID != "r1" {
			t.Errorf("finding not stamped: %+v", f)
		}
	}
}

func TestSaveReviewReplacesFindings(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.SaveReview(ctx, sampleReview("r1", "pr1", t0, 3)); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveReview(ctx, sampleReview("r1", "pr1", t0, 1)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Reviews(ctx, "pr1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Findings) != 1 {
		t.Fatalf("want 1 review with 1 finding, got %+v", got)
	}
}

func TestLatestCompletedSkipsFailed(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if _, ok, err := s.LatestCompleted(ctx, "pr1"); err != nil || ok {
		t.Fatalf("empty: ok=%v err=%v", ok, err)
	}
	complete := sampleReview("r1", "pr1", t0, 0)
	failed := sampleReview("r2", "pr1", t0.Add(time.Hour), 0)
	failed.Outcome = "failed"
	for _, r := range []Review{complete, failed} {
		if err := s.SaveReview(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := s.LatestCompleted(ctx, "pr1")
	if err != nil || !ok || got.ID != "r1" {
		t.Fatalf("want r1, got %+v ok=%v err=%v", got, ok, err)
	}

	inc := sampleReview("r3", "pr1", t0.Add(2*time.Hour), 2)
	inc.Outcome = "incomplete"
	if err := s.SaveReview(ctx, inc); err != nil {
		t.Fatal(err)
	}
	got, ok, err = s.LatestCompleted(ctx, "pr1")
	if err != nil || !ok || got.ID != "r3" || len(got.Findings) != 2 {
		t.Fatalf("want r3 with findings, got %+v ok=%v err=%v", got, ok, err)
	}
}

func TestSpentSince(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	rows := []CostRow{
		{At: now.Add(-2 * time.Hour), ReviewID: "r1", PRID: "pr1", CostUSD: 1.5, Duration: time.Minute},
		{At: now.Add(-26 * time.Hour), ReviewID: "r2", PRID: "pr1", CostUSD: 4},
		// Same second as the cutoff but a different fraction of it:
		// fixed-width timestamps must still order correctly.
		{At: now.Add(-24*time.Hour + 500*time.Millisecond), ReviewID: "r3", PRID: "pr1", CostUSD: 0.25},
	}
	for _, c := range rows {
		if err := s.AddCost(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.SpentSince(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got != 1.75 {
		t.Errorf("SpentSince = %v, want 1.75", got)
	}
	// Non-UTC input is normalized.
	loc := time.FixedZone("x", 5*3600)
	if got, _ := s.SpentSince(ctx, now.Add(-24*time.Hour).In(loc)); got != 1.75 {
		t.Errorf("non-UTC SpentSince = %v, want 1.75", got)
	}
	if got, err := s.SpentSince(ctx, now.Add(time.Hour)); err != nil || got != 0 {
		t.Errorf("empty window = %v err=%v, want 0", got, err)
	}
}

func TestAutoReviewsSince(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	mk := func(id, pr, mode string, at time.Time) Review {
		r := sampleReview(id, pr, at, 0)
		r.Mode = mode
		return r
	}
	for _, r := range []Review{
		mk("a1", "pr1", "auto", now.Add(-time.Hour)),
		mk("a2", "pr1", "auto", now.Add(-2*time.Hour)),
		mk("m1", "pr1", "manual", now.Add(-3*time.Hour)),
		mk("a3", "pr1", "auto", now.Add(-30*time.Hour)),
		mk("a4", "pr2", "auto", now.Add(-time.Hour)),
	} {
		if err := s.SaveReview(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.AutoReviewsSince(ctx, "pr1", now.Add(-24*time.Hour))
	if err != nil || got != 2 {
		t.Errorf("AutoReviewsSince = %d err=%v, want 2", got, err)
	}
}

func TestSlotsCRUD(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	a := Slot{Repo: "acme/widgets", Path: "/d/slot-1", PRNumber: 7, HeadOID: "h1", LastUsed: t0, Busy: true}
	b := Slot{Repo: "acme/widgets", Path: "/d/slot-2", PRNumber: 8, HeadOID: "h2", LastUsed: t0.Add(time.Hour)}
	c := Slot{Repo: "acme/other", Path: "/o/slot-1"}
	for _, sl := range []Slot{a, b, c} {
		if err := s.SaveSlot(ctx, sl); err != nil {
			t.Fatal(err)
		}
	}
	norm := func(sl []Slot) []Slot {
		for i := range sl {
			sl[i].LastUsed = sl[i].LastUsed.UTC().Round(0)
		}
		return sl
	}
	got, err := s.Slots(ctx, "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if want := []Slot{a, b}; !reflect.DeepEqual(norm(got), norm(want)) {
		t.Errorf("Slots = %+v, want %+v", got, want)
	}

	a.Busy = false
	a.HeadOID = "h9"
	if err := s.SaveSlot(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Slots(ctx, "acme/widgets")
	if len(got) != 2 || got[0].Busy || got[0].HeadOID != "h9" {
		t.Errorf("update failed: %+v", got)
	}

	if err := s.DeleteSlot(ctx, a.Path); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSlot(ctx, "/missing"); err != nil {
		t.Errorf("deleting unknown slot: %v", err)
	}
	got, _ = s.Slots(ctx, "acme/widgets")
	if len(got) != 1 || got[0].Path != b.Path {
		t.Errorf("after delete: %+v", got)
	}
}

func TestMigratesV1Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snow.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		migrations[0],
		`CREATE TABLE schema_version (version INTEGER)`,
		`INSERT INTO schema_version (version) VALUES (1)`,
		`INSERT INTO prs (id, repo, number, place, status, updated_at, data) VALUES ('pr1', 'acme/widgets', 7, 'wall', 'OPEN', '2026-11-18T09:30:00Z', '{}')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open v1 database: %v", err)
	}
	defer s.Close()

	var v int
	if err := s.db.QueryRow(`SELECT version FROM schema_version`).Scan(&v); err != nil || v != 5 {
		t.Fatalf("schema version = %d err=%v, want 5", v, err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM prs WHERE id = 'pr1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("prs row lost: n=%d err=%v", n, err)
	}
	for _, tbl := range []string{"reviews", "findings", "cost_ledger", "worktree_slots"} {
		if _, err := s.db.Exec(`SELECT 1 FROM ` + tbl); err != nil {
			t.Errorf("table %s missing: %v", tbl, err)
		}
	}
}
