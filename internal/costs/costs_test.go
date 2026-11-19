package costs

import (
	"math"
	"testing"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/store"
)

// useNY makes time.Local America/New_York for the test.
func useNY(t *testing.T) *time.Location {
	t.Helper()
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	old := time.Local
	time.Local = ny
	t.Cleanup(func() { time.Local = old })
	return ny
}

func TestBucketStart(t *testing.T) {
	ny := useNY(t)
	// Wednesday 2026-11-18 15:42:10 New York, given as UTC.
	in := time.Date(2026, 11, 18, 15, 42, 10, 5, ny).UTC()
	cases := []struct {
		b    Bucket
		want time.Time
	}{
		{"hour", time.Date(2026, 11, 18, 15, 0, 0, 0, ny)},
		{"day", time.Date(2026, 11, 18, 0, 0, 0, 0, ny)},
		{"week", time.Date(2026, 11, 16, 0, 0, 0, 0, ny)}, // Monday
	}
	for _, c := range cases {
		if got := BucketStart(in, c.b); !got.Equal(c.want) {
			t.Errorf("%s: got %v, want %v", c.b, got, c.want)
		}
	}
	// A Sunday belongs to the week that began the Monday before.
	sun := time.Date(2026, 11, 22, 23, 0, 0, 0, ny)
	if got, want := BucketStart(sun, "week"), time.Date(2026, 11, 16, 0, 0, 0, 0, ny); !got.Equal(want) {
		t.Errorf("sunday: got %v, want %v", got, want)
	}
}

func TestBucketsAcrossDST(t *testing.T) {
	ny := useNY(t)
	// 2026-11-01 is the fall-back day: 25 hours long.
	late := time.Date(2026, 10, 31, 23, 30, 0, 0, ny)
	early := time.Date(2026, 11, 1, 0, 30, 0, 0, ny)
	after := time.Date(2026, 11, 1, 23, 30, 0, 0, ny) // 25h after midnight
	next := time.Date(2026, 11, 2, 0, 30, 0, 0, ny)
	rows := []store.CostRow{
		{At: late, ReviewID: "a", CostUSD: 1},
		{At: early, ReviewID: "b", CostUSD: 2},
		{At: after, ReviewID: "c", CostUSD: 4},
		{At: next, ReviewID: "d", CostUSD: 8},
	}
	q := Query{
		Filter: store.CostFilter{
			From: time.Date(2026, 10, 31, 0, 0, 0, 0, ny),
			To:   time.Date(2026, 11, 3, 0, 0, 0, 0, ny),
		},
		Bucket: "day",
	}
	series, _ := Aggregate(rows, q)
	if len(series) != 1 {
		t.Fatalf("series = %d", len(series))
	}
	pts := series[0].Points
	want := []float64{1, 6, 8}
	if len(pts) != len(want) {
		t.Fatalf("points = %d, want %d: %v", len(pts), len(want), pts)
	}
	for i, w := range want {
		if pts[i].USD != w {
			t.Errorf("day %d = %v, want %v", i, pts[i].USD, w)
		}
	}
	if d := pts[2].Start.Sub(pts[1].Start); d != 25*time.Hour {
		t.Errorf("nov 1 lasts %v, want 25h", d)
	}
	if d := pts[1].Start.Sub(pts[0].Start); d != 24*time.Hour {
		t.Errorf("oct 31 to nov 1 = %v", d)
	}
}

func TestAggregateGroupsAndFills(t *testing.T) {
	ny := useNY(t)
	d := func(day, h int) time.Time { return time.Date(2026, 11, day, h, 0, 0, 0, ny) }
	rows := []store.CostRow{
		{At: d(2, 9), ReviewID: "1", Model: "opus", CostUSD: 1},
		{At: d(2, 10), ReviewID: "2", Model: "sonnet", CostUSD: 2},
		{At: d(4, 10), ReviewID: "3", Model: "opus", CostUSD: 3},
		{At: d(4, 11), ReviewID: "4", Model: "haiku", CostUSD: 100, Outcome: "failed"},
	}
	q := Query{
		Filter: store.CostFilter{From: d(2, 0), To: d(5, 0), Outcomes: nil, Models: []string{"opus", "sonnet"}},
		Bucket: "day", GroupBy: "model",
	}
	series, sum := Aggregate(rows, q)
	if len(series) != 2 || series[0].Name != "opus" || series[1].Name != "sonnet" {
		t.Fatalf("series = %+v", series)
	}
	for _, s := range series {
		if len(s.Points) != 3 {
			t.Fatalf("%s has %d points", s.Name, len(s.Points))
		}
		for i := range s.Points {
			if !s.Points[i].Start.Equal(series[0].Points[i].Start) {
				t.Errorf("%s bucket %d start differs", s.Name, i)
			}
		}
	}
	if got := []float64{series[0].Points[0].USD, series[0].Points[1].USD, series[0].Points[2].USD}; got[0] != 1 || got[1] != 0 || got[2] != 3 {
		t.Errorf("opus = %v", got)
	}
	if series[1].Points[0].USD != 2 || series[1].Points[2].USD != 0 {
		t.Errorf("sonnet = %v", series[1].Points)
	}
	if sum.Total != 6 || sum.Reviews != 3 {
		t.Errorf("summary = %+v (haiku must be filtered out)", sum)
	}
}

func TestAggregateCumulative(t *testing.T) {
	ny := useNY(t)
	d := func(day int) time.Time { return time.Date(2026, 11, day, 12, 0, 0, 0, ny) }
	rows := []store.CostRow{
		{At: d(2), ReviewID: "1", CostUSD: 1},
		{At: d(4), ReviewID: "2", CostUSD: 2},
	}
	q := Query{
		Filter: store.CostFilter{From: time.Date(2026, 11, 2, 0, 0, 0, 0, ny), To: time.Date(2026, 11, 5, 0, 0, 0, 0, ny)},
		Bucket: "day", Cumulative: true,
	}
	series, sum := Aggregate(rows, q)
	var got []float64
	for _, p := range series[0].Points {
		got = append(got, p.USD)
	}
	want := []float64{1, 1, 3}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("cumulative = %v, want %v", got, want)
	}
	if sum.Total != 3 {
		t.Errorf("total = %v", sum.Total)
	}
}

func TestSummaryTopFiveAndTiers(t *testing.T) {
	ny := useNY(t)
	at := time.Date(2026, 11, 2, 12, 0, 0, 0, ny)
	var rows []store.CostRow
	// Seven PRs costing 1..7; pr-a and pr-b tie at 3 to test the PRID tie break.
	costs := map[string]float64{"pr-1": 1, "pr-2": 2, "pr-b": 3, "pr-a": 3, "pr-5": 5, "pr-6": 6, "pr-7": 7}
	i := 0
	for id, c := range costs {
		i++
		rows = append(rows, store.CostRow{At: at, ReviewID: "r" + id, PRID: id, Repo: "o/r", Tier: "first_pass", CostUSD: c})
	}
	rows = append(rows, store.CostRow{At: at, ReviewID: "rfu", PRID: "pr-7", Repo: "o/r", Tier: "follow_up", CostUSD: 0.5})
	q := Query{Filter: store.CostFilter{From: at.Add(-time.Hour), To: at.Add(time.Hour)}, Bucket: "hour"}
	_, sum := Aggregate(rows, q)
	if len(sum.Top) != 5 {
		t.Fatalf("top = %+v", sum.Top)
	}
	wantIDs := []string{"pr-7", "pr-6", "pr-5", "pr-a", "pr-b"}
	for i, w := range wantIDs {
		if sum.Top[i].PRID != w {
			t.Errorf("top[%d] = %s, want %s", i, sum.Top[i].PRID, w)
		}
	}
	if sum.Top[0].USD != 7.5 || sum.Top[0].Repo != "o/r" {
		t.Errorf("top[0] = %+v", sum.Top[0])
	}
	if sum.FirstPass != 27 || sum.FollowUp != 0.5 || sum.Total != 27.5 {
		t.Errorf("summary = %+v", sum)
	}
	if sum.Reviews != 8 || math.Abs(sum.AvgPerReview-27.5/8) > 1e-9 {
		t.Errorf("reviews/avg = %d/%v", sum.Reviews, sum.AvgPerReview)
	}
}

func TestSummaryCountsUnknownCost(t *testing.T) {
	ny := useNY(t)
	at := time.Date(2026, 11, 2, 12, 0, 0, 0, ny)
	rows := []store.CostRow{
		{At: at, ReviewID: "1", PRID: "p", Tier: "first_pass", CostUSD: 2},
		{At: at, ReviewID: "2", PRID: "p", Tier: "first_pass", CostUSD: 9, CostUnknown: true},
		{At: at, ReviewID: "3", PRID: "q", Tier: "follow_up", CostUSD: 9, CostUnknown: true},
	}
	q := Query{Filter: store.CostFilter{From: at.Add(-time.Hour), To: at.Add(time.Hour)}, Bucket: "hour"}
	series, sum := Aggregate(rows, q)
	if sum.Unknown != 2 || sum.Reviews != 3 {
		t.Errorf("unknown/reviews = %d/%d", sum.Unknown, sum.Reviews)
	}
	if sum.Total != 2 || sum.FirstPass != 2 || sum.FollowUp != 0 {
		t.Errorf("summary = %+v", sum)
	}
	// The average is over the one review with a known cost: the two with
	// none would pull it toward zero.
	if math.Abs(sum.AvgPerReview-2) > 1e-9 {
		t.Errorf("avg = %v, want 2", sum.AvgPerReview)
	}
	for _, p := range series[0].Points {
		if p.USD > 2 {
			t.Errorf("unknown cost leaked into series: %v", p)
		}
	}
	for _, tp := range sum.Top {
		if tp.USD > 2 {
			t.Errorf("unknown cost leaked into top: %+v", tp)
		}
	}
}

func TestAggregateEdgeCases(t *testing.T) {
	ny := useNY(t)
	at := time.Date(2026, 11, 2, 12, 0, 0, 0, ny)
	finite := func(t *testing.T, series []Series, sum Summary) {
		t.Helper()
		if series == nil {
			t.Error("series is nil")
		}
		for _, s := range series {
			if s.Points == nil {
				t.Errorf("%s: points nil", s.Name)
			}
			for _, p := range s.Points {
				if math.IsNaN(p.USD) || math.IsInf(p.USD, 0) {
					t.Errorf("%s: bad point %v", s.Name, p)
				}
			}
		}
		for _, v := range []float64{sum.Total, sum.AvgPerReview, sum.FirstPass, sum.FollowUp} {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Errorf("bad summary %+v", sum)
			}
		}
	}
	f := store.CostFilter{From: at.Add(-48 * time.Hour), To: at.Add(48 * time.Hour)}

	t.Run("zero rows", func(t *testing.T) {
		series, sum := Aggregate(nil, Query{Filter: f, Bucket: "day"})
		finite(t, series, sum)
		if sum.Total != 0 || sum.AvgPerReview != 0 || sum.Reviews != 0 {
			t.Errorf("sum = %+v", sum)
		}
		if len(series) != 1 || len(series[0].Points) == 0 {
			t.Errorf("want one zero-filled series, got %+v", series)
		}
	})
	t.Run("zero rows grouped and open range", func(t *testing.T) {
		series, sum := Aggregate(nil, Query{Bucket: "week", GroupBy: "repo"})
		finite(t, series, sum)
	})
	t.Run("one row", func(t *testing.T) {
		series, sum := Aggregate([]store.CostRow{{At: at, ReviewID: "1", CostUSD: 3}}, Query{Filter: f, Bucket: "day"})
		finite(t, series, sum)
		if sum.Total != 3 || sum.AvgPerReview != 3 {
			t.Errorf("sum = %+v", sum)
		}
	})
	t.Run("spike", func(t *testing.T) {
		rows := []store.CostRow{{At: at, ReviewID: "1", CostUSD: 1e9}, {At: at, ReviewID: "2", CostUSD: 0.01}}
		series, sum := Aggregate(rows, Query{Filter: f, Bucket: "day", Cumulative: true})
		finite(t, series, sum)
	})
	t.Run("open range uses the rows", func(t *testing.T) {
		series, _ := Aggregate([]store.CostRow{{At: at, ReviewID: "1", CostUSD: 3}}, Query{Bucket: "day"})
		if len(series) != 1 || len(series[0].Points) != 1 || series[0].Points[0].USD != 3 {
			t.Errorf("series = %+v", series)
		}
	})
}

func TestGroupsFoldIntoOther(t *testing.T) {
	ny := useNY(t)
	at := time.Date(2026, 11, 2, 12, 0, 0, 0, ny)
	run := func(authors []string) ([]string, []Series, Summary) {
		var rows []store.CostRow
		for i, a := range authors {
			rows = append(rows, store.CostRow{At: at, ReviewID: a, Author: a, CostUSD: float64(i + 1)})
		}
		q := Query{Filter: store.CostFilter{From: at.Add(-time.Hour), To: at.Add(time.Hour)}, Bucket: "day", GroupBy: "author"}
		series, sum := Aggregate(rows, q)
		var names []string
		for _, s := range series {
			names = append(names, s.Name)
		}
		return names, series, sum
	}
	same := func(t *testing.T, got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("names = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("names = %v, want %v", got, want)
			}
		}
	}

	t.Run("eight groups", func(t *testing.T) {
		// Authors a..h cost 1..8; the three cheapest (a, b, c) fold into other.
		names, series, sum := run([]string{"a", "b", "c", "d", "e", "f", "g", "h"})
		same(t, names, []string{"d", "e", "f", "g", "h", "other"})
		if got := series[5].Points[0].USD; got != 6 {
			t.Errorf("other = %v, want 6", got)
		}
		if sum.Total != 36 {
			t.Errorf("total = %v", sum.Total)
		}
	})
	t.Run("exactly six groups", func(t *testing.T) {
		names, _, _ := run([]string{"a", "b", "c", "d", "e", "f"})
		same(t, names, []string{"a", "b", "c", "d", "e", "f"})
	})
}

// A review with a known run and an unknown one counts toward the average;
// one with only unknown runs does not.
func TestAvgPerReviewCountsReviewsWithAKnownRun(t *testing.T) {
	at := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	rows := []store.CostRow{
		{At: at, ReviewID: "1", PRID: "p", CostUSD: 3},
		{At: at, ReviewID: "1", PRID: "p", CostUnknown: true},
		{At: at, ReviewID: "2", PRID: "p", CostUSD: 0}, // a real $0
		{At: at, ReviewID: "3", PRID: "p", CostUnknown: true},
	}
	_, sum := Aggregate(rows, Query{Filter: store.CostFilter{From: at.Add(-time.Hour), To: at.Add(time.Hour)}, Bucket: "hour"})
	if sum.Reviews != 3 || math.Abs(sum.AvgPerReview-1.5) > 1e-9 {
		t.Errorf("reviews/avg = %d/%v, want 3/1.5", sum.Reviews, sum.AvgPerReview)
	}
	_, sum = Aggregate(rows[3:], Query{Filter: store.CostFilter{From: at.Add(-time.Hour), To: at.Add(time.Hour)}, Bucket: "hour"})
	if sum.AvgPerReview != 0 || math.IsNaN(sum.AvgPerReview) {
		t.Errorf("all unknown: avg = %v, want 0", sum.AvgPerReview)
	}
}
