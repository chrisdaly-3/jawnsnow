// Package tracker decides where each tracked PR lives (wall, waiting or
// done) and when it stops being tracked. It is a pure function over the
// previous state and a poll result.
package tracker

import (
	"slices"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/status"
)

type Place string

const (
	PlaceWall    Place = "wall"
	PlaceWaiting Place = "waiting"
	PlaceDone    Place = "done"
)

type WaitReason string

const (
	WaitHidden WaitReason = "hidden"
	WaitStale  WaitReason = "stale"
)

// defaultSince applies to a feed that is no longer in the config.
const defaultSince = 14 * 24 * time.Hour

type Tracked struct {
	PR     github.Snapshot
	Status status.Result
	Feeds  []string

	Place        Place
	WaitReason   WaitReason
	WaitingSince time.Time
	WaitingHead  string

	FirstSeen, LastSeen, HeadSeenAt, DoneAt time.Time
	ReturnedAt                              time.Time // last time it came back to the wall (unhide or new activity)
}

type PollInput struct {
	Found     map[string][]string // PR node ID → names of feeds whose search returned it
	Snapshots []github.Snapshot   // fresh detail for some tracked/found PRs
	Viewer    string
	Now       time.Time
}

// Apply returns the tracked set after a poll, plus the sorted IDs that
// stopped being tracked. It does not modify prev or in.
func Apply(prev map[string]Tracked, in PollInput, cfg config.Config) (next map[string]Tracked, removed []string) {
	next = make(map[string]Tracked, len(prev))
	for id, t := range prev {
		next[id] = t
	}

	for _, s := range in.Snapshots {
		t, ok := next[s.ID]
		if !ok {
			t = Tracked{FirstSeen: in.Now, HeadSeenAt: in.Now, Place: PlaceWall}
		} else if t.PR.HeadOID != s.HeadOID {
			t.HeadSeenAt = in.Now
		}
		t.PR = s
		t.Status = status.Derive(status.Input{PR: s, Viewer: in.Viewer, IgnoreChecks: cfg.IgnoreChecksFor(s.Repo)})
		t.LastSeen = in.Now
		next[s.ID] = t
	}

	for id, feeds := range in.Found {
		if t, ok := next[id]; ok {
			t.Feeds = unionSorted(t.Feeds, feeds)
			next[id] = t
		}
	}

	for id, t := range next {
		t = place(t, in, cfg)
		if expired(t, in.Now, cfg.Done.KeepFor.Duration) {
			delete(next, id)
			removed = append(removed, id)
			continue
		}
		next[id] = t
	}
	slices.Sort(removed)
	return next, removed
}

func place(t Tracked, in PollInput, cfg config.Config) Tracked {
	switch t.PR.State {
	case github.PRMerged, github.PRClosed:
		if t.Place != PlaceDone {
			t.Place = PlaceDone
			t.WaitReason, t.WaitingSince, t.WaitingHead = "", time.Time{}, ""
		}
		at := t.PR.MergedAt
		if t.PR.State == github.PRClosed {
			at = t.PR.ClosedAt
		}
		switch {
		case !at.IsZero():
			t.DoneAt = at
		case t.DoneAt.IsZero():
			t.DoneAt = in.Now
		}
		return t
	}

	if t.Place == PlaceDone { // reopened
		t = Unhide(t, in.Now)
		t.DoneAt = time.Time{}
	}

	if t.Place == PlaceWaiting &&
		(t.PR.HeadOID != t.WaitingHead || OthersActivityAt(t.PR, in.Viewer).After(t.WaitingSince)) {
		t = Unhide(t, in.Now)
	}

	if t.Place == PlaceWall {
		last := t.PR.UpdatedAt
		if t.ReturnedAt.After(last) {
			last = t.ReturnedAt
		}
		if in.Now.Sub(last) > longestSince(t.Feeds, cfg) {
			t.Place = PlaceWaiting
			t.WaitReason = WaitStale
			t.WaitingSince = in.Now
			t.WaitingHead = t.PR.HeadOID
		}
	}
	return t
}

func expired(t Tracked, now time.Time, keepFor time.Duration) bool {
	switch t.Place {
	case PlaceDone:
		return now.Sub(t.DoneAt) > keepFor
	case PlaceWaiting:
		return now.Sub(t.WaitingSince) > keepFor
	}
	return false
}

func longestSince(feeds []string, cfg config.Config) time.Duration {
	if len(feeds) == 0 {
		return defaultSince
	}
	var longest time.Duration
	for _, name := range feeds {
		since := defaultSince
		for _, f := range cfg.Feeds {
			if f.Name == name {
				since = f.Since.Duration
				break
			}
		}
		longest = max(longest, since)
	}
	return longest
}

func unionSorted(a, b []string) []string {
	out := slices.Concat(a, b)
	slices.Sort(out)
	return slices.Compact(out)
}

// Hide moves a PR to waiting/hidden.
func Hide(t Tracked, now time.Time) Tracked {
	t.Place = PlaceWaiting
	t.WaitReason = WaitHidden
	t.WaitingSince = now
	t.WaitingHead = t.PR.HeadOID
	return t
}

// Unhide returns a PR to the wall and clears its wait fields.
func Unhide(t Tracked, now time.Time) Tracked {
	t.Place = PlaceWall
	t.WaitReason, t.WaitingSince, t.WaitingHead = "", time.Time{}, ""
	t.ReturnedAt = now
	return t
}

// NeedsDetail reports whether the PR is due a check this round: wall PRs
// always, waiting PRs when the idle poll is due, done PRs never. A check
// fetches full detail only if the PR changed.
func NeedsDetail(t Tracked, idleDue bool) bool {
	switch t.Place {
	case PlaceWall:
		return true
	case PlaceWaiting:
		return idleDue
	}
	return false
}

// OthersActivityAt is the latest of the head commit time and any review or
// comment by someone other than viewer.
func OthersActivityAt(pr github.Snapshot, viewer string) time.Time {
	latest := pr.HeadCommittedAt
	for _, r := range pr.Reviews {
		if r.Author != viewer && r.SubmittedAt.After(latest) {
			latest = r.SubmittedAt
		}
	}
	for _, c := range pr.Comments {
		if c.Author != viewer && c.At.After(latest) {
			latest = c.At
		}
	}
	return latest
}
