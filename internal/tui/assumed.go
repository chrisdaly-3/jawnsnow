package tui

import (
	"maps"
	"slices"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
)

// assumedReview is a review this session posted, which the PR's snapshot
// may not show yet. at is when the post returned.
type assumedReview struct {
	review github.Review
	at     time.Time
}

// reviewStates maps a post's event to the state GitHub gives the review.
var reviewStates = map[string]string{
	"APPROVE":         "APPROVED",
	"REQUEST_CHANGES": "CHANGES_REQUESTED",
	"COMMENT":         "COMMENTED",
}

// assumePosted puts a review that just posted in its PR's snapshot, so the
// card moves lanes now. The poll after a post can't be relied on to: until
// GitHub's copy of the PR shows the review, the PR's fingerprint hasn't
// moved and the poll fetches no detail. The review stays until a poll that
// started after the post brings a snapshot of the PR.
func (m Model) assumePosted(done postDoneMsg) Model {
	state, ok := reviewStates[done.Event]
	if _, tracked := m.prs[done.PRID]; !ok || !tracked || m.viewer == "" {
		return m
	}
	m.assumed = maps.Clone(m.assumed)
	if m.assumed == nil {
		m.assumed = map[string]assumedReview{}
	}
	m.assumed[done.PRID] = assumedReview{
		review: github.Review{
			Author: m.viewer, State: state, Body: done.Body, CommitOID: done.Posted.CommitOID,
			SubmittedAt: done.Posted.At, NewThreads: done.NewThreads,
		},
		at: done.Posted.At,
	}
	return m.applyAssumed()
}

// settleAssumed runs after a poll that started at started: GitHub's word
// replaces an assumed review once such a poll brings a snapshot of its PR,
// and a poll that started before the post can't take the review away.
func (m Model) settleAssumed(snaps []github.Snapshot, started time.Time) Model {
	if len(m.assumed) == 0 {
		return m
	}
	m.assumed = maps.Clone(m.assumed)
	for _, s := range snaps {
		if a, ok := m.assumed[s.ID]; ok && !started.Before(a.at) {
			delete(m.assumed, s.ID)
		}
	}
	maps.DeleteFunc(m.assumed, func(id string, _ assumedReview) bool {
		_, ok := m.prs[id]
		return !ok
	})
	return m.applyAssumed()
}

// applyAssumed adds each assumed review its PR's snapshot lacks, then
// derives the statuses again.
func (m Model) applyAssumed() Model {
	changed := false
	for id, a := range m.assumed {
		t := m.prs[id]
		if slices.ContainsFunc(t.PR.Reviews, func(r github.Review) bool { return sameReview(r, a.review) }) {
			continue
		}
		if !changed {
			m.prs, changed = maps.Clone(m.prs), true
		}
		t.PR.Reviews = append(slices.Clone(t.PR.Reviews), a.review)
		m.prs[id] = t
	}
	if changed {
		m.prs = tracker.Recompute(m.prs, m.cfg, m.viewer)
	}
	return m
}

func sameReview(a, b github.Review) bool {
	return a.Author == b.Author && a.State == b.State && a.Body == b.Body &&
		a.CommitOID == b.CommitOID && a.SubmittedAt.Equal(b.SubmittedAt) && a.NewThreads == b.NewThreads
}
