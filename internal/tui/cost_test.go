package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/costs"
	"github.com/chrisdaly-3/jawnsnow/internal/review"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
)

// useUTC makes time.Local UTC for the test, so buckets and the preset
// ranges don't depend on the machine's zone.
func useUTC(t *testing.T) {
	t.Helper()
	old := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = old })
}

// today is local midnight of t0's day.
var today = time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)

// spend is one ledger row daysAgo days before t0, at hour:00.
func spend(daysAgo, hour int, review, pr, repo, author, model, tier string, usd float64) store.CostRow {
	return store.CostRow{
		At: today.AddDate(0, 0, -daysAgo).Add(time.Duration(hour) * time.Hour), ReviewID: review,
		PRID: pr, Repo: repo, Author: author, Model: model, Effort: "high", Tier: tier,
		Mode: "manual", Outcome: "complete", CostUSD: usd,
	}
}

// costFixture is three PRs' spend over the last three weeks, one run of
// which reported no cost.
func costFixture() ([]store.CostRow, []store.Review) {
	rows := []store.CostRow{
		spend(20, 10, "r1", "PR_a", "acme/widgets", "alice", "opus", "first_pass", 4.20),
		spend(14, 9, "r2", "PR_b", "acme/gadgets", "bob", "sonnet", "first_pass", 1.10),
		spend(9, 16, "r3", "PR_a", "acme/widgets", "alice", "opus", "follow_up", 2.40),
		spend(6, 11, "r4", "PR_c", "acme/widgets", "carol", "sonnet", "first_pass", 0.85),
		spend(3, 14, "r5", "PR_b", "acme/gadgets", "bob", "opus", "follow_up", 3.05),
		spend(1, 13, "r6", "PR_c", "acme/widgets", "carol", "sonnet", "follow_up", 0.60),
		spend(0, 9, "r7", "PR_a", "acme/widgets", "alice", "opus", "follow_up", 1.75),
	}
	unknown := spend(2, 12, "r8", "PR_b", "acme/gadgets", "bob", "sonnet", "follow_up", 0)
	unknown.CostUnknown = true
	rows = append(rows, unknown)
	reviews := []store.Review{
		{ID: "r1", PRID: "PR_a", Repo: "acme/widgets", Number: 12, Author: "alice", Outcome: "complete", StartedAt: rows[0].At},
		{ID: "r2", PRID: "PR_b", Repo: "acme/gadgets", Number: 7, Author: "bob", Outcome: "complete", StartedAt: rows[1].At},
		{ID: "r4", PRID: "PR_c", Repo: "acme/widgets", Number: 31, Author: "carol", Outcome: "complete", StartedAt: rows[3].At},
	}
	return rows, reviews
}

func seedCost(t *testing.T, st *store.Store, rows []store.CostRow, reviews []store.Review) {
	t.Helper()
	ctx := context.Background()
	for _, r := range rows {
		if err := st.AddCost(ctx, r); err != nil {
			t.Fatalf("AddCost: %v", err)
		}
	}
	for _, r := range reviews {
		if err := st.SaveReview(ctx, r); err != nil {
			t.Fatalf("SaveReview: %v", err)
		}
	}
}

// costHarness is a harness over the cost fixture, sized width×height and
// not yet on the Cost view.
func costHarness(t *testing.T, o opts, width, height int, seed bool) *harness {
	t.Helper()
	useUTC(t)
	h := newHarness(t, o)
	h.m, _ = update(t, h.m, tea.WindowSizeMsg{Width: width, Height: height})
	if seed {
		rows, reviews := costFixture()
		seedCost(t, h.store, rows, reviews)
	}
	return h
}

// costLoads runs cmd and returns the Cost view loads it produced.
func costLoads(t *testing.T, cmd tea.Cmd) []costLoadedMsg {
	t.Helper()
	msgs, _ := runCmd(t, cmd)
	var out []costLoadedMsg
	for _, m := range msgs {
		if l, ok := m.(costLoadedMsg); ok {
			out = append(out, l)
		}
	}
	return out
}

func stripped(m Model) string { return ansi.Strip(content(m)) }

func TestCostViewGolden(t *testing.T) {
	h := costHarness(t, opts{}, 120, 30, true)
	h.press(t, "4", "g")
	golden(t, "cost.golden", content(h.m))
}

func TestCostViewSubscriptionLabel(t *testing.T) {
	const label = "≈ API-equivalent cost (subscription login)"
	sub := review.ClaudeInfo{Found: true, LoggedIn: true, AuthMethod: "claude.ai"}
	h := costHarness(t, opts{claude: sub}, 120, 30, true)
	h.press(t, "4")
	if got := stripped(h.m); !strings.Contains(got, "Cost · 30d") || !strings.Contains(got, label) {
		t.Errorf("a subscription login doesn't label the cost:\n%s", got)
	}

	key := review.ClaudeInfo{Found: true, LoggedIn: true, AuthMethod: "api_key"}
	h = costHarness(t, opts{claude: key}, 120, 30, true)
	h.press(t, "4")
	if got := stripped(h.m); strings.Contains(got, label) {
		t.Errorf("an API key login is labelled as a subscription:\n%s", got)
	}
}

func TestCostViewRangeReloads(t *testing.T) {
	h := costHarness(t, opts{}, 120, 30, true)
	old := spend(60, 10, "r0", "PR_z", "acme/old", "zed", "opus", "first_pass", 50)
	seedCost(t, h.store, []store.CostRow{old}, nil)

	m, enter := update(t, h.m, key("4"))
	loads := costLoads(t, enter)
	if len(loads) != 1 {
		t.Fatalf("entering the Cost view made %d loads, want 1", len(loads))
	}
	if want := today.AddDate(0, 0, -29); !loads[0].From.Equal(want) || !loads[0].To.Equal(today.AddDate(0, 0, 1)) {
		t.Errorf("30d loads [%v, %v), want [%v, %v)", loads[0].From, loads[0].To, want, today.AddDate(0, 0, 1))
	}
	stale := loads[0]

	m, cmd := update(t, m, key("r"))
	loads = costLoads(t, cmd)
	if len(loads) != 1 {
		t.Fatalf("r made %d loads, want 1", len(loads))
	}
	if want := today.AddDate(0, 0, -89); !loads[0].From.Equal(want) {
		t.Errorf("90d loads from %v, want %v", loads[0].From, want)
	}
	m, _ = settle(t, m, loads[0])
	if got := stripped(m); !strings.Contains(got, "Cost · 90d") || !strings.Contains(got, "Total $63.95") {
		t.Errorf("90d doesn't include the 60-day-old run:\n%s", got)
	}

	// The 30d load lands late; the 90d range is still selected, so it's dropped.
	m, _ = settle(t, m, stale)
	if got := stripped(m); !strings.Contains(got, "Total $63.95") {
		t.Errorf("a stale load replaced the 90d rows:\n%s", got)
	}
}

func TestCostViewCustomRangeInput(t *testing.T) {
	h := costHarness(t, opts{}, 120, 30, true)
	h.press(t, "4", "r", "r") // 30d → 90d → custom
	if !h.m.cost.typing {
		t.Fatal("r from 90d did not open the custom range input")
	}
	from := h.m.cost.from

	for _, bad := range []string{"q", "2026-13-01..2026-11-07", "2026-11-07..2026-11-01"} {
		h.m, _ = update(t, h.m, ctrl('u'))
		for _, r := range bad {
			var cmd tea.Cmd
			h.m, cmd = update(t, h.m, key(string(r)))
			if isQuit(t, cmd) {
				t.Fatalf("typing %q in the range input quit", r)
			}
		}
		var cmd tea.Cmd
		h.m, cmd = update(t, h.m, key("enter"))
		if loads := costLoads(t, cmd); len(loads) != 0 {
			t.Errorf("%q loaded %v", bad, loads)
		}
		if got := stripped(h.m); !strings.Contains(got, "invalid range") {
			t.Errorf("%q isn't flagged inline:\n%s", bad, got)
		}
		if !h.m.cost.from.Equal(from) {
			t.Errorf("%q changed the range to %v", bad, h.m.cost.from)
		}
	}

	h.m, _ = update(t, h.m, ctrl('u'))
	for _, r := range "2026-11-10..2026-11-14" {
		h.m, _ = update(t, h.m, key(string(r)))
	}
	m, cmd := update(t, h.m, key("enter"))
	loads := costLoads(t, cmd)
	if len(loads) != 1 {
		t.Fatalf("a valid range made %d loads, want 1", len(loads))
	}
	wantFrom, wantTo := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	if !loads[0].From.Equal(wantFrom) || !loads[0].To.Equal(wantTo) {
		t.Errorf("custom loads [%v, %v), want [%v, %v)", loads[0].From, loads[0].To, wantFrom, wantTo)
	}
	m, _ = settle(t, m, loads[0])
	got := stripped(m)
	if m.cost.typing || !strings.Contains(got, "Cost · 2026-11-10..2026-11-14") || strings.Contains(got, "invalid range") {
		t.Errorf("the custom range isn't applied:\n%s", got)
	}
	// r4 (Nov 12) is the only run in that week.
	if !strings.Contains(got, "Total $0.85") {
		t.Errorf("custom range total is wrong:\n%s", got)
	}
}

func TestCostViewGroupBucketToggle(t *testing.T) {
	h := costHarness(t, opts{}, 120, 30, true)
	h.press(t, "4")
	if h.m.cost.bucket != costs.Day || h.m.cost.group != costs.ByNone || h.m.cost.cumulative {
		t.Fatalf("defaults: bucket %q group %q cumulative %v", h.m.cost.bucket, h.m.cost.group, h.m.cost.cumulative)
	}

	h.press(t, "g")
	if h.m.cost.group != costs.ByModel || len(h.m.cost.series) != 2 {
		t.Errorf("g: group %q with %d series, want model with 2", h.m.cost.group, len(h.m.cost.series))
	}
	if got := stripped(h.m); !strings.Contains(got, "■ opus") || !strings.Contains(got, "■ sonnet") {
		t.Errorf("grouped by model, the legend is missing a model:\n%s", got)
	}
	for _, want := range []costs.GroupBy{costs.ByAuthor, costs.ByRepo, costs.ByTier, costs.ByNone} {
		h.press(t, "g")
		if h.m.cost.group != want {
			t.Errorf("g: group %q, want %q", h.m.cost.group, want)
		}
	}

	// 30d is too long for hourly buckets, so b skips hour.
	h.press(t, "b")
	if h.m.cost.bucket != costs.Week {
		t.Errorf("b: bucket %q, want week", h.m.cost.bucket)
	}
	h.press(t, "b")
	if h.m.cost.bucket != costs.Day {
		t.Errorf("b over 30d: bucket %q, want day (hour skipped)", h.m.cost.bucket)
	}

	h.press(t, "r", "r", "esc", "r") // 90d, custom (cancelled), 7d
	if got := stripped(h.m); !strings.Contains(got, "Cost · 7d") {
		t.Fatalf("not on 7d:\n%s", got)
	}
	h.press(t, "b", "b")
	if h.m.cost.bucket != costs.Hour {
		t.Errorf("b b over 7d: bucket %q, want hour", h.m.cost.bucket)
	}
	h.press(t, "r") // 30d again: hour falls back to day
	if h.m.cost.bucket != costs.Day {
		t.Errorf("30d kept bucket %q, want day", h.m.cost.bucket)
	}

	h.press(t, "t")
	if !h.m.cost.cumulative || !strings.Contains(stripped(h.m), "running total") {
		t.Errorf("t did not switch to a running total:\n%s", stripped(h.m))
	}
	pts := h.m.cost.series[0].Points
	if last := pts[len(pts)-1].USD; last < 13.95-1e-9 || last > 13.95+1e-9 {
		t.Errorf("running total ends at %v, want 13.95", last)
	}
	h.press(t, "t")
	if h.m.cost.cumulative {
		t.Error("t did not switch back to per bucket")
	}

	// The cursor starts on today and h moves it back a day.
	if got := stripped(h.m); !strings.Contains(got, "Nov 18 · ■ total $1.75") {
		t.Errorf("the cursor readout isn't on today:\n%s", got)
	}
	h.press(t, "h")
	if got := stripped(h.m); !strings.Contains(got, "Nov 17 · ■ total $0.60") {
		t.Errorf("h did not move the cursor back a day:\n%s", got)
	}
	h.press(t, "l", "l")
	if got := stripped(h.m); !strings.Contains(got, "Nov 18 ·") {
		t.Errorf("l moved the cursor past the last bucket:\n%s", got)
	}
}

func TestCostViewFilterPicker(t *testing.T) {
	h := costHarness(t, opts{}, 120, 30, true)
	h.press(t, "4", "f")
	if !h.m.cost.picking {
		t.Fatal("f did not open the filter picker")
	}
	// The first dimension is author: alice, bob, carol. Pick bob.
	h.press(t, "j", " ")
	if got := stripped(h.m); !strings.Contains(got, "[x] bob") {
		t.Errorf("space did not select bob:\n%s", got)
	}
	h.press(t, "esc")
	if h.m.cost.picking {
		t.Fatal("esc did not close the picker")
	}
	got := stripped(h.m)
	if !strings.Contains(got, "author=bob") || !strings.Contains(got, "Total $4.15") {
		t.Errorf("the author filter isn't applied:\n%s", got)
	}
}

func TestCostViewTopPROpensReviews(t *testing.T) {
	h := costHarness(t, opts{}, 120, 30, true)
	h.press(t, "4")
	if n := h.m.paneCount(); n != 3 {
		t.Errorf("the Cost view has %d panes, want 3", n)
	}
	got := stripped(h.m)
	a, b := strings.Index(got, "acme/widgets#12"), strings.Index(got, "acme/gadgets#7")
	if a < 0 || b < 0 || a > b || !strings.Contains(got, "acme/widgets#31") {
		t.Fatalf("top PRs aren't listed by spend:\n%s", got)
	}
	h.press(t, "j", "enter")
	if h.m.view != ViewReviews {
		t.Errorf("enter switched to %v, want Reviews", h.m.view)
	}
	if want := (reviewsFilter{PRID: "PR_b", Label: "acme/gadgets#7"}); h.m.pendingReviewsFilter != want {
		t.Errorf("pending Reviews filter = %+v, want %+v", h.m.pendingReviewsFilter, want)
	}
}

func TestCostViewEmpty(t *testing.T) {
	h := costHarness(t, opts{}, 120, 30, false)
	h.press(t, "4")
	if got := stripped(h.m); !strings.Contains(got, "No review spend in this range") {
		t.Errorf("no empty state:\n%s", got)
	}
}

func TestCostViewResolvesTopPRsAFilterBringsIn(t *testing.T) {
	h := costHarness(t, opts{}, 120, 30, false)
	var rows []store.CostRow
	var reviews []store.Review
	for i := 1; i <= 6; i++ {
		id, pr, who := "r"+string(rune('0'+i)), "PR_"+string(rune('0'+i)), "dev"+string(rune('0'+i))
		rows = append(rows, spend(i, 10, id, pr, "acme/p", who, "opus", "first_pass", float64(10-i)))
		reviews = append(reviews, store.Review{ID: id, PRID: pr, Repo: "acme/p", Number: 100 + i, Outcome: "complete", StartedAt: rows[i-1].At})
	}
	seedCost(t, h.store, rows, reviews)
	h.press(t, "4")
	if _, asked := h.m.cost.labels["PR_6"]; asked || len(h.m.cost.labels) != 5 {
		t.Errorf("the load looked up %v, want only the top 5", h.m.cost.labels)
	}
	// Filter to dev6, whose PR wasn't in the top 5.
	h.press(t, "f", "j", "j", "j", "j", "j", " ", "esc")
	if got := stripped(h.m); !strings.Contains(got, "author=dev6") || !strings.Contains(got, "acme/p#106") {
		t.Errorf("the filtered top PR isn't labelled:\n%s", got)
	}
}

func TestCostViewTinyWindowDoesNotPanic(t *testing.T) {
	h := costHarness(t, opts{}, 120, 30, true)
	h.press(t, "4")
	for _, size := range []tea.WindowSizeMsg{{Width: 0, Height: 0}, {Width: 10, Height: 3}, {Width: 30, Height: 8}, {Width: 50, Height: 5}} {
		m, _ := update(t, h.m, size)
		_ = content(m)
		m, _ = settle(t, m, key("f"))
		_ = content(m)
	}
}

func TestFooterKeepsGlobalKeys(t *testing.T) {
	h := costHarness(t, opts{}, 120, 30, true)
	for v := range viewCount {
		h.m = h.m.switchView(v)
		for _, w := range []int{80, 100, 120} {
			got := ansi.Strip(h.m.footer(w))
			if !strings.Contains(got, "? help") || !strings.Contains(got, "q quit") {
				t.Errorf("%v at %d cols hides the global keys: %q", v, w, got)
			}
			if lw := ansi.StringWidth(got); lw > w {
				t.Errorf("%v at %d cols: footer is %d wide", v, w, lw)
			}
		}
	}
	// A view's hints that don't fit are dropped whole, marked with "…".
	h.m = h.m.switchView(ViewWall)
	if got := ansi.Strip(h.m.footer(80)); !strings.Contains(got, "… · [ ] views") || strings.Contains(got, "author") {
		t.Errorf("the Wall's footer at 80 cols doesn't drop whole hints: %q", got)
	}
	h.m = h.m.switchView(ViewCost)
	want := " r range · f filter · g group · b bucket · t total · enter reviews · [ ] views · R poll · m mode · ? help · q quit"
	if got := ansi.Strip(h.m.footer(120)); got != want {
		t.Errorf("Cost footer at 120 cols:\n got %q\nwant %q", got, want)
	}
}

func TestCostFooterShowsModeKeys(t *testing.T) {
	h := costHarness(t, opts{}, 120, 30, true)
	h.press(t, "4", "f")
	if got := ansi.Strip(h.m.footer(120)); !strings.Contains(got, "space toggle · esc close") || strings.Contains(got, "g group") {
		t.Errorf("the picker's footer: %q", got)
	}
	h.press(t, "esc", "r", "r")
	if got := ansi.Strip(h.m.footer(120)); !strings.Contains(got, "enter apply · esc cancel") || strings.Contains(got, "g group") {
		t.Errorf("the range input's footer: %q", got)
	}
}

// enterRange types s into the custom range input, already open, and
// presses enter, returning the loads that produced.
func enterRange(t *testing.T, h *harness, s string) []costLoadedMsg {
	t.Helper()
	h.m, _ = update(t, h.m, ctrl('u'))
	for _, r := range s {
		h.m, _ = update(t, h.m, key(string(r)))
	}
	var cmd tea.Cmd
	h.m, cmd = update(t, h.m, key("enter"))
	return costLoads(t, cmd)
}

func TestCostViewCustomRangeEdges(t *testing.T) {
	day := func(y int, mo time.Month, d int) time.Time { return time.Date(y, mo, d, 0, 0, 0, 0, time.UTC) }
	cases := []struct {
		in       string
		from, to time.Time // zero when refused
		note     string
	}{
		{in: "2026-11-12..2026-11-12", from: day(2026, 11, 12), to: day(2026, 11, 13)},
		{in: "  2026-11-10 .. 2026-11-14  ", from: day(2026, 11, 10), to: day(2026, 11, 15)},
		{in: "2024-11-18..2026-11-18", from: day(2024, 11, 18), to: day(2026, 11, 19)}, // 731 days
		{in: "2024-11-17..2026-11-18", note: "range can be at most 2 years"},
	}
	for _, c := range cases {
		h := costHarness(t, opts{}, 120, 30, true)
		h.press(t, "4", "r", "r")
		loads := enterRange(t, h, c.in)
		if c.note != "" {
			if len(loads) != 0 || !strings.Contains(stripped(h.m), c.note) {
				t.Errorf("%q: loads %v, want none and %q inline:\n%s", c.in, loads, c.note, stripped(h.m))
			}
			continue
		}
		if len(loads) != 1 || !loads[0].From.Equal(c.from) || !loads[0].To.Equal(c.to) {
			t.Errorf("%q: loads %v, want [%v, %v)", c.in, loads, c.from, c.to)
		}
	}
}

func TestCostViewPluralUnknownRuns(t *testing.T) {
	h := costHarness(t, opts{}, 120, 30, true)
	more := spend(4, 12, "r9", "PR_c", "acme/widgets", "carol", "sonnet", "follow_up", 0)
	more.CostUnknown = true
	seedCost(t, h.store, []store.CostRow{more}, nil)
	h.press(t, "4")
	if got := stripped(h.m); !strings.Contains(got, "Total $13.95 · 2 runs cost unknown") {
		t.Errorf("two unknown runs aren't counted:\n%s", got)
	}
}
