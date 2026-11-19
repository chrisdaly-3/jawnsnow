package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/review"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
)

// fakeReviewer saves a review to the real store for every Run, as the
// engine does, with blocking blocking findings, or a failed review when
// fail is set. It records what it was asked to do.
type fakeReviewer struct {
	store *store.Store
	clock *testClock

	mu       sync.Mutex
	reqs     []review.Request
	ctxErrs  []error // each Run's ctx.Err() when it was called
	released []string
	pruned   int
	blocking int
	fail     error
	n        int
	// headMoved makes Run return review.ErrHeadMoved and save nothing, as
	// the engine does after a force push.
	headMoved bool
	// block makes Run signal entered, then wait for its context to end and
	// return a failed review, as a killed claude does. Set it before the
	// run starts.
	block   bool
	entered chan struct{}
}

func (f *fakeReviewer) Run(ctx context.Context, req review.Request, onEvent func(review.Event)) (store.Review, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.ctxErrs = append(f.ctxErrs, ctx.Err())
	f.n++
	pr := req.PR.PR
	rv := store.Review{
		ID: fmt.Sprintf("rv%d", f.n), PRID: pr.ID, Repo: pr.Repo, Number: pr.Number, HeadOID: pr.HeadOID,
		Mode: string(req.Mode), Outcome: "complete", StartedAt: f.clock.now, EndedAt: f.clock.now,
	}
	for i := range f.blocking {
		rv.Findings = append(rv.Findings, store.Finding{Path: "a.go", Line: i + 1, Blocking: true, Body: "bug", Ledger: "new"})
	}
	err := f.fail
	block, entered := f.block, f.entered
	if f.headMoved {
		f.mu.Unlock()
		return store.Review{}, review.ErrHeadMoved
	}
	f.mu.Unlock()
	if block {
		close(entered)
		<-ctx.Done()
		// The engine's error names the dead process, not context.Canceled.
		err = errors.New("claude: signal: killed")
	}
	onEvent(review.Event{Tool: "Read", Path: "pkg/foo.go"})
	if err != nil {
		rv.Outcome, rv.Error, rv.Findings = "failed", err.Error(), nil
	}
	if serr := f.store.SaveReview(context.Background(), rv); serr != nil {
		return rv, serr
	}
	return rv, err
}

func (f *fakeReviewer) ReleasePR(_ context.Context, repo string, number int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, fmt.Sprintf("%s#%d", repo, number))
	return nil
}

func (f *fakeReviewer) PruneIdle(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruned++
	return nil
}

func (f *fakeReviewer) requested() []review.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

func (f *fakeReviewer) requestedIDs() []string {
	var ids []string
	for _, r := range f.requested() {
		ids = append(ids, r.PR.PR.ID)
	}
	return ids
}

// runReviews runs the review commands held so far and delivers what they
// produce. Reviews those messages start stay held for the next call.
func (h *harness) runReviews(t *testing.T) {
	t.Helper()
	runs := h.runs
	h.runs = nil
	for _, c := range runs {
		msgs, _ := runCmd(t, c)
		h.m, _ = settle(t, h.m, msgs...)
	}
}

func spaceKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "} }

// seenAgo is a wall PR whose head arrived d before t0.
func seenAgo(id string, d time.Duration) tracker.Tracked {
	t := wallPR(id)
	t.HeadSeenAt = t0.Add(-d)
	return t
}

func TestAutoModeStartsEligibleUpToMaxInFlight(t *testing.T) {
	h := newHarness(t, opts{reviews: true, auto: true},
		seenAgo("1", 3*time.Hour), seenAgo("2", time.Hour), seenAgo("3", 2*time.Hour))
	h.finishPoll(t)
	if got, want := len(h.runs), 2; got != want {
		t.Fatalf("auto mode started %d reviews after the poll, want max_in_flight %d", got, want)
	}
	h.runReviews(t)
	reqs := h.reviewer.requested()
	if got := h.reviewer.requestedIDs(); !slices.Equal(got, []string{"1", "3"}) {
		t.Fatalf("first reviews = %v, want the oldest heads [1 3]", got)
	}
	for _, r := range reqs {
		if r.Mode != review.ModeAuto || r.Force || r.OneRun != nil {
			t.Errorf("auto request %s: mode %q force %v oneRun %v", r.PR.PR.ID, r.Mode, r.Force, r.OneRun)
		}
	}
	if len(h.runs) != 1 {
		t.Fatalf("after two finished, %d reviews started, want the third alone", len(h.runs))
	}
	h.runReviews(t)
	if got := h.reviewer.requestedIDs(); !slices.Equal(got, []string{"1", "3", "2"}) {
		t.Errorf("reviews = %v, want [1 3 2]", got)
	}
	if len(h.runs) != 0 {
		t.Errorf("%d more reviews started for PRs already reviewed at their heads", len(h.runs))
	}
}

func TestWatcherModeNeverStartsReviews(t *testing.T) {
	h := newHarness(t, opts{reviews: true}, wallPR("1"), wallPR("2"))
	h.finishPoll(t)
	h.m, _ = settle(t, h.m, reviewTickMsg{})
	h.press(t, "R")
	if len(h.runs) != 0 {
		t.Fatalf("watcher mode started %d reviews", len(h.runs))
	}
}

func TestSpaceForcesReviewEvenWhenReviewedAtHead(t *testing.T) {
	h := newHarness(t, opts{reviews: true, auto: true}, wallPR("1"))
	done := store.Review{ID: "old", PRID: "1", Repo: "acme/widgets", Number: 1, HeadOID: "head-1", Mode: "auto", Outcome: "complete", StartedAt: t0.Add(-time.Minute)}
	if err := h.store.SaveReview(context.Background(), done); err != nil {
		t.Fatal(err)
	}
	h.finishPoll(t)
	if len(h.runs) != 0 {
		t.Fatalf("auto mode started a review of a PR reviewed at its head")
	}
	h.m, _ = settle(t, h.m, spaceKey())
	h.runReviews(t)
	reqs := h.reviewer.requested()
	if len(reqs) != 1 {
		t.Fatalf("space started %d reviews, want 1", len(reqs))
	}
	if r := reqs[0]; !r.Force || r.Mode != review.ModeManual || r.PR.PR.ID != "1" {
		t.Errorf("space request: PR %s force %v mode %q, want a forced manual review of 1", r.PR.PR.ID, r.Force, r.Mode)
	}
}

func TestSpaceOverBudgetAsksFirst(t *testing.T) {
	h := newHarness(t, opts{reviews: true}, wallPR("1"))
	if err := h.store.AddCost(context.Background(), store.CostRow{At: t0.Add(-time.Minute), ReviewID: "x", PRID: "9", CostUSD: 250}); err != nil {
		t.Fatal(err)
	}
	h.finishPoll(t)
	const ask = "Over today's $250 budget — review anyway? y/n"
	h.m, _ = settle(t, h.m, spaceKey())
	if !strings.Contains(content(h.m), ask) {
		t.Fatalf("space over budget did not ask %q:\n%s", ask, content(h.m))
	}
	h.press(t, "n")
	if len(h.runs) != 0 {
		t.Fatal("n started the review")
	}
	if strings.Contains(content(h.m), ask) {
		t.Error("the question is still showing after n")
	}
	h.m, _ = settle(t, h.m, spaceKey())
	h.press(t, "y")
	if len(h.runs) != 1 {
		t.Fatalf("y started %d reviews, want 1", len(h.runs))
	}
}

func TestPickerOneRunModel(t *testing.T) {
	h := newHarness(t, opts{reviews: true}, wallPR("1"))
	h.finishPoll(t)
	h.press(t, "c")
	c := content(h.m)
	for _, want := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5-2"} {
		if !strings.Contains(c, want) {
			t.Errorf("model picker missing %s:\n%s", want, c)
		}
	}
	h.press(t, "j", "enter")
	c = content(h.m)
	for _, want := range []string{"low", "medium", "high", "xhigh", "max"} {
		if !strings.Contains(c, want) {
			t.Errorf("effort picker missing %s:\n%s", want, c)
		}
	}
	h.press(t, "enter")
	h.runReviews(t)
	reqs := h.reviewer.requested()
	if len(reqs) != 1 {
		t.Fatalf("the picker started %d reviews, want 1", len(reqs))
	}
	r := reqs[0]
	if r.OneRun == nil || *r.OneRun != (config.Tier{Model: "claude-sonnet-5-5", Effort: "low"}) {
		t.Fatalf("OneRun = %v, want sonnet/low", r.OneRun)
	}
	if !r.Force || r.Mode != review.ModeManual {
		t.Errorf("picker request: force %v mode %q, want a forced manual review", r.Force, r.Mode)
	}
}

func TestPickerEscCancels(t *testing.T) {
	h := newHarness(t, opts{reviews: true}, wallPR("1"))
	h.finishPoll(t)
	h.press(t, "c", "esc", "x")
	if len(h.runs) != 0 {
		t.Fatal("esc in the picker started a review")
	}
	if h.m.prs["1"].Place != tracker.PlaceWaiting {
		t.Error("the wall did not get keys back after esc closed the picker")
	}
}

func TestRunningBadgeAndSpinning(t *testing.T) {
	h := newHarness(t, opts{reviews: true}, wallPR("1"))
	h.finishPoll(t)
	h.m, _ = settle(t, h.m, spaceKey())
	if !strings.Contains(content(h.m), "⣾ Claude reviewing…") {
		t.Fatalf("no running badge:\n%s", content(h.m))
	}
	if h.m.prs["1"].Status.Spinning {
		t.Error("the review wrote Status.Spinning on the tracked PR; it must be decided at render")
	}
	if got := h.m.spinInterval(); got != spinner.Dot.FPS {
		t.Errorf("spinInterval = %v while a review runs, want spinner.Dot's %v", got, spinner.Dot.FPS)
	}
	if !h.m.spinning {
		t.Error("the spinner tick loop is not running while a review runs")
	}
	if d := h.m.displayPRs(); len(d) != 1 || !d[0].Status.Spinning {
		t.Errorf("the rendered PR is not spinning: %+v", d)
	}

	h.m, _ = settle(t, h.m, reviewEventMsg{PRID: "1", Event: review.Event{Tool: "Read", Path: "pkg/foo.go"}})
	if got := h.m.reviews.state["1"].ticker; got != "reading pkg/foo.go" {
		t.Errorf("ticker = %q, want %q", got, "reading pkg/foo.go")
	}

	// A poll derives every status again; the badge must survive it.
	h.press(t, "R")
	if !strings.Contains(content(h.m), "Claude reviewing…") {
		t.Errorf("the running badge went away after a poll:\n%s", content(h.m))
	}
	h.m, _ = update(t, h.m, spinTickMsg{})
	if !strings.Contains(content(h.m), "⣽ Claude reviewing…") {
		t.Errorf("the badge did not advance to spinner.Dot's next frame:\n%s", content(h.m))
	}

	h.runReviews(t)
	if h.m.spinInterval() != 0 {
		t.Errorf("spinInterval = %v after the review finished, want 0", h.m.spinInterval())
	}
}

func TestDoneBadges(t *testing.T) {
	tests := []struct {
		name     string
		blocking int
		fail     error
		want     string
	}{
		{"blocking", 2, nil, "✦ Claude: 2 blocking"},
		{"clean", 0, nil, "✦ Claude: clean"},
		{"failed", 0, errors.New("claude exited 1"), "! review failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, opts{reviews: true}, wallPR("1"))
			h.reviewer.blocking, h.reviewer.fail = tt.blocking, tt.fail
			h.finishPoll(t)
			h.m, _ = settle(t, h.m, spaceKey())
			h.runReviews(t)
			if c := content(h.m); !strings.Contains(c, tt.want) {
				t.Errorf("badge %q missing:\n%s", tt.want, c)
			}
		})
	}
}

func TestBadgeOfAnOlderHeadIsMuted(t *testing.T) {
	h := newHarness(t, opts{reviews: true}, wallPR("1"))
	h.finishPoll(t)
	h.m, _ = settle(t, h.m, spaceKey())
	h.runReviews(t)
	if b := ansi.Strip(h.m.badge(h.m.prs["1"], 0)); b != "✦ Claude: clean" {
		t.Fatalf("badge at the reviewed head = %q", b)
	}

	moved := h.client.snaps["1"]
	moved.HeadOID, moved.UpdatedAt = "head-1b", t0
	h.client.snaps["1"] = moved
	h.client.found = []string{"1"}
	h.press(t, "R")
	if h.m.prs["1"].PR.HeadOID != "head-1b" {
		t.Fatalf("head = %q after the poll", h.m.prs["1"].PR.HeadOID)
	}
	if got, want := h.m.badge(h.m.prs["1"], 0), h.m.theme.Muted().Render("✦ Claude: clean (older head)"); got != want {
		t.Errorf("badge after a push = %q, want %q", got, want)
	}
	h.m = h.m.withState("1", func(s *reviewState) {
		s.latest = &store.Review{HeadOID: "head-0", Outcome: "failed"}
	})
	if got, want := h.m.badge(h.m.prs["1"], 0), h.m.theme.Muted().Render("! review failed (older head)"); got != want {
		t.Errorf("failed badge at an older head = %q, want %q", got, want)
	}
}

func TestIncompleteBadgeSaysSo(t *testing.T) {
	h := newHarness(t, opts{reviews: true}, wallPR("1"))
	h.finishPoll(t)
	for _, tc := range []struct {
		findings []store.Finding
		want     string
	}{
		{nil, "✦ Claude: clean · incomplete"},
		{[]store.Finding{{Blocking: true, Ledger: "new"}}, "✦ Claude: 1 blocking · incomplete"},
	} {
		h.m = h.m.withState("1", func(s *reviewState) {
			s.latest = &store.Review{HeadOID: h.m.prs["1"].PR.HeadOID, Outcome: "incomplete", Findings: tc.findings}
		})
		if got := ansi.Strip(h.m.badge(h.m.prs["1"], 0)); got != tc.want {
			t.Errorf("incomplete badge = %q, want %q", got, tc.want)
		}
	}
}

func TestTeammateBadge(t *testing.T) {
	pr := wallPR("1")
	pr.PR.Reviews = []github.Review{{Author: "bob", State: "APPROVED", CommitOID: "head-1"}}
	h := newHarness(t, opts{reviews: true, auto: true, config: func(c *config.Config) { c.Feeds[0].Team = true }}, pr)
	h.client.snaps["1"] = pr.PR
	h.finishPoll(t)
	if len(h.runs) != 0 {
		t.Fatal("auto mode reviewed a team PR a teammate already reviewed")
	}
	if c := content(h.m); !strings.Contains(c, "· teammate reviewed") {
		t.Errorf("no teammate badge:\n%s", c)
	}
}

func TestFailedReviewsBackOff(t *testing.T) {
	h := newHarness(t, opts{reviews: true, auto: true}, wallPR("1"))
	h.reviewer.fail = errors.New("claude exited 1")
	h.finishPoll(t)
	h.runReviews(t)
	if got := h.m.reviews.state["1"].failures; got != 1 {
		t.Fatalf("failures = %d, want 1", got)
	}
	if len(h.runs) != 0 {
		t.Fatal("a failed review was retried at once, without backoff")
	}
	h.clock.now = t0.Add(30 * time.Second)
	h.m, _ = settle(t, h.m, reviewTickMsg{})
	if len(h.runs) != 0 {
		t.Fatal("retried 30s after the first failure; the backoff is 1m")
	}
	h.clock.now = t0.Add(2 * time.Minute)
	h.m, _ = settle(t, h.m, reviewTickMsg{})
	if len(h.runs) != 1 {
		t.Fatalf("not retried once the 1m backoff passed (%d runs)", len(h.runs))
	}
	h.runReviews(t)
	if got := h.m.reviews.state["1"].failures; got != 2 {
		t.Errorf("failures = %d after the retry failed too, want 2", got)
	}
}

func TestReleaseWhenPRLeavesWall(t *testing.T) {
	second := wallPR("2")
	second.PR.Number = 2
	h := newHarness(t, opts{reviews: true}, wallPR("1"), second)
	h.finishPoll(t)
	h.m, _ = settle(t, h.m, spaceKey()) // the cursor is on PR 1
	if len(h.runs) != 1 {
		t.Fatalf("space started %d reviews, want 1", len(h.runs))
	}

	merged := h.client.snaps["1"]
	merged.State, merged.MergedAt, merged.UpdatedAt = github.PRMerged, t0, t0
	h.client.snaps["1"] = merged
	h.press(t, "R")
	if got := h.m.prs["1"].Place; got != tracker.PlaceDone {
		t.Fatalf("PR 1 is %q after merging, want done", got)
	}
	if !h.m.reviews.released["1"] {
		t.Error("PR 1 was not marked released when it left the wall")
	}
	h.runReviews(t)
	if errs := h.reviewer.ctxErrs; len(errs) != 1 || !errors.Is(errs[0], context.Canceled) {
		t.Errorf("the running review's context: %v, want it cancelled before the release", errs)
	}
	if !slices.Contains(h.reviewer.released, "acme/widgets#1") {
		t.Errorf("released %v, want acme/widgets#1", h.reviewer.released)
	}

	// A PR the wall hides is released too, and coming back clears it.
	h.press(t, "x")
	if h.m.prs["2"].Place != tracker.PlaceWaiting {
		t.Fatal("x did not hide PR 2")
	}
	if !slices.Contains(h.reviewer.released, "acme/widgets#2") || !h.m.reviews.released["2"] {
		t.Errorf("hiding PR 2 did not release it: %v", h.reviewer.released)
	}
	m, _ := h.m.apply(actionUnhide{ID: "2"})
	h.m = m
	if h.m.reviews.released["2"] {
		t.Error("PR 2 is still marked released after coming back to the wall")
	}
}

func TestNoRunForReleasedPR(t *testing.T) {
	h := newHarness(t, opts{reviews: true}, wallPR("1"), wallPR("2"))
	h.finishPoll(t)
	// PR 1 is still on the wall, so only the released mark can refuse it.
	h.m.reviews.released = map[string]bool{"1": true}
	h.m.mode = modeAuto
	h.m, _ = settle(t, h.m, reviewTickMsg{})
	h.runReviews(t)
	if got := h.reviewer.requestedIDs(); !slices.Equal(got, []string{"2"}) {
		t.Errorf("auto mode reviewed %v, want only [2]: PR 1 is released", got)
	}
	m, cmd := h.m.forceReview("1", nil, true)
	h.m = m
	if cmd != nil || len(h.runs) != 0 {
		t.Error("space started a review of a released PR")
	}
}

func TestCancelledReviewIsNotAFailure(t *testing.T) {
	h := newHarness(t, opts{reviews: true}, wallPR("1"))
	h.reviewer.block, h.reviewer.entered = true, make(chan struct{})
	h.finishPoll(t)
	h.m, _ = settle(t, h.m, spaceKey())
	if len(h.runs) != 1 {
		t.Fatalf("space started %d reviews, want 1", len(h.runs))
	}
	run := h.runs[0]
	h.runs = nil
	out := make(chan tea.Msg, 1)
	go func() { out <- run() }()
	<-h.reviewer.entered

	h.press(t, "x") // PR 1 leaves the wall mid-run
	var first tea.Msg
	select {
	case first = <-out:
	case <-time.After(5 * time.Second):
		t.Fatal("the release did not cancel the running review")
	}
	h.m, _ = settle(t, h.m, first)

	s := h.m.reviews.state["1"]
	if s.running {
		t.Fatal("the cancelled review is still running")
	}
	if s.failures != 0 {
		t.Errorf("failures = %d after a cancelled review, want 0", s.failures)
	}
	if s.latest == nil || s.latest.ID == "" {
		t.Errorf("latest = %v, want the cancelled run's saved review", s.latest)
	}
	m, _ := h.m.apply(actionUnhide{ID: "1"})
	h.m = m
	c := content(h.m)
	if !strings.Contains(c, "· review cancelled") || strings.Contains(c, "review failed") {
		t.Errorf("badge after a cancelled review, want \"· review cancelled\":\n%s", c)
	}
}

func TestHeadMovedIsNotAFailure(t *testing.T) {
	h := newHarness(t, opts{reviews: true}, wallPR("1"))
	h.reviewer.headMoved = true
	h.finishPoll(t)
	h.m, _ = settle(t, h.m, spaceKey())
	h.runReviews(t)

	s := h.m.reviews.state["1"]
	if s.running {
		t.Fatal("the review is still running")
	}
	if s.failures != 0 || s.latest != nil {
		t.Errorf("failures = %d, latest = %+v; want no failure and no review", s.failures, s.latest)
	}
	if c := content(h.m); strings.Contains(c, "review failed") || strings.Contains(c, "review cancelled") {
		t.Errorf("badge after the head moved:\n%s", c)
	}
	if rs, err := h.store.Reviews(context.Background(), "1"); err != nil || len(rs) != 0 {
		t.Errorf("stored reviews = %+v, %v; want none", rs, err)
	}
}

// queueHarness has PRs 1 and 2 under auto review, filling max_in_flight,
// and the cursor on PR 3, which auto mode skips: it is reviewed at its head.
func queueHarness(t *testing.T) *harness {
	t.Helper()
	third := seenAgo("3", 10*time.Minute)
	third.PR.Number = 3
	h := newHarness(t, opts{reviews: true, auto: true}, seenAgo("1", 3*time.Hour), seenAgo("2", 2*time.Hour), third)
	rv := store.Review{ID: "old", PRID: "3", Repo: "acme/widgets", Number: 3, HeadOID: "head-3", Mode: "auto", Outcome: "complete", StartedAt: t0.Add(-time.Minute)}
	if err := h.store.SaveReview(context.Background(), rv); err != nil {
		t.Fatal(err)
	}
	h.finishPoll(t)
	if len(h.runs) != 2 {
		t.Fatalf("auto mode started %d reviews, want 2", len(h.runs))
	}
	h.m, _ = settle(t, h.m, spaceKey())
	if len(h.runs) != 2 {
		t.Fatalf("space at max_in_flight started a review (%d running)", len(h.runs))
	}
	if !strings.Contains(content(h.m), "review queued: 2 already running") {
		t.Errorf("no queued notice:\n%s", content(h.m))
	}
	return h
}

func TestSpaceQueuesWhenAtMaxInFlight(t *testing.T) {
	t.Run("starts when a slot frees", func(t *testing.T) {
		h := queueHarness(t)
		h.runReviews(t)
		if len(h.runs) != 1 {
			t.Fatalf("%d reviews started after two finished, want the queued one", len(h.runs))
		}
		h.runReviews(t)
		reqs := h.reviewer.requested()
		if got := h.reviewer.requestedIDs(); !slices.Equal(got, []string{"1", "2", "3"}) {
			t.Fatalf("reviews = %v, want [1 2 3]", got)
		}
		if r := reqs[2]; !r.Force || r.Mode != review.ModeManual {
			t.Errorf("queued request: force %v mode %q, want a forced manual review", r.Force, r.Mode)
		}
	})
	t.Run("dropped when released", func(t *testing.T) {
		h := queueHarness(t)
		h.press(t, "x") // the cursor is still on PR 3
		if h.m.queued("3") {
			t.Fatal("PR 3 is still queued after leaving the wall")
		}
		h.runReviews(t)
		h.runReviews(t)
		if got := h.reviewer.requestedIDs(); !slices.Equal(got, []string{"1", "2"}) {
			t.Errorf("reviews = %v, want [1 2]: the released PR's queued review is dropped", got)
		}
	})
}

func TestQuitCancelsRunningReviews(t *testing.T) {
	h := newHarness(t, opts{reviews: true, auto: true}, wallPR("1"), wallPR("2"))
	h.finishPoll(t)
	if len(h.runs) != 2 {
		t.Fatalf("%d reviews running, want 2", len(h.runs))
	}
	m, cmd := update(t, h.m, key("q"))
	if isQuit(t, cmd) {
		t.Fatal("q quit at once with reviews running")
	}
	const ask = "2 reviews running — quit and cancel them? y/n"
	if !strings.Contains(content(m), ask) {
		t.Fatalf("q did not ask %q:\n%s", ask, content(m))
	}
	m, cmd = update(t, m, key("y"))
	msgs, ticks := runCmd(t, cmd)
	if len(msgs) != 0 {
		t.Fatalf("y produced %v before the reviews finished", msgs)
	}
	if len(ticks) != 1 || ticks[0].d != 5*time.Second {
		t.Errorf("y scheduled %v, want one 5s wait", ticks)
	}

	// Each run's events come first; skip them to reach its reviewDoneMsg.
	var done []tea.Msg
	for _, c := range h.runs {
		msg := c()
		for {
			ev, ok := msg.(reviewEventMsg)
			if !ok {
				break
			}
			msg = waitReview(ev.next)()
		}
		done = append(done, msg)
	}
	h.runs = nil
	if errs := h.reviewer.ctxErrs; len(errs) != 2 || !errors.Is(errs[0], context.Canceled) || !errors.Is(errs[1], context.Canceled) {
		t.Fatalf("Run contexts: %v, want both cancelled", errs)
	}
	if len(done) != 2 {
		t.Fatalf("got %v, want two reviewDoneMsg", done)
	}
	m, cmd = update(t, m, done[0])
	if isQuit(t, cmd) {
		t.Fatal("quit before the second review finished")
	}
	_, cmd = update(t, m, done[1])
	if !isQuit(t, cmd) {
		t.Fatal("did not quit once every review finished")
	}
}

func TestQuitWaitsAtMostFiveSeconds(t *testing.T) {
	h := newHarness(t, opts{reviews: true, auto: true}, wallPR("1"))
	h.finishPoll(t)
	m, _ := update(t, h.m, key("q"))
	m, _ = update(t, m, key("y"))
	if _, cmd := update(t, m, reviewQuitTimeoutMsg{}); !isQuit(t, cmd) {
		t.Fatal("the 5s wait ran out without quitting")
	}
}

func TestNoReviewerRefusesAutoAndSpace(t *testing.T) {
	h := newHarness(t, opts{reviewsOff: "claude is not installed"}, wallPR("1"))
	h.finishPoll(t)
	h.press(t, "m")
	if h.m.mode != modeWatcher {
		t.Error("m turned on auto mode with no reviewer")
	}
	const note = "reviews unavailable: claude is not installed"
	if !strings.Contains(content(h.m), note) {
		t.Errorf("m did not say %q:\n%s", note, content(h.m))
	}
	h.m.notice = ""
	h.m, _ = settle(t, h.m, spaceKey())
	if !strings.Contains(content(h.m), note) {
		t.Errorf("space did not say %q:\n%s", note, content(h.m))
	}
	h.press(t, "c")
	if h.m.reviews.picker != nil {
		t.Error("c opened the model picker with no reviewer")
	}
}

func TestStatusBarShowsTodaysSpend(t *testing.T) {
	tests := []struct {
		name   string
		costs  []store.CostRow
		spend  string
		paused bool
	}{
		{"under budget", []store.CostRow{
			{At: t0.Add(-time.Minute), CostUSD: 3.12},
			{At: t0.Add(-48 * time.Hour), CostUSD: 100}, // yesterday's doesn't count
		}, "$3.12 / $250 today", false},
		{"over budget", []store.CostRow{
			{At: t0.Add(-time.Minute), CostUSD: 3.12},
			{At: t0, CostUSD: 250},
		}, "$253.12 / $250 today", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, opts{reviews: true, auto: true}, wallPR("1"))
			for i, c := range tt.costs {
				c.ReviewID, c.PRID = fmt.Sprint("x", i), "9"
				if err := h.store.AddCost(context.Background(), c); err != nil {
					t.Fatal(err)
				}
			}
			h.finishPoll(t)
			top := strings.SplitN(content(h.m), "\n", 2)[0]
			if !strings.Contains(top, tt.spend) {
				t.Errorf("top line missing %q:\n%s", tt.spend, top)
			}
			if got := strings.Contains(top, "auto-review paused: daily budget"); got != tt.paused {
				t.Errorf("paused note shown = %v, want %v:\n%s", got, tt.paused, top)
			}
			if started := len(h.runs) > 0; started == tt.paused {
				t.Errorf("auto mode started a review = %v, over budget = %v", started, tt.paused)
			}
		})
	}
}

func TestWaitingTableShowsLatestVerdict(t *testing.T) {
	hidden := tracker.Hide(wallPR("1"), t0.Add(-time.Hour))
	h := newHarness(t, opts{reviews: true}, hidden)
	rv := store.Review{
		ID: "r1", PRID: "1", Repo: "acme/widgets", Number: 1, HeadOID: "head-1", Mode: "manual", Outcome: "complete",
		StartedAt: t0.Add(-2 * time.Hour), Findings: []store.Finding{{Path: "a.go", Line: 1, Blocking: true, Body: "bug", Ledger: "new"}},
	}
	if err := h.store.SaveReview(context.Background(), rv); err != nil {
		t.Fatal(err)
	}
	h.finishPoll(t)
	h.press(t, "2")
	if c := content(h.m); !strings.Contains(c, "✦ Claude: 1 blocking") {
		t.Errorf("Waiting table has no latest verdict:\n%s", c)
	}
}

func TestPruneIdleEveryTenMinutes(t *testing.T) {
	h := newHarness(t, opts{reviews: true}, wallPR("1"))
	var ticks []skippedTick
	h.m, ticks = settle(t, h.m, pruneTickMsg{})
	if h.reviewer.pruned != 1 {
		t.Errorf("PruneIdle ran %d times, want 1", h.reviewer.pruned)
	}
	if !slices.ContainsFunc(ticks, func(tk skippedTick) bool {
		_, ok := tk.msg.(pruneTickMsg)
		return ok && tk.d == 10*time.Minute
	}) {
		t.Errorf("no next prune in 10m: %v", ticks)
	}
}
