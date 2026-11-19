package tui

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/status"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
)

func doneCard(id, repo string, n int, title, author string, st status.Status, doneAt time.Time) tracker.Tracked {
	state := github.PRMerged
	if st == status.Closed {
		state = github.PRClosed
	}
	return tracker.Tracked{
		PR: github.Snapshot{
			ID: id, Repo: repo, Number: n, Title: title, Author: author,
			URL: "https://github.com/" + repo + "/pull/" + id, State: state,
		},
		Status: status.Result{Status: st},
		Place:  tracker.PlaceDone, DoneAt: doneAt,
	}
}

func waitingCard(id, repo string, n int, title, author string, reason tracker.WaitReason, since time.Time) tracker.Tracked {
	t := wallCard(id, repo, n, title, author, status.ReadyForReview, false, 1, 1, time.Hour)
	t.Place, t.WaitReason, t.WaitingSince = tracker.PlaceWaiting, reason, since
	return t
}

// donePRs is two merged, one closed, one hidden and one stale PR, in no
// particular order.
func donePRs() []tracker.Tracked {
	return []tracker.Tracked{
		doneCard("m1", "chainguard-dev/mono", 85001, "feat(policies): add repo_name to the platform Decision", "alice", status.Merged, t0.Add(-26*time.Hour)),
		waitingCard("w1", "chainguard-dev/mono", 85300, "chore: rotate the registry keys", "bob", tracker.WaitHidden, t0.Add(-3*time.Hour)),
		doneCard("c1", "chainguard-dev/os", 4001, "bump zlib to 1.3.2", "erin", status.Closed, t0.Add(-100*time.Hour)),
		waitingCard("w2", "chainguard-dev/enforce", 310, "refactor: split the poller", "frank", tracker.WaitStale, t0.Add(-50*time.Hour)),
		doneCard("m2", "chainguard-dev/mono", 85044, "docs: explain the deploy order", "me", status.Merged, t0.Add(-2*time.Hour)),
	}
}

func testDone(width, height, focus int) doneModel {
	d := newDone()
	d.now, d.focus, d.loc = t0, focus, time.UTC
	d.setSize(width, height)
	return d
}

func renderDone(t *testing.T, d doneModel, width, height int, prs []tracker.Tracked) string {
	t.Helper()
	d.setSize(width, height)
	return d.view(width, height, prs, testTheme(t))
}

func pressDone(t *testing.T, d doneModel, prs []tracker.Tracked, key string) (doneModel, action) {
	t.Helper()
	d, _, act := d.update(tea.KeyPressMsg(tea.Key{Text: key, Code: []rune(key)[0]}), prs)
	return d, act
}

func ids(rows []tracker.Tracked) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.PR.ID)
	}
	return out
}

func TestDoneGolden(t *testing.T) {
	d := testDone(120, 20, 0)
	d.stats = map[string]store.PRStat{
		"m1": {Reviews: 2, CostUSD: 1.75},
		"c1": {Reviews: 1, Unknown: 1},
		"m2": {Reviews: 2, CostUSD: 0.5, Known: 1, Unknown: 1},
	}
	golden(t, "done.golden", renderDone(t, d, 120, 20, donePRs()))
}

func TestDoneTabSwitchesTables(t *testing.T) {
	prs := donePRs()
	// Merged table focused: x does nothing, since only waiting rows unhide.
	d, act := pressDone(t, testDone(120, 20, 0), prs, "x")
	if _, ok := act.(actionNone); !ok {
		t.Errorf("x in Merged & closed = %#v, want actionNone", act)
	}
	// o opens the focused table's row.
	if _, act = pressDone(t, d, prs, "o"); act != (actionOpen{URL: "https://github.com/chainguard-dev/mono/pull/m2"}) {
		t.Errorf("o in Merged & closed = %#v, want the newest merged PR", act)
	}
	// Waiting focused: o opens the waiting row.
	if _, act = pressDone(t, testDone(120, 20, 1), prs, "o"); act != (actionOpen{URL: "https://github.com/chainguard-dev/mono/pull/w1"}) {
		t.Errorf("o in Waiting = %#v, want the newest waiting PR", act)
	}
	// Each table keeps its own cursor.
	d = testDone(120, 20, 0)
	d, _ = pressDone(t, d, prs, "j")
	d.focus = 1
	if _, act = pressDone(t, d, prs, "o"); act != (actionOpen{URL: "https://github.com/chainguard-dev/mono/pull/w1"}) {
		t.Errorf("waiting cursor moved with the merged one: %#v", act)
	}
	d.focus = 0
	if _, act = pressDone(t, d, prs, "o"); act != (actionOpen{URL: "https://github.com/chainguard-dev/mono/pull/m1"}) {
		t.Errorf("merged cursor after j = %#v, want the second PR", act)
	}
	// k and j stay inside the table.
	d, _ = pressDone(t, d, prs, "j")
	d, _ = pressDone(t, d, prs, "j")
	if _, act = pressDone(t, d, prs, "o"); act != (actionOpen{URL: "https://github.com/chainguard-dev/os/pull/c1"}) {
		t.Errorf("j past the end = %#v, want the last row", act)
	}
}

func TestDoneUnhideEmitsAction(t *testing.T) {
	prs := donePRs()
	d, act := pressDone(t, testDone(120, 20, 1), prs, "x")
	if act != (actionUnhide{ID: "w1"}) {
		t.Errorf("x on the first waiting row = %#v, want actionUnhide{w1}", act)
	}
	d, _ = pressDone(t, d, prs, "j")
	if _, act = pressDone(t, d, prs, "x"); act != (actionUnhide{ID: "w2"}) {
		t.Errorf("x on the stale row = %#v, want actionUnhide{w2}", act)
	}
}

func TestDoneEnterOpensDrawer(t *testing.T) {
	prs := donePRs()
	if _, act := pressDone(t, testDone(120, 20, 0), prs, "enter"); act != (actionOpenDrawer{ID: "m2"}) {
		t.Errorf("enter in Merged & closed = %#v, want actionOpenDrawer{m2}", act)
	}
	if _, act := pressDone(t, testDone(120, 20, 1), prs, "enter"); act != (actionOpenDrawer{ID: "w1"}) {
		t.Errorf("enter in Waiting = %#v, want actionOpenDrawer{w1}", act)
	}
	if _, act := pressDone(t, testDone(120, 20, 0), nil, "enter"); act != (actionNone{}) {
		t.Errorf("enter on an empty table = %#v, want actionNone", act)
	}
}

// The drawer opens beside the Done view on the selected row's PR, takes
// focus with tab, and esc closes it.
func TestDoneEnterOpensDrawerThroughRootModel(t *testing.T) {
	hidden := tracker.Hide(wallPR("1"), t0.Add(-time.Minute))
	h := newHarness(t, opts{}, hidden, wallPR("2"))
	h.finishPoll(t)
	h.press(t, "2", "tab", "enter")
	settleDrawer(t, h)
	if !h.m.drawer.isOpen() || h.m.drawer.prID != "1" || h.m.view != ViewDone {
		t.Fatalf("drawer open %v on %q in view %v, want open on 1 in Done", h.m.drawer.isOpen(), h.m.drawer.prID, h.m.view)
	}
	c := ansi.Strip(content(h.m))
	if !strings.Contains(c, "No reviews yet") || !strings.Contains(c, "Waiting for new activity") {
		t.Errorf("the drawer is not beside the Done view:\n%s", c)
	}
	if got := h.m.paneCount(); got != 3 {
		t.Errorf("panes with the drawer open = %d, want 3", got)
	}
	h.press(t, "tab")
	if !h.m.drawerCtx().Focused {
		t.Errorf("tab did not focus the drawer (focus %d)", h.m.focus)
	}
	h.press(t, "esc")
	if h.m.drawer.isOpen() || h.m.focus != 0 {
		t.Errorf("esc: open %v focus %d, want closed and focus 0", h.m.drawer.isOpen(), h.m.focus)
	}
	// A drawer opened in Done doesn't follow to the Wall.
	h.press(t, "tab", "enter")
	settleDrawer(t, h)
	if !h.m.drawer.isOpen() {
		t.Fatal("enter did not open the drawer again")
	}
	h.press(t, "1")
	if strings.Contains(ansi.Strip(content(h.m)), "No reviews yet") {
		t.Errorf("the Done view's drawer shows on the Wall:\n%s", ansi.Strip(content(h.m)))
	}
}

func TestDoneSorting(t *testing.T) {
	prs := donePRs()
	if got, want := ids(mergedRows(prs)), []string{"m2", "m1", "c1"}; !slices.Equal(got, want) {
		t.Errorf("merged & closed order = %v, want newest DoneAt first %v", got, want)
	}
	if got, want := ids(waitingRows(prs)), []string{"w1", "w2"}; !slices.Equal(got, want) {
		t.Errorf("waiting order = %v, want newest WaitingSince first %v", got, want)
	}
}

func TestDoneEmptyTables(t *testing.T) {
	out := renderDone(t, testDone(100, 12, 0), 100, 12, nil)
	if n := strings.Count(out, "Nothing here yet"); n != 2 {
		t.Errorf("empty Done shows %d %q lines, want 2:\n%s", n, "Nothing here yet", out)
	}
}

func TestDoneCleansGitHubText(t *testing.T) {
	prs := []tracker.Tracked{doneCard("m", "o/r", 1, "evil\x1b]0;pwn\x07 title", "a\x1b[2Jb", status.Merged, t0)}
	out := renderDone(t, testDone(100, 12, 0), 100, 12, prs)
	if strings.Contains(out, "\x1b]0;") || strings.Contains(out, "\x1b[2J") {
		t.Errorf("escape sequence from GitHub text reached the frame: %q", out)
	}
}

// A stale PR comes back to the wall with a full since window: Unhide stamps
// ReturnedAt, and the root model saves it.
func TestDoneUnhideStalePRThroughRootModel(t *testing.T) {
	stale := wallPR("1")
	stale.Place, stale.WaitReason, stale.WaitingSince = tracker.PlaceWaiting, tracker.WaitStale, t0.Add(-72*time.Hour)
	h := newHarness(t, opts{}, stale, wallPR("2"))

	h.press(t, "2")
	if h.m.view != ViewDone || h.m.focus != 0 {
		t.Fatalf("after 2: view %v focus %d, want Done and 0", h.m.view, h.m.focus)
	}
	h.press(t, "x") // the Merged & closed table is focused: nothing to unhide
	if h.m.prs["1"].Place != tracker.PlaceWaiting {
		t.Fatalf("x in Merged & closed changed PR 1 to %q", h.m.prs["1"].Place)
	}
	h.press(t, "tab", "x")
	if h.m.focus != 1 {
		t.Errorf("focus after tab = %d, want 1 (Waiting)", h.m.focus)
	}
	if p := h.m.prs["1"]; p.Place != tracker.PlaceWall || !p.ReturnedAt.Equal(t0) || p.WaitReason != "" {
		t.Errorf("PR 1: place %q returned %v reason %q, want wall at %v with no reason", p.Place, p.ReturnedAt, p.WaitReason, t0)
	}
	stored, err := h.store.LoadTracked(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stored["1"].Place != tracker.PlaceWall {
		t.Errorf("stored PR 1 place = %q, want wall", stored["1"].Place)
	}
}

func TestDoneDatesUseTheLocalZone(t *testing.T) {
	// 03:00 UTC on Nov 18 is still Nov 17 eight hours west.
	prs := []tracker.Tracked{doneCard("m1", "o/r", 1, "late", "alice", status.Merged, time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC))}
	d := testDone(120, 20, 0)
	d.loc = time.FixedZone("PST", -8*3600)
	out := ansi.Strip(renderDone(t, d, 120, 20, prs))
	if !strings.Contains(out, "Nov 17") || strings.Contains(out, "Nov 18") {
		t.Errorf("Done date not in the local zone:\n%s", out)
	}
	if newDone().loc != time.Local {
		t.Error("the Done view does not default to the local zone")
	}
}

func TestReviewCostText(t *testing.T) {
	for _, tc := range []struct {
		name string
		st   store.PRStat
		want string
	}{
		{"none", store.PRStat{}, ""},
		{"cost with no reviews", store.PRStat{CostUSD: 3}, ""},
		{"one review", store.PRStat{Reviews: 1, CostUSD: 0.5}, "1 review · $0.50"},
		{"several", store.PRStat{Reviews: 3, CostUSD: 12.345}, "3 reviews · $12.35"},
		{"some unknown", store.PRStat{Reviews: 2, CostUSD: 1, Known: 1, Unknown: 1}, "2 reviews · $1.00+?"},
		{"known $0 and unknown", store.PRStat{Reviews: 2, Known: 1, Unknown: 1}, "2 reviews · $0.00+?"},
		{"all unknown", store.PRStat{Reviews: 2, Unknown: 2}, "2 reviews · ?"},
		{"one unknown", store.PRStat{Reviews: 1, Unknown: 1}, "1 review · ?"},
	} {
		if got := reviewCostText(tc.st); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// doneCostHarness seeds reviews and ledger rows for the Done fixture's PRs.
func doneCostHarness(t *testing.T) *harness {
	t.Helper()
	useUTC(t)
	prs := append(donePRs(), doneCard("x1", "chainguard-dev/mono", 85090, "fix: mixed costs", "gus", status.Merged, t0.Add(-5*time.Hour)),
		doneCard("n1", "chainguard-dev/mono", 85091, "docs: never reviewed", "hal", status.Merged, t0.Add(-6*time.Hour)))
	h := newHarness(t, opts{}, prs...)
	ctx := context.Background()
	for _, r := range []store.Review{
		{ID: "a1", PRID: "m1", StartedAt: t0.Add(-5 * time.Hour)},
		{ID: "a2", PRID: "m1", StartedAt: t0.Add(-4 * time.Hour)},
		{ID: "b1", PRID: "m2", StartedAt: t0.Add(-3 * time.Hour)},
		{ID: "c1r", PRID: "c1", StartedAt: t0.Add(-3 * time.Hour)},
		{ID: "x1r", PRID: "x1", StartedAt: t0.Add(-3 * time.Hour)},
	} {
		if err := h.store.SaveReview(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []store.CostRow{
		{At: t0, ReviewID: "a1", PRID: "m1", CostUSD: 1.5},
		{At: t0, ReviewID: "a2", PRID: "m1", CostUSD: 0.25},
		{At: t0, ReviewID: "b1", PRID: "m2", CostUSD: 0.4},
		{At: t0, ReviewID: "c1r", PRID: "c1", CostUnknown: true},
		{At: t0, ReviewID: "x1r", PRID: "x1", CostUSD: 1},
		{At: t0, ReviewID: "x1r", PRID: "x1", CostUnknown: true},
	} {
		if err := h.store.AddCost(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func TestDoneShowsReviewCost(t *testing.T) {
	h := doneCostHarness(t)
	h.press(t, "2")
	out := ansi.Strip(content(h.m))
	for _, want := range []string{
		"2 reviews · $1.75",
		"1 review · $0.40",
		"1 review · ?",
		"1 review · $1.00+?",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Done view lacks %q:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "never reviewed") && strings.Contains(line, "review") && strings.Contains(line, "$") {
			t.Errorf("a PR with no reviews shows a cost: %q", line)
		}
	}

	// A finished review refreshes the column without leaving the view.
	if err := h.store.SaveReview(context.Background(), store.Review{ID: "b2", PRID: "m2", StartedAt: t0}); err != nil {
		t.Fatal(err)
	}
	h.m, _ = settle(t, h.m, reviewDoneMsg{PRID: "m2", Review: store.Review{ID: "b2", PRID: "m2"}})
	if out := ansi.Strip(content(h.m)); !strings.Contains(out, "2 reviews · $0.40") {
		t.Errorf("after a review finished:\n%s", out)
	}
}
