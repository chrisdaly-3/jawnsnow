package tracker

import (
	"slices"
	"testing"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/status"
)

const viewer = "me"

var now = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

func snap(id string, mods ...func(*github.Snapshot)) github.Snapshot {
	s := github.Snapshot{
		ID: id, Repo: "o/r", State: github.PROpen, HeadOID: "H1",
		UpdatedAt: now.Add(-time.Hour), HeadCommittedAt: now.Add(-2 * time.Hour),
	}
	for _, m := range mods {
		m(&s)
	}
	return s
}

func testConfig() config.Config {
	return config.Config{
		Feeds: []config.Feed{
			{Name: "short", Since: config.Duration{Duration: 36 * time.Hour}},
			{Name: "long", Since: config.Duration{Duration: days(14)}},
		},
		Done: config.DoneConfig{KeepFor: config.Duration{Duration: days(30)}},
	}
}

func poll(at time.Time, found map[string][]string, snaps ...github.Snapshot) PollInput {
	return PollInput{Found: found, Snapshots: snaps, Viewer: viewer, Now: at}
}

func TestApplyAddsFoundPRToWall(t *testing.T) {
	s := snap("A")
	next, removed := Apply(nil, poll(now, map[string][]string{"A": {"long"}}, s), testConfig())
	got, ok := next["A"]
	if !ok || len(removed) != 0 {
		t.Fatalf("next=%v removed=%v", next, removed)
	}
	if got.Place != PlaceWall || !got.FirstSeen.Equal(now) || !got.LastSeen.Equal(now) || !got.HeadSeenAt.Equal(now) {
		t.Errorf("unexpected tracked entry: %+v", got)
	}
	if !slices.Equal(got.Feeds, []string{"long"}) {
		t.Errorf("Feeds = %v", got.Feeds)
	}
	if got.Status.Status == "" {
		t.Error("Status not derived")
	}
}

func TestApplyMergedMovesToDone(t *testing.T) {
	merged := now.Add(-2 * time.Hour)
	s := snap("A", func(s *github.Snapshot) { s.State = github.PRMerged; s.MergedAt = merged })
	next, _ := Apply(nil, poll(now, map[string][]string{"A": {"long"}}, s), testConfig())
	got := next["A"]
	if got.Place != PlaceDone || !got.DoneAt.Equal(merged) {
		t.Errorf("Place=%s DoneAt=%v", got.Place, got.DoneAt)
	}
	closedAt := now.Add(-3 * time.Hour)
	c := snap("B", func(s *github.Snapshot) { s.State = github.PRClosed; s.ClosedAt = closedAt })
	next, _ = Apply(nil, poll(now, map[string][]string{"B": {"long"}}, c), testConfig())
	if g := next["B"]; g.Place != PlaceDone || !g.DoneAt.Equal(closedAt) {
		t.Errorf("closed: Place=%s DoneAt=%v", g.Place, g.DoneAt)
	}
}

func TestApplyReopenedPRReturnsToWall(t *testing.T) {
	closedAt := now.Add(-days(3))
	done := Tracked{
		PR:    snap("A", func(s *github.Snapshot) { s.State = github.PRClosed; s.ClosedAt = closedAt }),
		Feeds: []string{"long"}, Place: PlaceDone, DoneAt: closedAt, FirstSeen: now.Add(-days(5)),
	}
	reopened := snap("A", func(s *github.Snapshot) { s.UpdatedAt = now.Add(-time.Minute) })
	next, removed := Apply(map[string]Tracked{"A": done}, poll(now, map[string][]string{"A": {"long"}}, reopened), testConfig())
	got := next["A"]
	if got.Place != PlaceWall || !got.ReturnedAt.Equal(now) || !got.DoneAt.IsZero() || len(removed) != 0 {
		t.Errorf("reopened PR: Place=%s ReturnedAt=%v DoneAt=%v removed=%v, want wall, returned now, no DoneAt",
			got.Place, got.ReturnedAt, got.DoneAt, removed)
	}
}

func TestApplyStaleMovesToWaiting(t *testing.T) {
	s := snap("A", func(s *github.Snapshot) { s.UpdatedAt = now.Add(-days(15)) })
	next, _ := Apply(nil, poll(now, map[string][]string{"A": {"long"}}, s), testConfig())
	got := next["A"]
	if got.Place != PlaceWaiting || got.WaitReason != WaitStale || !got.WaitingSince.Equal(now) || got.WaitingHead != "H1" {
		t.Errorf("got %+v", got)
	}
}

func TestApplyMultiFeedUsesLongestSince(t *testing.T) {
	s := snap("A", func(s *github.Snapshot) { s.UpdatedAt = now.Add(-days(3)) })
	next, _ := Apply(nil, poll(now, map[string][]string{"A": {"short", "long"}}, s), testConfig())
	if len(next) != 1 {
		t.Fatalf("len(next) = %d, want 1", len(next))
	}
	if got := next["A"]; got.Place != PlaceWall {
		t.Errorf("Place = %s, want wall", got.Place)
	}
	// Feeds stay known after search stops returning the PR.
	next2, _ := Apply(next, poll(now.Add(time.Minute), nil, s), testConfig())
	if got := next2["A"]; got.Place != PlaceWall || !slices.Equal(got.Feeds, []string{"long", "short"}) {
		t.Errorf("after search stops: %+v", got)
	}
}

func TestApplyUnknownFeedCountsAsDefault(t *testing.T) {
	s := snap("A", func(s *github.Snapshot) { s.UpdatedAt = now.Add(-days(10)) })
	next, _ := Apply(nil, poll(now, map[string][]string{"A": {"gone"}}, s), testConfig())
	if next["A"].Place != PlaceWall {
		t.Errorf("10d old with unknown feed should stay on wall (14d default)")
	}
	s2 := snap("B", func(s *github.Snapshot) { s.UpdatedAt = now.Add(-days(15)) })
	next, _ = Apply(nil, poll(now, nil, s2), testConfig())
	if next["B"].Place != PlaceWaiting {
		t.Errorf("empty Feeds should use 14d default")
	}
}

func waitingPR(reason WaitReason) Tracked {
	pr := snap("A")
	return Tracked{
		PR: pr, Feeds: []string{"long"}, Place: PlaceWaiting, WaitReason: reason,
		WaitingSince: now.Add(-time.Hour), WaitingHead: "H1",
		FirstSeen: now.Add(-days(1)), LastSeen: now.Add(-time.Hour), HeadSeenAt: now.Add(-days(1)),
	}
}

func TestApplyWaitingReturnsOnNewCommit(t *testing.T) {
	prev := map[string]Tracked{"A": waitingPR(WaitHidden)}
	s := snap("A", func(s *github.Snapshot) { s.HeadOID = "H2" })
	next, _ := Apply(prev, poll(now, nil, s), testConfig())
	got := next["A"]
	if got.Place != PlaceWall || got.WaitReason != "" || !got.WaitingSince.IsZero() || got.WaitingHead != "" || !got.ReturnedAt.Equal(now) {
		t.Errorf("got %+v", got)
	}
}

func TestApplyWaitingReturnsOnOthersComment(t *testing.T) {
	prev := map[string]Tracked{"A": waitingPR(WaitStale)}
	s := snap("A", func(s *github.Snapshot) {
		s.Comments = []github.Comment{{Author: "them", At: now.Add(-time.Minute)}}
	})
	next, _ := Apply(prev, poll(now, nil, s), testConfig())
	if got := next["A"]; got.Place != PlaceWall || !got.ReturnedAt.Equal(now) {
		t.Errorf("got %+v", got)
	}
}

func TestApplyWaitingIgnoresYourOwnComment(t *testing.T) {
	prev := map[string]Tracked{"A": waitingPR(WaitHidden)}
	s := snap("A", func(s *github.Snapshot) {
		s.Comments = []github.Comment{{Author: viewer, At: now.Add(-time.Minute)}}
		s.Reviews = []github.Review{{Author: viewer, State: "COMMENTED", SubmittedAt: now.Add(-time.Minute)}}
	})
	next, _ := Apply(prev, poll(now, nil, s), testConfig())
	if got := next["A"]; got.Place != PlaceWaiting {
		t.Errorf("Place = %s, want waiting", got.Place)
	}
}

func TestApplyExpiresAfterKeepFor(t *testing.T) {
	done := Tracked{PR: snap("D", func(s *github.Snapshot) { s.State = github.PRMerged }), Place: PlaceDone, DoneAt: now.Add(-days(31))}
	wait := waitingPR(WaitHidden)
	wait.PR.ID = "W"
	wait.WaitingSince = now.Add(-days(31))
	wait.PR.HeadCommittedAt = now.Add(-days(40)) // no activity since it started waiting
	fresh := Tracked{PR: snap("F", func(s *github.Snapshot) { s.State = github.PRMerged }), Place: PlaceDone, DoneAt: now.Add(-days(29))}
	prev := map[string]Tracked{"D": done, "W": wait, "F": fresh}

	next, removed := Apply(prev, poll(now, nil), testConfig())
	if !slices.Equal(removed, []string{"D", "W"}) {
		t.Errorf("removed = %v", removed)
	}
	if _, ok := next["F"]; !ok || len(next) != 1 {
		t.Errorf("next = %v", next)
	}
	if len(prev) != 3 {
		t.Error("prev was mutated")
	}

	// A removed ID found again starts fresh.
	again, _ := Apply(next, poll(now, map[string][]string{"D": {"long"}}, snap("D")), testConfig())
	if g := again["D"]; g.Place != PlaceWall || !g.FirstSeen.Equal(now) {
		t.Errorf("fresh restart: %+v", g)
	}
}

func TestApplyHeadChangeResetsHeadSeenAt(t *testing.T) {
	first := now.Add(-time.Hour)
	next, _ := Apply(nil, poll(first, map[string][]string{"A": {"long"}}, snap("A")), testConfig())
	// Same head keeps HeadSeenAt.
	same, _ := Apply(next, poll(now, nil, snap("A")), testConfig())
	if !same["A"].HeadSeenAt.Equal(first) {
		t.Errorf("HeadSeenAt = %v, want %v", same["A"].HeadSeenAt, first)
	}
	changed, _ := Apply(next, poll(now, nil, snap("A", func(s *github.Snapshot) { s.HeadOID = "H2" })), testConfig())
	if !changed["A"].HeadSeenAt.Equal(now) {
		t.Errorf("HeadSeenAt = %v, want %v", changed["A"].HeadSeenAt, now)
	}
}

func TestApplyNoSnapshotKeepsPRButAppliesTimeRules(t *testing.T) {
	old := now.Add(-days(15))
	wall := Tracked{
		PR: snap("A", func(s *github.Snapshot) { s.UpdatedAt = old }), Status: status.Result{Status: status.CIWaiting},
		Feeds: []string{"long"}, Place: PlaceWall, FirstSeen: old, LastSeen: old, HeadSeenAt: old,
	}
	next, _ := Apply(map[string]Tracked{"A": wall}, poll(now, nil), testConfig())
	got := next["A"]
	if got.Place != PlaceWaiting || got.WaitReason != WaitStale || got.WaitingHead != "H1" {
		t.Errorf("got %+v", got)
	}
	if got.Status.Status != status.CIWaiting || !got.LastSeen.Equal(old) {
		t.Errorf("Status/LastSeen should be kept: %+v", got)
	}
}

func TestHideUnhide(t *testing.T) {
	tr := Tracked{PR: snap("A"), Place: PlaceWall}
	h := Hide(tr, now)
	if h.Place != PlaceWaiting || h.WaitReason != WaitHidden || !h.WaitingSince.Equal(now) || h.WaitingHead != "H1" {
		t.Errorf("hide: %+v", h)
	}
	u := Unhide(h, now.Add(time.Hour))
	if u.Place != PlaceWall || u.WaitReason != "" || !u.WaitingSince.IsZero() || u.WaitingHead != "" || !u.ReturnedAt.Equal(now.Add(time.Hour)) {
		t.Errorf("unhide: %+v", u)
	}
}

func TestUnhideStaleGetsFullWindow(t *testing.T) {
	s := snap("A", func(s *github.Snapshot) { s.UpdatedAt = now.Add(-days(20)) })
	next, _ := Apply(nil, poll(now.Add(-time.Hour), map[string][]string{"A": {"long"}}, s), testConfig())
	if next["A"].Place != PlaceWaiting {
		t.Fatalf("setup: want waiting, got %s", next["A"].Place)
	}
	prev := map[string]Tracked{"A": Unhide(next["A"], now)}
	after, _ := Apply(prev, poll(now.Add(time.Hour), nil, s), testConfig())
	if after["A"].Place != PlaceWall {
		t.Errorf("Place = %s, want wall", after["A"].Place)
	}
}

func TestNeedsDetail(t *testing.T) {
	cases := []struct {
		place Place
		idle  bool
		want  bool
	}{
		{PlaceWall, false, true}, {PlaceWall, true, true},
		{PlaceWaiting, false, false}, {PlaceWaiting, true, true},
		{PlaceDone, false, false}, {PlaceDone, true, false},
	}
	for _, c := range cases {
		if got := NeedsDetail(Tracked{Place: c.place}, c.idle); got != c.want {
			t.Errorf("NeedsDetail(%s, %v) = %v, want %v", c.place, c.idle, got, c.want)
		}
	}
}

func TestOthersActivityAt(t *testing.T) {
	s := snap("A", func(s *github.Snapshot) {
		s.HeadCommittedAt = now.Add(-5 * time.Hour)
		s.Reviews = []github.Review{{Author: "them", SubmittedAt: now.Add(-3 * time.Hour)}, {Author: viewer, SubmittedAt: now}}
		s.Comments = []github.Comment{{Author: "them", At: now.Add(-2 * time.Hour)}, {Author: viewer, At: now}}
	})
	if got := OthersActivityAt(s, viewer); !got.Equal(now.Add(-2 * time.Hour)) {
		t.Errorf("got %v", got)
	}
}
