package store

import (
	"context"
	"testing"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
)

func addCost(t *testing.T, s *Store, c CostRow) {
	t.Helper()
	if err := s.AddCost(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

func TestCostRowsFilters(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	base := CostRow{ReviewID: "r", PRID: "pr1", Repo: "acme/widgets", Author: "sam", Tier: "deep",
		PassKind: "first_pass", Mode: "manual", Model: "opus", Effort: "high", Outcome: "complete", CostUSD: 1}
	rows := []CostRow{
		base,
		func() CostRow { c := base; c.ReviewID = "author"; c.Author = "kim"; return c }(),
		func() CostRow { c := base; c.ReviewID = "repo"; c.Repo = "acme/gears"; return c }(),
		func() CostRow { c := base; c.ReviewID = "model"; c.Model = "sonnet"; return c }(),
		func() CostRow { c := base; c.ReviewID = "effort"; c.Effort = "low"; return c }(),
		func() CostRow { c := base; c.ReviewID = "tier"; c.Tier = "light"; return c }(),
		func() CostRow { c := base; c.ReviewID = "mode"; c.Mode = "auto"; return c }(),
		func() CostRow { c := base; c.ReviewID = "outcome"; c.Outcome = "failed"; return c }(),
	}
	for i := range rows {
		rows[i].At = t0.Add(time.Duration(i) * time.Minute)
		addCost(t, s, rows[i])
	}

	ids := func(f CostFilter) []string {
		t.Helper()
		got, err := s.CostRows(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range got {
			out = append(out, r.ReviewID)
		}
		return out
	}
	eq := func(got []string, want ...string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	all := []string{"r", "author", "repo", "model", "effort", "tier", "mode", "outcome"}
	for _, tc := range []struct {
		name string
		f    CostFilter
		want []string
	}{
		{"no filter", CostFilter{}, all},
		{"author", CostFilter{Authors: []string{"kim"}}, []string{"author"}},
		{"repo", CostFilter{Repos: []string{"acme/gears"}}, []string{"repo"}},
		{"model", CostFilter{Models: []string{"sonnet"}}, []string{"model"}},
		{"effort", CostFilter{Efforts: []string{"low"}}, []string{"effort"}},
		{"tier", CostFilter{Tiers: []string{"light"}}, []string{"tier"}},
		{"mode", CostFilter{Modes: []string{"auto"}}, []string{"mode"}},
		{"outcome", CostFilter{Outcomes: []string{"failed"}}, []string{"outcome"}},
		{"two values OR", CostFilter{Authors: []string{"kim", "nobody"}, Repos: []string{"acme/widgets"}}, []string{"author"}},
		{"values within a field OR", CostFilter{Models: []string{"sonnet", "opus"}, Modes: []string{"manual"}}, []string{"r", "author", "repo", "model", "effort", "tier", "outcome"}},
		{"from is inclusive", CostFilter{From: rows[6].At}, []string{"mode", "outcome"}},
		{"to is exclusive", CostFilter{To: rows[2].At}, []string{"r", "author"}},
		{"from and to", CostFilter{From: rows[1].At, To: rows[3].At}, []string{"author", "repo"}},
		{"empty range", CostFilter{From: rows[3].At, To: rows[3].At}, nil},
		{"no match", CostFilter{Authors: []string{"nobody"}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ids(tc.f)
			if !eq(got, tc.want...) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCostRowsDecodesFields(t *testing.T) {
	s, _ := openTemp(t)
	in := CostRow{At: t0, ReviewID: "r", PRID: "pr1", Repo: "acme/widgets", CostUSD: 2.5,
		CostUnknown: true, TokensIn: 1, TokensOut: 2, TokensCached: 3, Duration: time.Minute}
	addCost(t, s, in)
	got, err := s.CostRows(context.Background(), CostFilter{})
	if err != nil || len(got) != 1 {
		t.Fatalf("CostRows = %v, %v", got, err)
	}
	g := got[0]
	if !g.At.Equal(in.At) || g.CostUSD != 2.5 || !g.CostUnknown || g.TokensCached != 3 || g.Duration != time.Minute {
		t.Errorf("got %+v", g)
	}
}

func TestReviewLogFilters(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	// pr1 is tracked (title "Fix pr1", author sam); pr2 is not tracked.
	if err := s.SaveTracked(ctx, []tracker.Tracked{sample("pr1", 7)}); err != nil {
		t.Fatal(err)
	}
	mk := func(id, pr, repo string, number int, author, outcome string, at time.Time, findings int) {
		r := sampleReview(id, pr, at, findings)
		r.Repo, r.Number, r.Author, r.Outcome = repo, number, author, outcome
		if err := s.SaveReview(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	mk("r1", "pr1", "acme/widgets", 7, "sam", "complete", t0, 3)                    // 3 findings, 1 blocking
	mk("r2", "pr1", "acme/widgets", 7, "sam", "failed", t0.Add(time.Hour), 0)       // newest
	mk("r3", "pr2", "acme/gears", 42, "KIM", "complete", t0.Add(30*time.Minute), 1) // untracked

	ids := func(f LogFilter) []string {
		t.Helper()
		got, err := s.ReviewLog(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range got {
			out = append(out, e.Review.ID)
		}
		return out
	}
	same := func(got []string, want ...string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}
	for _, tc := range []struct {
		name string
		f    LogFilter
		want []string
	}{
		{"newest first", LogFilter{}, []string{"r2", "r3", "r1"}},
		{"text repo", LogFilter{Text: "GEARS"}, []string{"r3"}},
		{"text number", LogFilter{Text: "42"}, []string{"r3"}},
		{"text title", LogFilter{Text: "fix PR1"}, []string{"r2", "r1"}},
		{"text author", LogFilter{Text: "kim"}, []string{"r3"}},
		{"text no match", LogFilter{Text: "zzz"}, nil},
		{"outcome", LogFilter{Outcome: "failed"}, []string{"r2"}},
		{"pr id", LogFilter{PRID: "pr2"}, []string{"r3"}},
		{"limit", LogFilter{Limit: 2}, []string{"r2", "r3"}},
		{"text and outcome", LogFilter{Text: "sam", Outcome: "complete"}, []string{"r1"}},
		{"percent is literal", LogFilter{Text: "%"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ids(tc.f); !same(got, tc.want...) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}

	got, err := s.ReviewLog(ctx, LogFilter{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]LogEntry{}
	for _, e := range got {
		byID[e.Review.ID] = e
	}
	if e := byID["r1"]; e.Title != "Fix pr1" || e.Findings != 3 || e.Blocking != 1 {
		t.Errorf("r1 entry = title %q findings %d blocking %d", e.Title, e.Findings, e.Blocking)
	}
	if e := byID["r2"]; e.Findings != 0 || e.Blocking != 0 {
		t.Errorf("r2 counts = %d/%d", e.Findings, e.Blocking)
	}
	if e := byID["r3"]; e.Title != "" || e.Findings != 1 || e.Blocking != 1 {
		t.Errorf("r3 (untracked) entry = title %q findings %d blocking %d", e.Title, e.Findings, e.Blocking)
	}
}

func TestPRReviewStats(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	for _, r := range []Review{
		sampleReview("r1", "pr1", t0, 0),
		sampleReview("r2", "pr1", t0.Add(time.Hour), 0),
		sampleReview("r3", "pr2", t0, 0),
		sampleReview("r4", "pr3", t0, 0), // reviewed, no ledger rows
	} {
		if err := s.SaveReview(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	addCost(t, s, CostRow{At: t0, ReviewID: "r1", PRID: "pr1", CostUSD: 1.5})
	addCost(t, s, CostRow{At: t0, ReviewID: "r1", PRID: "pr1", CostUSD: 0.5}) // continuation of r1
	addCost(t, s, CostRow{At: t0, ReviewID: "r2", PRID: "pr1", CostUSD: 2})
	addCost(t, s, CostRow{At: t0, ReviewID: "r2", PRID: "pr1", CostUSD: 99, CostUnknown: true})
	addCost(t, s, CostRow{At: t0, ReviewID: "r3", PRID: "pr2", CostUSD: 3})
	addCost(t, s, CostRow{At: t0, ReviewID: "gone", PRID: "pr4", CostUSD: 4}) // cost, no review row

	got, err := s.PRReviewStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]PRStat{
		"pr1": {Reviews: 2, CostUSD: 4, Unknown: 1},
		"pr2": {Reviews: 1, CostUSD: 3},
		"pr3": {Reviews: 1, CostUSD: 0},
		"pr4": {Reviews: 0, CostUSD: 4},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s = %+v, want %+v", id, got[id], w)
		}
	}
}
