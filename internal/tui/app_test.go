package tui

import (
	"context"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/status"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/theme"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
)

var t0 = time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)

// fakeClient serves every feed search with found and every fingerprint and
// detail request from snaps. It records each Fingerprints and Details call.
type fakeClient struct {
	found   []string
	snaps   map[string]github.Snapshot
	rate    github.RateLimit
	err     error
	polls   int
	checked [][]string
	detail  [][]string

	viewerErrs  []error
	viewerCalls int
}

// Viewer fails with each of viewerErrs in turn, then answers "me".
func (f *fakeClient) Viewer(context.Context) (string, error) {
	f.viewerCalls++
	if len(f.viewerErrs) > 0 {
		err := f.viewerErrs[0]
		f.viewerErrs = f.viewerErrs[1:]
		return "", err
	}
	return "me", nil
}

func (f *fakeClient) Search(context.Context, string) ([]github.Fingerprint, github.RateLimit, error) {
	f.polls++
	if f.err != nil {
		return nil, github.RateLimit{}, f.err
	}
	var out []github.Fingerprint
	for _, id := range f.found {
		out = append(out, f.snaps[id].Fingerprint())
	}
	return out, f.rate, nil
}

func (f *fakeClient) Fingerprints(_ context.Context, ids []string) ([]github.Fingerprint, github.RateLimit, error) {
	f.checked = append(f.checked, slices.Clone(ids))
	var out []github.Fingerprint
	for _, id := range ids {
		if s, ok := f.snaps[id]; ok {
			out = append(out, s.Fingerprint())
		}
	}
	return out, f.rate, nil
}

func (f *fakeClient) Details(_ context.Context, _ string, ids []string) ([]github.Snapshot, github.RateLimit, error) {
	f.detail = append(f.detail, slices.Clone(ids))
	var out []github.Snapshot
	for _, id := range ids {
		if s, ok := f.snaps[id]; ok {
			out = append(out, s)
		}
	}
	return out, f.rate, nil
}

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

// skippedTick stands in for a tea.Tick command: running it returns the
// message the tick would deliver, without waiting.
type skippedTick struct {
	d   time.Duration
	msg tea.Msg
}

func fakeTick(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
	return func() tea.Msg { return skippedTick{d: d, msg: fn(time.Time{})} }
}

// runCmd runs cmd synchronously, expanding batches. It returns the messages
// to deliver and, separately, the ticks it skipped.
func runCmd(t *testing.T, cmd tea.Cmd) (msgs []tea.Msg, ticks []skippedTick) {
	t.Helper()
	if cmd == nil {
		return nil, nil
	}
	switch msg := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			m, tk := runCmd(t, c)
			msgs = append(msgs, m...)
			ticks = append(ticks, tk...)
		}
	case skippedTick:
		ticks = append(ticks, msg)
	default:
		msgs = append(msgs, msg)
	}
	return msgs, ticks
}

// settle delivers msgs and every message their commands produce, until no
// command produces anything but ticks. It returns the ticks it skipped.
func settle(t *testing.T, m Model, msgs ...tea.Msg) (Model, []skippedTick) {
	t.Helper()
	var ticks []skippedTick
	for i := 0; len(msgs) > 0; i++ {
		if i > 100 {
			t.Fatal("settle: commands never went quiet")
		}
		msg := msgs[0]
		msgs = msgs[1:]
		next, cmd := m.Update(msg)
		m = next.(Model)
		more, tk := runCmd(t, cmd)
		msgs = append(msgs, more...)
		ticks = append(ticks, tk...)
	}
	return m, ticks
}

func update(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func snap(id string) github.Snapshot {
	return github.Snapshot{
		ID: id, Repo: "acme/widgets", Number: len(id), Title: "PR " + id, Author: "sam",
		URL: "https://github.com/acme/widgets/pull/" + id, State: github.PROpen, HeadOID: "head-" + id,
		HeadCommittedAt: t0.Add(-2 * time.Hour), UpdatedAt: t0.Add(-time.Hour),
	}
}

func wallPR(id string) tracker.Tracked {
	return tracker.Tracked{
		PR: snap(id), Feeds: []string{"review-requested"}, Place: tracker.PlaceWall,
		FirstSeen: t0.Add(-time.Hour), LastSeen: t0.Add(-time.Hour), HeadSeenAt: t0.Add(-time.Hour),
	}
}

type harness struct {
	m      Model
	client *fakeClient
	store  *store.Store
	clock  *testClock
	opened []string
	// pollCmd is the first poll, held back so a test can act while it is
	// in flight.
	pollCmd tea.Cmd
	// initMsgs are the messages Init produced other than the load.
	initMsgs []tea.Msg
}

type opts struct {
	noClient bool
	reloads  <-chan config.Reload
	// loadErr replaces the store's answer to the load with this error.
	loadErr error
	// noLoad holds the load back, as if the store were still reading.
	noLoad bool
	// noViewer starts without the gh user, as snow does, so the model
	// looks it up.
	noViewer   bool
	viewerErrs []error
}

// newHarness builds a model over a real store seeded with prs, runs Init and
// delivers the load, leaving the first poll in flight in h.pollCmd. Init's
// reload listener blocks until o.reloads has a value, so fill it first.
func newHarness(t *testing.T, o opts, prs ...tracker.Tracked) *harness {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SaveTracked(context.Background(), prs); err != nil {
		t.Fatalf("SaveTracked: %v", err)
	}
	th, err := theme.New(config.ThemeConfig{})
	if err != nil {
		t.Fatalf("theme.New: %v", err)
	}

	h := &harness{store: st, clock: &testClock{now: t0}, client: &fakeClient{snaps: map[string]github.Snapshot{}, viewerErrs: o.viewerErrs}}
	for _, p := range prs {
		h.client.snaps[p.PR.ID] = p.PR
	}
	d := Deps{
		Config: config.Defaults(), Theme: th, Store: st, Viewer: "me",
		Reloads: o.reloads, Now: h.clock.Now,
	}
	if !o.noClient {
		d.Client = h.client
	}
	if o.noViewer {
		d.Viewer = ""
	}
	m := New(d)
	m.tick = fakeTick
	m.open = func(url string) error { h.opened = append(h.opened, url); return nil }
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 160, Height: 30})

	msgs, _ := runCmd(t, m.Init())
	var loaded []tea.Msg
	for _, msg := range msgs {
		if _, ok := msg.(loadedMsg); ok {
			loaded = append(loaded, msg)
		} else {
			h.initMsgs = append(h.initMsgs, msg)
		}
	}
	if len(loaded) != 1 {
		t.Fatalf("Init produced %d loadedMsg, want 1 (all: %#v)", len(loaded), msgs)
	}
	switch {
	case o.noLoad:
	case o.loadErr != nil:
		m, h.pollCmd = update(t, m, loadedMsg{err: o.loadErr})
	default:
		m, h.pollCmd = update(t, m, loaded[0])
	}
	h.m = m
	return h
}

// finishPoll runs the held first poll and delivers its result.
func (h *harness) finishPoll(t *testing.T) []skippedTick {
	t.Helper()
	msgs, _ := runCmd(t, h.pollCmd)
	h.pollCmd = nil
	var ticks []skippedTick
	h.m, ticks = settle(t, h.m, msgs...)
	return ticks
}

func (h *harness) press(t *testing.T, keys ...string) []skippedTick {
	t.Helper()
	var ticks []skippedTick
	for _, k := range keys {
		var tk []skippedTick
		h.m, tk = settle(t, h.m, key(k))
		ticks = append(ticks, tk...)
	}
	return ticks
}

func content(m Model) string { return m.View().Content }

func pollTicks(ticks []skippedTick) []time.Duration {
	var out []time.Duration
	for _, tk := range ticks {
		if _, ok := tk.msg.(pollTickMsg); ok {
			out = append(out, tk.d)
		}
	}
	return out
}

func TestInitLoadsStoreAndStartsPoll(t *testing.T) {
	h := newHarness(t, opts{}, wallPR("1"), wallPR("2"))
	if len(h.m.prs) != 2 {
		t.Fatalf("loaded %d PRs, want 2", len(h.m.prs))
	}
	if !h.m.polling || h.pollCmd == nil {
		t.Fatal("no poll in flight after the load")
	}
	if !h.m.View().AltScreen {
		t.Error("View does not use the alt screen")
	}
}

func TestPollErrorKeepsWall(t *testing.T) {
	h := newHarness(t, opts{}, wallPR("1"), wallPR("2"))
	m, cmd := update(t, h.m, pollResultMsg{Err: io.EOF})
	if len(m.prs) != 2 {
		t.Errorf("after a failed poll there are %d PRs, want 2", len(m.prs))
	}
	if !strings.Contains(content(m), "EOF") {
		t.Errorf("View does not show the poll error:\n%s", content(m))
	}
	if cmd == nil {
		t.Fatal("no command after a failed poll; the next poll must still be scheduled")
	}
	if _, ticks := runCmd(t, cmd); !reflect.DeepEqual(pollTicks(ticks), []time.Duration{3 * time.Minute}) {
		t.Errorf("next poll ticks = %v, want [3m0s]", pollTicks(ticks))
	}
}

func TestFailedFeedSearchStillUpdatesTrackedPRs(t *testing.T) {
	h := newHarness(t, opts{}, wallPR("1"))
	h.client.err = io.EOF
	s1 := snap("1")
	s1.Title, s1.UpdatedAt = "renamed", t0
	h.client.snaps["1"] = s1
	h.finishPoll(t)
	if got := h.m.prs["1"].PR.Title; got != "renamed" {
		t.Errorf("PR 1 title = %q, want the fresh detail despite the failed search", got)
	}
	c := ansi.Strip(content(h.m))
	if !strings.Contains(c, `search feed "review-requested": EOF`) {
		t.Errorf("View does not name the failed feed:\n%s", c)
	}
	if strings.Contains(c, "poll failed") {
		t.Errorf("View calls a partly successful poll failed:\n%s", c)
	}

	h.client.err = nil
	h.press(t, "R")
	if strings.Contains(content(h.m), "search feed") {
		t.Errorf("the feed error outlived a clean poll:\n%s", content(h.m))
	}
}

func TestLoadErrorTurnsSavingOff(t *testing.T) {
	hidden := tracker.Hide(wallPR("1"), t0.Add(-time.Minute))
	h := newHarness(t, opts{loadErr: errors.New("disk on fire")}, hidden)
	moved := snap("1")
	moved.HeadOID, moved.UpdatedAt = "head-1b", t0
	h.client.snaps["1"] = moved
	h.client.found = []string{"1"}
	h.finishPoll(t)

	if got := h.m.prs["1"]; got.PR.HeadOID != "head-1b" {
		t.Errorf("PR 1 head = %q, want the poll applied to the screen", got.PR.HeadOID)
	}
	stored, err := h.store.LoadTracked(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p := stored["1"]; p.Place != tracker.PlaceWaiting || p.WaitReason != tracker.WaitHidden || p.PR.HeadOID != "head-1" {
		t.Errorf("stored PR 1 = place %q reason %q head %q, want the stored hidden row untouched", p.Place, p.WaitReason, p.PR.HeadOID)
	}
	want := "could not load saved PRs: disk on fire — not saving this session"
	if c := ansi.Strip(content(h.m)); !strings.Contains(c, want) {
		t.Errorf("View does not say %q:\n%s", want, c)
	}

	m, cmd := h.m.apply(actionHide{ID: "1"})
	if cmd != nil {
		t.Error("a hide started a save although saving is off")
	}
	h.m = m
}

func TestViewerLookupRetriesUntilItWorks(t *testing.T) {
	h := newHarness(t, opts{noViewer: true, viewerErrs: []error{errors.New("dial tcp: no route to host")}}, wallPR("1"))
	ticks := h.finishPoll(t)
	if h.client.viewerCalls != 1 || h.client.polls != 0 {
		t.Fatalf("viewer calls %d, searches %d; want one failed lookup and no poll", h.client.viewerCalls, h.client.polls)
	}
	if c := ansi.Strip(content(h.m)); !strings.Contains(c, "no route to host") {
		t.Errorf("View does not show the failed lookup:\n%s", c)
	}
	if got := pollTicks(ticks); !reflect.DeepEqual(got, []time.Duration{3 * time.Minute}) {
		t.Errorf("retry ticks = %v, want [3m0s]", got)
	}

	h.m, _ = settle(t, h.m, pollTickMsg{gen: h.m.pollGen})
	if h.client.viewerCalls != 2 || h.client.polls != 1 || h.m.viewer != "me" {
		t.Errorf("after the retry: viewer calls %d, searches %d, viewer %q; want 2, 1, me", h.client.viewerCalls, h.client.polls, h.m.viewer)
	}
	if strings.Contains(content(h.m), "no route to host") {
		t.Errorf("the lookup error outlived a good poll:\n%s", content(h.m))
	}
	h.press(t, "R")
	if h.client.viewerCalls != 2 {
		t.Errorf("a known viewer was looked up again (%d calls)", h.client.viewerCalls)
	}
}

func TestUIDrawsBeforeTheViewerIsKnown(t *testing.T) {
	h := newHarness(t, opts{noViewer: true}, wallPR("1"))
	if h.client.viewerCalls != 0 {
		t.Fatalf("the model called Viewer %d times while handling messages; it must only return a command", h.client.viewerCalls)
	}
	if h.pollCmd == nil {
		t.Fatal("no viewer lookup started after the load")
	}
	c := ansi.Strip(content(h.m))
	if !strings.Contains(c, "❄ jawnSnow") || !strings.Contains(c, "polling…") {
		t.Errorf("View before the lookup returns:\n%s", c)
	}
}

func TestNoPollBeforeTheLoad(t *testing.T) {
	h := newHarness(t, opts{noLoad: true}, wallPR("1"))
	h.press(t, "R")
	h.m, _ = settle(t, h.m, pollTickMsg{gen: h.m.pollGen})
	if h.m.polling || h.client.polls != 0 {
		t.Errorf("a poll started before the saved PRs loaded (polling=%v, searches=%d)", h.m.polling, h.client.polls)
	}
}

func TestHideDuringPollIsNotOverwritten(t *testing.T) {
	h := newHarness(t, opts{}, wallPR("1"), wallPR("2"))
	h.client.found = []string{"1", "2"}

	m, cmd := h.m.apply(actionHide{ID: "1"})
	h.m, _ = settle(t, m)
	msgs, _ := runCmd(t, cmd)
	h.m, _ = settle(t, h.m, msgs...)
	h.finishPoll(t)

	got := h.m.prs["1"]
	if got.Place != tracker.PlaceWaiting || got.WaitReason != tracker.WaitHidden {
		t.Errorf("PR 1 after the poll: place %q reason %q, want waiting/hidden", got.Place, got.WaitReason)
	}
	if h.m.prs["2"].Place != tracker.PlaceWall {
		t.Errorf("PR 2 place = %q, want wall", h.m.prs["2"].Place)
	}
	stored, err := h.store.LoadTracked(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p := stored["1"]; p.Place != tracker.PlaceWaiting || p.WaitReason != tracker.WaitHidden {
		t.Errorf("stored PR 1: place %q reason %q, want waiting/hidden", p.Place, p.WaitReason)
	}
}

func TestUnhideReturnsToWall(t *testing.T) {
	hidden := tracker.Hide(wallPR("1"), t0.Add(-time.Minute))
	h := newHarness(t, opts{}, hidden)
	m, cmd := h.m.apply(actionUnhide{ID: "1"})
	msgs, _ := runCmd(t, cmd)
	h.m, _ = settle(t, m, msgs...)
	if p := h.m.prs["1"]; p.Place != tracker.PlaceWall || !p.ReturnedAt.Equal(t0) {
		t.Errorf("PR 1: place %q returned %v, want wall at %v", p.Place, p.ReturnedAt, t0)
	}
	stored, err := h.store.LoadTracked(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stored["1"].Place != tracker.PlaceWall {
		t.Errorf("stored PR 1 place = %q, want wall", stored["1"].Place)
	}
}

func TestApplyUnknownIDDoesNothing(t *testing.T) {
	h := newHarness(t, opts{}, wallPR("1"))
	for _, a := range []action{actionNone{}, actionHide{ID: "nope"}, actionUnhide{ID: "nope"}} {
		m, cmd := h.m.apply(a)
		if cmd != nil {
			t.Errorf("apply(%#v) returned a command", a)
		}
		if !reflect.DeepEqual(m.prs, h.m.prs) {
			t.Errorf("apply(%#v) changed the PRs", a)
		}
	}
}

func TestOpenUsesInjectedOpener(t *testing.T) {
	h := newHarness(t, opts{}, wallPR("1"))
	m, cmd := h.m.apply(actionOpen{URL: "https://example.com/pr/1"})
	msgs, _ := runCmd(t, cmd)
	settle(t, m, msgs...)
	if !reflect.DeepEqual(h.opened, []string{"https://example.com/pr/1"}) {
		t.Errorf("opened = %v", h.opened)
	}
}

func TestPollAppliesAndSaves(t *testing.T) {
	h := newHarness(t, opts{}, wallPR("1"))
	s3 := snap("3")
	h.client.found = []string{"3"}
	h.client.snaps["3"] = s3
	h.client.rate = github.RateLimit{Remaining: 4600, Cost: 1, ResetAt: t0.Add(time.Hour)}

	ticks := h.finishPoll(t)
	if len(h.m.prs) != 2 || h.m.prs["3"].Place != tracker.PlaceWall {
		t.Fatalf("after the poll: %v, want PRs 1 and 3 on the wall", h.m.prs)
	}
	if got := pollTicks(ticks); !reflect.DeepEqual(got, []time.Duration{3 * time.Minute}) {
		t.Errorf("next poll ticks = %v, want [3m0s]", got)
	}
	stored, err := h.store.LoadTracked(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 || stored["3"].PR.Title != s3.Title {
		t.Errorf("stored = %v, want PRs 1 and 3", stored)
	}
}

func TestPollDeletesRemoved(t *testing.T) {
	merged := wallPR("1")
	merged.Place = tracker.PlaceDone
	merged.PR.State = github.PRMerged
	merged.PR.MergedAt = t0.Add(-40 * 24 * time.Hour)
	merged.DoneAt = merged.PR.MergedAt
	h := newHarness(t, opts{}, merged, wallPR("2"))

	h.finishPoll(t)
	if _, ok := h.m.prs["1"]; ok {
		t.Fatal("PR 1 is past keep_for but still tracked")
	}
	stored, err := h.store.LoadTracked(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stored["1"]; ok {
		t.Error("PR 1 was not deleted from the store")
	}
	if _, ok := stored["2"]; !ok {
		t.Error("PR 2 missing from the store")
	}
}

func TestPollSlowsWhenRateLimitLow(t *testing.T) {
	h := newHarness(t, opts{}, wallPR("1"))
	h.client.rate = github.RateLimit{Remaining: 800, Cost: 1, ResetAt: t0.Add(time.Hour)}
	ticks := h.finishPoll(t)
	if got := pollTicks(ticks); !reflect.DeepEqual(got, []time.Duration{6 * time.Minute}) {
		t.Errorf("next poll ticks = %v, want [6m0s]", got)
	}
	if !strings.Contains(content(h.m), "polling slower") {
		t.Errorf("View does not explain the slower polling:\n%s", content(h.m))
	}
}

func TestRateLimitErrorBacksOff(t *testing.T) {
	h := newHarness(t, opts{}, wallPR("1"))
	h.client.err = &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: "RATE_LIMITED", Message: "API rate limit exceeded"}}}
	ticks := h.finishPoll(t)
	if got := pollTicks(ticks); !reflect.DeepEqual(got, []time.Duration{6 * time.Minute}) {
		t.Errorf("after a rate-limit error next poll ticks = %v, want [6m0s]", got)
	}
	c := content(h.m)
	if !strings.Contains(c, "GitHub rate limit low — resuming 15:06") {
		t.Errorf("View does not show the paced note:\n%s", c)
	}
	if !strings.Contains(c, "rate limited") {
		t.Errorf("top line does not say the poll was rate limited:\n%s", c)
	}
	if strings.Contains(c, "poll failed") {
		t.Errorf("View shows the raw error for a rate limit:\n%s", c)
	}
	if len(h.m.prs) != 1 {
		t.Errorf("PRs = %d after a rate-limit error, want 1", len(h.m.prs))
	}

	h.m, ticks = settle(t, h.m, pollTickMsg{gen: h.m.pollGen})
	if got := pollTicks(ticks); !reflect.DeepEqual(got, []time.Duration{12 * time.Minute}) {
		t.Errorf("after a second rate-limit error next poll ticks = %v, want [12m0s]", got)
	}

	h.client.err = nil
	h.m, ticks = settle(t, h.m, pollTickMsg{gen: h.m.pollGen})
	if got := pollTicks(ticks); !reflect.DeepEqual(got, []time.Duration{3 * time.Minute}) {
		t.Errorf("after a successful poll next poll ticks = %v, want [3m0s]", got)
	}
	if strings.Contains(content(h.m), "rate limit") {
		t.Errorf("rate-limit note outlived a successful poll:\n%s", content(h.m))
	}
}

func TestDetailIDsIncludeWaitingOnlyWhenIdleDue(t *testing.T) {
	hidden := tracker.Hide(wallPR("2"), t0.Add(-time.Minute))
	h := newHarness(t, opts{}, wallPR("1"), hidden)
	h.finishPoll(t)

	h.clock.now = t0.Add(time.Minute)
	h.press(t, "R")
	h.clock.now = t0.Add(16 * time.Minute)
	h.press(t, "R")

	want := [][]string{{"1", "2"}, {"1"}, {"1", "2"}}
	if !reflect.DeepEqual(h.client.checked, want) {
		t.Errorf("Fingerprints calls = %v, want %v", h.client.checked, want)
	}
}

func TestPollFetchesDetailOnlyForChangedPRs(t *testing.T) {
	h := newHarness(t, opts{}, wallPR("1"), wallPR("2"))
	moved := snap("2")
	moved.HeadOID, moved.UpdatedAt = "head-2b", t0
	h.client.snaps["2"] = moved
	h.client.snaps["3"] = snap("3")
	h.client.found = []string{"3"}
	h.finishPoll(t)
	if want := [][]string{{"2", "3"}}; !reflect.DeepEqual(h.client.detail, want) {
		t.Errorf("Details calls = %v, want %v: the unchanged PR 1 needs none", h.client.detail, want)
	}
	if h.m.prs["2"].PR.HeadOID != "head-2b" {
		t.Errorf("PR 2 head = %q, want the fresh head", h.m.prs["2"].PR.HeadOID)
	}

	h.client.detail = nil
	h.press(t, "R")
	if len(h.client.detail) != 0 {
		t.Errorf("Details calls = %v after a poll with no changes, want none", h.client.detail)
	}
}

// The rollup state can stay PENDING while the required checks finish, if an
// optional check is still running, so a PR waiting on CI is always fetched.
func TestPollAlwaysFetchesPRsWaitingOnCI(t *testing.T) {
	waiting := wallPR("1")
	waiting.Status = status.Result{Status: status.CIWaiting, CI: status.CIPending, Spinning: true}
	h := newHarness(t, opts{}, waiting, wallPR("2"))
	h.finishPoll(t)
	if want := [][]string{{"1"}}; !reflect.DeepEqual(h.client.detail, want) {
		t.Errorf("Details calls = %v, want %v", h.client.detail, want)
	}
}

func TestRPollsOnlyWhenIdle(t *testing.T) {
	h := newHarness(t, opts{}, wallPR("1"))
	m, cmd := update(t, h.m, key("R"))
	msgs, _ := runCmd(t, cmd)
	settle(t, m, msgs...)
	if h.client.polls != 0 {
		t.Fatalf("R during a poll started another; client saw %d searches", h.client.polls)
	}

	h.finishPoll(t)
	if h.client.polls != 1 {
		t.Fatalf("client saw %d searches after the first poll, want 1", h.client.polls)
	}
	h.press(t, "R")
	if h.client.polls != 2 {
		t.Errorf("R when idle: client saw %d searches, want 2", h.client.polls)
	}
}

func TestStaleScheduledPollIsIgnored(t *testing.T) {
	h := newHarness(t, opts{}, wallPR("1"))
	first := pollTicks(h.finishPoll(t))
	if len(first) != 1 {
		t.Fatalf("got %d scheduled polls, want 1", len(first))
	}
	stale := pollTickMsg{gen: h.m.pollGen}
	h.press(t, "R") // reschedules, which makes the earlier tick stale
	if h.client.polls != 2 {
		t.Fatalf("client saw %d searches, want 2", h.client.polls)
	}
	h.m, _ = settle(t, h.m, stale)
	if h.client.polls != 2 {
		t.Errorf("a superseded poll tick started a poll; client saw %d searches", h.client.polls)
	}
	h.m, _ = settle(t, h.m, pollTickMsg{gen: h.m.pollGen})
	if h.client.polls != 3 {
		t.Errorf("the current poll tick did not poll; client saw %d searches", h.client.polls)
	}
}

func TestNilClientNeverPolls(t *testing.T) {
	h := newHarness(t, opts{noClient: true}, wallPR("1"))
	if h.m.polling || h.pollCmd != nil {
		t.Fatal("a poll started with no client")
	}
	h.press(t, "R")
	h.m, _ = settle(t, h.m, pollTickMsg{gen: h.m.pollGen})
	if h.m.polling {
		t.Error("a poll started with no client")
	}
	if len(h.m.prs) != 1 {
		t.Errorf("PRs = %d, want the 1 loaded from the store", len(h.m.prs))
	}
	_ = content(h.m)
}

func TestViewCycling(t *testing.T) {
	h := newHarness(t, opts{}, wallPR("1"))
	h.press(t, "]", "]", "]", "]", "]")
	if h.m.view != ViewWall {
		t.Errorf("after ]×5 view = %v, want Wall", h.m.view)
	}
	h.press(t, "3")
	if h.m.view != ViewReviews {
		t.Errorf("after 3 view = %v, want Reviews", h.m.view)
	}
	if !strings.Contains(content(h.m), "Coming in plan 3") {
		t.Errorf("Reviews placeholder missing:\n%s", content(h.m))
	}
	h.press(t, "1", "[")
	if h.m.view != ViewAgent {
		t.Errorf("[ from Wall: view = %v, want Agent", h.m.view)
	}
	if !strings.Contains(content(h.m), "Coming in plan 2") {
		t.Errorf("Agent placeholder missing:\n%s", content(h.m))
	}
}

func TestReloadErrorKeepsLastGood(t *testing.T) {
	h := newHarness(t, opts{}, wallPR("1"))
	before := h.m.cfg
	bad := config.Defaults()
	bad.GitHub.PollInterval = config.Duration{Duration: 10 * time.Minute}
	h.m, _ = settle(t, h.m, reloadMsg{Config: bad, Err: errors.New("parse config.toml: bad key")})
	if !reflect.DeepEqual(h.m.cfg, before) {
		t.Error("a failed reload changed the config")
	}
	want := "config error: parse config.toml: bad key — using last good config"
	if !strings.Contains(content(h.m), want) {
		t.Errorf("View does not show %q:\n%s", want, content(h.m))
	}
}

func TestReloadReplacesConfig(t *testing.T) {
	h := newHarness(t, opts{}, wallPR("1"))
	h.m, _ = settle(t, h.m, reloadMsg{Err: errors.New("broken")})
	good := config.Defaults()
	good.Review.Budget.DailyUSD = 99
	good.Theme.Chrome.Accent = "#ff0000"
	h.m, _ = settle(t, h.m, reloadMsg{Config: good, Warnings: []string{`unknown key "x"`}})
	if !reflect.DeepEqual(h.m.cfg, good) {
		t.Error("a good reload did not replace the config")
	}
	c := content(h.m)
	if strings.Contains(c, "config error") {
		t.Errorf("the error banner outlived a good reload:\n%s", c)
	}
	if !strings.Contains(c, "$99 today") {
		t.Errorf("status bar does not use the new budget:\n%s", c)
	}
	if !strings.Contains(c, `unknown key "x"`) {
		t.Errorf("reload warning not shown:\n%s", c)
	}
}

func TestNoConfigBannerClearsOnFirstGoodReload(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	th, _ := theme.New(config.ThemeConfig{})
	m := New(Deps{Config: config.Defaults(), Theme: th, Store: st, ConfigBanner: "No config at ~/.config/jawnsnow/config.toml", Now: func() time.Time { return t0 }})
	if !strings.Contains(content(m), "No config at") {
		t.Fatalf("the no-config banner is missing:\n%s", content(m))
	}
	m, _ = settle(t, m, reloadMsg{Err: errors.New("parse config.toml: bad key")})
	m, _ = settle(t, m, reloadMsg{Config: config.Defaults()})
	if strings.Contains(content(m), "No config at") {
		t.Errorf("the no-config banner outlived a good reload:\n%s", content(m))
	}
}

func TestReloadsAreListenedFor(t *testing.T) {
	ch := make(chan config.Reload, 1)
	ch <- config.Reload{Err: errors.New("boom")}
	h := newHarness(t, opts{reloads: ch}, wallPR("1"))
	if len(h.initMsgs) != 1 {
		t.Fatalf("Init's reload listener produced %#v, want one reloadMsg", h.initMsgs)
	}
	var next tea.Cmd
	h.m, next = update(t, h.m, h.initMsgs[0])
	if next == nil {
		t.Error("the model stopped listening after a reload")
	}
	if !strings.Contains(content(h.m), "config error: boom") {
		t.Errorf("reload error not shown:\n%s", content(h.m))
	}
	close(ch)
	if msgs, _ := runCmd(t, next); len(msgs) != 0 {
		t.Errorf("listener on a closed channel produced %v", msgs)
	}
}

func TestStatusBar(t *testing.T) {
	h := newHarness(t, opts{}, wallPR("1"))
	h.client.rate = github.RateLimit{Remaining: 4600, Cost: 1, ResetAt: t0.Add(time.Hour)}
	h.finishPoll(t)
	h.clock.now = t0.Add(40 * time.Second)
	h.m, _ = settle(t, h.m, clockMsg(h.clock.now))
	top := strings.SplitN(content(h.m), "\n", 2)[0]
	for _, want := range []string{"❄ jawnSnow", "◉ watcher", "polled 40s ago", "$0.00 / $250 today", "GH 4.6k", "Wall", "Done", "Reviews", "Cost", "Agent"} {
		if !strings.Contains(top, want) {
			t.Errorf("top line missing %q:\n%s", want, top)
		}
	}
	lines := strings.Split(content(h.m), "\n")
	if len(lines) != 30 {
		t.Errorf("View has %d lines, want the window height 30", len(lines))
	}
	if !strings.Contains(lines[len(lines)-1], "? help") {
		t.Errorf("footer missing key hints: %q", lines[len(lines)-1])
	}
}

func TestBannersShowUnderTopLine(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	th, _ := theme.New(config.ThemeConfig{})
	m := New(Deps{Config: config.Defaults(), Theme: th, Store: st, Banners: []string{"gh is not logged in"}, Now: func() time.Time { return t0 }})
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 20})
	lines := strings.Split(content(m), "\n")
	if len(lines) < 2 || !strings.Contains(lines[1], "gh is not logged in") {
		t.Errorf("banner not on line 2:\n%s", content(m))
	}
}

func TestModeToggle(t *testing.T) {
	h := newHarness(t, opts{}, wallPR("1"))
	h.press(t, "m")
	if !strings.Contains(content(h.m), "◉ auto") {
		t.Errorf("after m the mode is not auto:\n%s", content(h.m))
	}
	h.press(t, "m")
	if !strings.Contains(content(h.m), "◉ watcher") {
		t.Errorf("after m m the mode is not watcher:\n%s", content(h.m))
	}
}

func TestHelpListsEveryKey(t *testing.T) {
	h := newHarness(t, opts{}, wallPR("1"))
	h.press(t, "?")
	c := content(h.m)
	for _, k := range allKeys() {
		if !strings.Contains(c, k.keys) || !strings.Contains(c, k.help) {
			t.Errorf("help overlay missing %q %q", k.keys, k.help)
		}
	}
	h.press(t, "?")
	if h.m.help {
		t.Error("? did not close the help overlay")
	}
}

func TestHelpSwallowsOtherKeys(t *testing.T) {
	h := newHarness(t, opts{}, readyPR("1"))
	h.finishPoll(t)
	polls := h.client.polls
	h.press(t, "?", "]", "x", "R", "m", "2", "tab")
	if h.m.view != ViewWall || h.m.mode != modeWatcher || h.m.focus != 0 {
		t.Errorf("keys under the help overlay acted: view %v mode %q focus %d", h.m.view, h.m.mode, h.m.focus)
	}
	if h.m.prs["1"].Place != tracker.PlaceWall {
		t.Errorf("x under the help overlay hid PR 1")
	}
	if h.client.polls != polls {
		t.Errorf("R under the help overlay polled")
	}
	if !h.m.help {
		t.Fatal("the help overlay closed on a key that isn't ? or esc")
	}
	h.press(t, "esc")
	if h.m.help {
		t.Error("esc did not close the help overlay")
	}
}

func TestFocusCycles(t *testing.T) {
	h := newHarness(t, opts{}, wallPR("1"))
	h.press(t, "tab", "shift+tab", "shift+tab")
	if h.m.focus < 0 || h.m.focus >= paneCount(h.m.view) {
		t.Errorf("focus = %d, out of range for %d panes", h.m.focus, paneCount(h.m.view))
	}
}

func TestQuit(t *testing.T) {
	h := newHarness(t, opts{}, wallPR("1"))
	_, cmd := update(t, h.m, key("q"))
	if cmd == nil {
		t.Fatal("q returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q did not quit")
	}
}

func TestTinyWindowDoesNotPanic(t *testing.T) {
	h := newHarness(t, opts{}, wallPR("1"))
	for _, size := range []tea.WindowSizeMsg{{Width: 0, Height: 0}, {Width: 10, Height: 2}, {Width: 30, Height: 5}} {
		m, _ := update(t, h.m, size)
		m, _ = update(t, m, pollResultMsg{Err: errors.New("a long error\nwith a newline")})
		for i, line := range strings.Split(content(m), "\n") {
			if w := lipgloss.Width(line); size.Width > 0 && w > size.Width {
				t.Errorf("size %v line %d is %d wide: %q", size, i, w, line)
			}
		}
	}
}
