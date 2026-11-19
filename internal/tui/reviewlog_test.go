package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
)

// logPRs are the two PRs the review log fixture's reviews belong to.
func logPRs() []tracker.Tracked {
	a, b := wallPR("1"), wallPR("22")
	a.PR.Title = "Fix the widget cache"
	b.PR.Repo, b.PR.Number, b.PR.Title = "acme/gadgets", 22, "Tune the gadget\x1b[31m loop"
	return []tracker.Tracked{a, b}
}

// logReviews is four reviews on two PRs, newest first: r3, r4, r1, r2.
func logReviews() []store.Review {
	return []store.Review{
		{ID: "r1", PRID: "1", Repo: "acme/widgets", Number: 1, Author: "sam", Tier: "first_pass", Model: "opus", Effort: "high",
			Outcome: "complete", Verdict: "comment", CostUSD: 1.5, StartedAt: t0.Add(-3 * time.Hour),
			Findings: []store.Finding{{Path: "a.go", Line: 1, Body: "one", Blocking: true}, {Path: "b.go", Line: 2, Body: "two"}}},
		{ID: "r2", PRID: "1", Repo: "acme/widgets", Number: 1, Author: "sam", Tier: "follow_up", Model: "sonnet", Effort: "medium",
			Outcome: "complete", Verdict: "approve", CostUSD: 0.4, StartedAt: t0.Add(-49 * time.Hour)},
		{ID: "r3", PRID: "22", Repo: "acme/gadgets", Number: 22, Author: "kim", Tier: "first_pass", Model: "opus", Effort: "high",
			Outcome: "failed", CostUnknown: true, StartedAt: t0.Add(-30 * time.Minute)},
		{ID: "r4", PRID: "22", Repo: "acme/gadgets", Number: 22, Author: "kim", Tier: "first_pass", Model: "sonnet", Effort: "low",
			Outcome: "incomplete", Verdict: "request_changes", CostUSD: 12.5, StartedAt: t0.Add(-time.Hour)},
	}
}

func logHarness(t *testing.T, width, height int, seed bool) *harness {
	t.Helper()
	useUTC(t)
	h := newHarness(t, opts{}, logPRs()...)
	h.m, _ = update(t, h.m, tea.WindowSizeMsg{Width: width, Height: height})
	if seed {
		ctx := context.Background()
		for _, r := range logReviews() {
			if err := h.store.SaveReview(ctx, r); err != nil {
				t.Fatalf("SaveReview: %v", err)
			}
		}
		post := store.Posted{ReviewID: "r1", Event: "COMMENT", At: t0}
		if err := h.store.RecordPost(ctx, post, nil); err != nil {
			t.Fatalf("RecordPost: %v", err)
		}
	}
	return h
}

// logLoads runs cmd and returns the review log loads it produced.
func logLoads(t *testing.T, cmd tea.Cmd) []reviewLogLoadedMsg {
	t.Helper()
	msgs, _ := runCmd(t, cmd)
	var out []reviewLogLoadedMsg
	for _, m := range msgs {
		if l, ok := m.(reviewLogLoadedMsg); ok {
			out = append(out, l)
		}
	}
	return out
}

func TestReviewLogGolden(t *testing.T) {
	h := logHarness(t, 140, 24, true)
	h.press(t, "3")
	golden(t, "reviewlog.golden", content(h.m))
}

func TestReviewLogFilters(t *testing.T) {
	h := logHarness(t, 120, 30, true)
	h.press(t, "3")
	rows := func() []string {
		var ids []string
		for _, e := range h.m.reviewLog.entries {
			ids = append(ids, e.Review.ID)
		}
		return ids
	}
	if got := strings.Join(rows(), ","); got != "r3,r4,r1,r2" {
		t.Fatalf("rows = %s, want newest first r3,r4,r1,r2", got)
	}
	if got := stripped(h.m); !strings.Contains(got, "Tune the gadget loop") || strings.Contains(got, "\x1b") {
		t.Errorf("the title isn't cleaned:\n%s", got)
	}

	// o cycles all → complete → incomplete → failed → all, one load each.
	for _, want := range []string{"r1,r2", "r4", "r3", "r3,r4,r1,r2"} {
		var cmd tea.Cmd
		h.m, cmd = update(t, h.m, key("o"))
		if loads := logLoads(t, cmd); len(loads) != 1 {
			t.Fatalf("o made %d loads, want 1", len(loads))
		}
		h.m, _ = settle(t, h.m, logLoads(t, cmd)[0])
		if got := strings.Join(rows(), ","); got != want {
			t.Errorf("after o rows = %s, want %s", got, want)
		}
	}

	// / opens the input; keys, including q, go to it; the footer shows only
	// enter and esc.
	h.press(t, "/")
	if !h.m.reviewLog.typing {
		t.Fatal("/ did not open the filter input")
	}
	var cmd tea.Cmd
	for _, r := range "gadg" {
		h.m, cmd = update(t, h.m, key(string(r)))
		if isQuit(t, cmd) {
			t.Fatalf("typing %q quit", r)
		}
	}
	if foot := ansi.Strip(h.m.footer(120)); strings.Contains(foot, "outcome") || !strings.Contains(foot, "enter apply") || !strings.Contains(foot, "esc cancel") {
		t.Errorf("typing footer = %q", foot)
	}
	h.m, cmd = update(t, h.m, key("enter"))
	loads := logLoads(t, cmd)
	if len(loads) != 1 {
		t.Fatalf("enter made %d loads, want 1", len(loads))
	}
	h.m, _ = settle(t, h.m, loads[0])
	if got := strings.Join(rows(), ","); got != "r3,r4" {
		t.Errorf("text filter rows = %s, want r3,r4", got)
	}

	// A load for a filter no longer selected is dropped.
	stale := loads[0]
	h.m, cmd = update(t, h.m, key("o"))
	fresh := logLoads(t, cmd)
	h.m, _ = settle(t, h.m, fresh[0])
	h.m, _ = settle(t, h.m, reviewLogLoadedMsg{Filter: store.LogFilter{Text: "gadg"}, Entries: stale.Entries})
	if len(h.m.reviewLog.entries) != len(fresh[0].Entries) || h.m.reviewLog.shown.Outcome != "complete" {
		t.Errorf("a stale load replaced the rows: %d rows shown for %+v", len(h.m.reviewLog.entries), h.m.reviewLog.shown)
	}

	// esc clears every filter and reloads.
	h.m, cmd = update(t, h.m, key("esc"))
	loads = logLoads(t, cmd)
	if len(loads) != 1 || loads[0].Filter != (store.LogFilter{}) {
		t.Fatalf("esc loads = %+v, want one unfiltered load", loads)
	}

	// esc while typing cancels the input and keeps the filter.
	h.press(t, "/")
	h.m, cmd = update(t, h.m, key("esc"))
	if h.m.reviewLog.typing || len(logLoads(t, cmd)) != 0 {
		t.Errorf("esc in the input: typing=%v, loads=%d", h.m.reviewLog.typing, len(logLoads(t, cmd)))
	}
}

func TestReviewLogOpensDrawerAtPass(t *testing.T) {
	h := logHarness(t, 120, 30, true)
	h.press(t, "3", "j", "j", "j") // r3, r4, r1, r2
	h.press(t, "enter")
	if !h.m.drawer.isOpen() || h.m.drawer.prID != "1" {
		t.Fatalf("drawer open=%v on %q, want PR 1", h.m.drawer.isOpen(), h.m.drawer.prID)
	}
	if r := h.m.drawer.current(); r == nil || r.ID != "r2" {
		t.Errorf("drawer shows %+v, want the older pass r2", r)
	}
	if h.m.drawerView != ViewReviews {
		t.Errorf("drawerView = %v, want Reviews", h.m.drawerView)
	}
	if n := h.m.paneCount(); n != 2 {
		t.Errorf("panes with the drawer = %d, want 2", n)
	}

	// A review that isn't in the drawer's PR leaves the default pass.
	d, _ := newDrawer().showReview("1", "nope", 120)
	d = d.loadedReviews(drawerLoadedMsg{PRID: "1", Seq: 1, Reviews: []store.Review{{ID: "r1"}, {ID: "r2"}}})
	if d.pass != 0 || d.wantReview != "" {
		t.Errorf("pass = %d, want = %q; want the default pass and no pending want", d.pass, d.wantReview)
	}
}

func TestReviewLogPRFilterFromCost(t *testing.T) {
	h := costHarness(t, opts{}, 120, 30, true)
	h.press(t, "4", "j", "enter")
	if h.m.view != ViewReviews {
		t.Fatalf("view = %v, want Reviews", h.m.view)
	}
	if h.m.pendingReviewsFilter != (reviewsFilter{}) {
		t.Errorf("the pending filter was not cleared: %+v", h.m.pendingReviewsFilter)
	}
	got := stripped(h.m)
	if !strings.Contains(got, "acme/gadgets#7") || strings.Contains(got, "acme/widgets#12") {
		t.Errorf("the PR filter isn't applied:\n%s", got)
	}
	if h.m.reviewLog.prID != "PR_b" || h.m.reviewLog.label != "acme/gadgets#7" {
		t.Errorf("chip = %q %q", h.m.reviewLog.prID, h.m.reviewLog.label)
	}

	h.press(t, "esc")
	got = stripped(h.m)
	if !strings.Contains(got, "acme/widgets#12") || !strings.Contains(got, "acme/gadgets#7") {
		t.Errorf("esc did not clear the chip:\n%s", got)
	}
}

func TestReviewLogEmpty(t *testing.T) {
	h := logHarness(t, 120, 30, false)
	h.press(t, "3")
	if got := stripped(h.m); !strings.Contains(got, "No Claude reviews yet") {
		t.Errorf("empty state missing:\n%s", got)
	}
	h.press(t, "o")
	if got := stripped(h.m); !strings.Contains(got, "No reviews match") || strings.Contains(got, "No Claude reviews yet") {
		t.Errorf("a filter with no matches should say so:\n%s", got)
	}
}

func TestReviewLogNarrowDropsColumns(t *testing.T) {
	for _, w := range []int{40, 60, 80, 100} {
		h := logHarness(t, w, 20, true)
		h.press(t, "3")
		got := stripped(h.m)
		for _, l := range strings.Split(got, "\n") {
			if ansi.StringWidth(l) > w {
				t.Errorf("width %d: line %q is %d wide", w, l, ansi.StringWidth(l))
			}
		}
		if !strings.Contains(got, "acme/gadgets#22") || !strings.Contains(got, "failed") {
			t.Errorf("width %d lost a key column:\n%s", w, got)
		}
	}
}

// A review finishing while the Reviews view is open adds its row without
// moving the cursor off the review you were on.
func TestReviewLogReloadsAfterReviewAndPost(t *testing.T) {
	h := logHarness(t, 140, 24, true)
	h.press(t, "3", "j") // r3, r4, ...
	if e := h.m.reviewLog.entries[h.m.reviewLog.at()]; e.Review.ID != "r4" {
		t.Fatalf("cursor on %q, want r4", e.Review.ID)
	}

	ctx := context.Background()
	if err := h.store.SaveReview(ctx, store.Review{ID: "r5", PRID: "1", Repo: "acme/widgets", Number: 1, Outcome: "complete", StartedAt: t0}); err != nil {
		t.Fatal(err)
	}
	h.m, _ = settle(t, h.m, reviewDoneMsg{PRID: "1", Review: store.Review{ID: "r5", PRID: "1"}})
	r := h.m.reviewLog
	if len(r.entries) != 5 || r.entries[0].Review.ID != "r5" {
		t.Fatalf("entries after a review = %d, first %q; want 5 starting r5", len(r.entries), r.entries[0].Review.ID)
	}
	if got := r.entries[r.at()].Review.ID; got != "r4" {
		t.Errorf("cursor moved to %q, want it kept on r4", got)
	}

	p := store.Posted{ReviewID: "r4", GitHubReviewID: "g1", At: t0}
	if err := h.store.RecordPost(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	h.m, _ = settle(t, h.m, postDoneMsg{ReviewID: "r4", PRID: "22", Posted: p})
	r = h.m.reviewLog
	if !r.posted["r4"] {
		t.Errorf("r4 not marked posted after the post: %v", r.posted)
	}
	if got := r.entries[r.at()].Review.ID; got != "r4" {
		t.Errorf("cursor moved to %q after the post, want r4", got)
	}

	// Another view doesn't pay for the reload.
	h.press(t, "1")
	_, cmd := update(t, h.m, reviewDoneMsg{PRID: "1", Review: store.Review{ID: "r5", PRID: "1"}})
	if n := len(logLoads(t, cmd)); n != 0 {
		t.Errorf("a review finishing outside the Reviews view reloaded its log %d times", n)
	}
}

// Two loads with the same filter can land out of order; the older one
// must not replace the newer.
func TestReviewLogDropsAnOlderLoad(t *testing.T) {
	h := logHarness(t, 120, 30, true)
	h.press(t, "3")
	m, first := h.m.loadReviewLog()
	older := logLoads(t, first)
	extra := store.Review{ID: "r5", PRID: "1", Repo: "acme/widgets", Number: 1, Outcome: "complete", StartedAt: t0}
	if err := h.store.SaveReview(context.Background(), extra); err != nil {
		t.Fatal(err)
	}
	m, second := m.loadReviewLog()
	newer := logLoads(t, second)
	if len(older) != 1 || len(newer) != 1 {
		t.Fatalf("loads: %d and %d", len(older), len(newer))
	}
	m, _ = settle(t, m, newer[0], older[0])
	if n := len(m.reviewLog.entries); n != 5 || m.reviewLog.entries[0].Review.ID != "r5" {
		t.Errorf("entries = %d (first %q), want the newer load's 5 starting r5", n, m.reviewLog.entries[0].Review.ID)
	}
}

// The Reviews cost cell reads like Done's: "$X.XX+?" when some runs
// reported no cost, "?" when none did.
func TestReviewLogCostCell(t *testing.T) {
	h := logHarness(t, 140, 30, true)
	for _, c := range []store.CostRow{
		{At: t0, ReviewID: "r1", PRID: "1", CostUSD: 1.5},
		{At: t0, ReviewID: "r1", PRID: "1", CostUnknown: true},
		{At: t0, ReviewID: "r2", PRID: "1", CostUSD: 0.4},
	} {
		if err := h.store.AddCost(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	h.press(t, "3")
	cells := map[string]string{}
	for _, l := range strings.Split(ansi.Strip(content(h.m)), "\n") {
		for _, want := range []string{"$1.50+?", "$0.40", "$12.50", "?"} {
			for _, f := range strings.Fields(l) {
				if f == want {
					cells[want] = l
				}
			}
		}
	}
	for _, want := range []string{"$1.50+?", "$0.40", "$12.50", "?"} {
		if cells[want] == "" {
			t.Errorf("no cost cell %q in:\n%s", want, ansi.Strip(content(h.m)))
		}
	}
}
