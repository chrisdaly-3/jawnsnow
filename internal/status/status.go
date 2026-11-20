// Package status derives the single status shown for each PR, following the
// precedence table in the spec. It is a pure function over github.Snapshot.
package status

import "github.com/chrisdaly-3/jawnsnow/internal/github"

// Status values match the theme's status keys exactly.
type Status string

const (
	Merged           Status = "MERGED"
	Closed           Status = "CLOSED"
	Approved         Status = "APPROVED"
	CIFailed         Status = "CI_FAILED"
	NewChanges       Status = "NEW_CHANGES"
	ApprovedByYou    Status = "APPROVED_BY_YOU"
	ChangesRequested Status = "CHANGES_REQUESTED"
	CIWaiting        Status = "CI_WAITING"
	ReadyForReview   Status = "READY_FOR_REVIEW"
)

// Lane is the Wall column a status belongs to.
type Lane int

const (
	LaneCI Lane = iota
	LaneReady
	LaneNew
	LaneAuthor
	LaneApproved
	LaneDone
)

type Result struct {
	Status     Status
	CI         CI
	YourReview *github.Review
	Spinning   bool
}

type Input struct {
	PR           github.Snapshot
	Viewer       string
	IgnoreChecks []string
}

// Derive gives the PR exactly one status; the first matching rule wins.
func Derive(in Input) Result {
	pr := in.PR
	ci := EvalCI(pr.Checks, in.IgnoreChecks)
	mine := YourLatestReview(pr.Reviews, in.Viewer)
	res := Result{CI: ci, YourReview: mine}

	switch {
	case pr.State == github.PRMerged:
		res.Status = Merged
	case pr.State == github.PRClosed:
		res.Status = Closed
	case approvedByPeople(pr):
		res.Status = Approved
	case ci == CIFailedState:
		res.Status = CIFailed
	case mine != nil && newChanges(pr, mine):
		res.Status = NewChanges
		res.Spinning = ci == CIPending
	case mine != nil && mine.State == "APPROVED":
		res.Status = ApprovedByYou
	case mine != nil:
		res.Status = ChangesRequested
	case ci == CIPending:
		res.Status = CIWaiting
		res.Spinning = true
	default:
		res.Status = ReadyForReview
	}
	return res
}

// newChanges reports whether the PR moved since the viewer's review, ignoring
// merges of the base branch. Only the last 100 commits are fetched, so a
// review commit that is absent from Commits (a force-push, or a PR longer than
// the window) can't be traced and counts as new changes. The same goes for a
// head commit that is absent along with the review commit.
func newChanges(pr github.Snapshot, review *github.Review) bool {
	if pr.HeadOID == review.CommitOID {
		return false
	}
	return !OnlyBaseMergesSince(pr.Commits, review.CommitOID)
}

func (s Status) Lane() Lane {
	switch s {
	case CIWaiting, CIFailed:
		return LaneCI
	case ReadyForReview:
		return LaneReady
	case NewChanges:
		return LaneNew
	case ChangesRequested:
		return LaneAuthor
	case Approved, ApprovedByYou:
		return LaneApproved
	default: // Merged, Closed
		return LaneDone
	}
}
