package status

import (
	"slices"
	"strings"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
)

// YourLatestReview returns the newest review by viewer that counts as a
// review: APPROVED, CHANGES_REQUESTED, DISMISSED, or COMMENTED with a
// non-blank body or at least one comment that starts a thread. PENDING reviews
// and COMMENTED reviews with no body whose comments are all replies (which is
// what GitHub creates for thread replies) are skipped. "Newest" is by SubmittedAt, not
// slice order.
func YourLatestReview(reviews []github.Review, viewer string) *github.Review {
	var best *github.Review
	for i := range reviews {
		r := &reviews[i]
		if r.Author != viewer || !counts(r) {
			continue
		}
		if best == nil || r.SubmittedAt.After(best.SubmittedAt) {
			best = r
		}
	}
	return best
}

// approvedByPeople reports whether at least two people approved (A9). Each
// reviewer counts once, by their latest approve, request-changes or
// dismissed review, so an approval later taken back doesn't count. Bots
// and deleted users don't count. GitHub's own decision must not disagree:
// it is APPROVED, or empty on a repo with no review rules.
func approvedByPeople(pr github.Snapshot) bool {
	if pr.ReviewDecision != "APPROVED" && pr.ReviewDecision != "" {
		return false
	}
	latest := map[string]*github.Review{}
	for i := range pr.Reviews {
		r := &pr.Reviews[i]
		if r.Bot || r.Author == "" {
			continue
		}
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
		default:
			continue
		}
		if l := latest[r.Author]; l == nil || r.SubmittedAt.After(l.SubmittedAt) {
			latest[r.Author] = r
		}
	}
	n := 0
	for _, r := range latest {
		if r.State == "APPROVED" {
			n++
		}
	}
	return n >= 2
}

func counts(r *github.Review) bool {
	switch r.State {
	case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
		return true
	case "COMMENTED":
		return strings.TrimSpace(r.Body) != "" || r.NewThreads > 0
	}
	return false
}

// OnlyBaseMergesSince reports whether every commit after sinceOID is a merge
// that pulled in the base branch: it has two parents and at least one of them
// is not among the PR's own commits. It works from the GraphQL data alone.
// If sinceOID is not in commits (for example after a force-push) it returns
// false, so the caller treats the change as new.
func OnlyBaseMergesSince(commits []github.Commit, sinceOID string) bool {
	idx := slices.IndexFunc(commits, func(c github.Commit) bool { return c.OID == sinceOID })
	if idx < 0 {
		return false
	}
	own := make(map[string]bool, len(commits))
	for _, c := range commits {
		own[c.OID] = true
	}
	for _, c := range commits[idx+1:] {
		if len(c.Parents) != 2 || (own[c.Parents[0]] && own[c.Parents[1]]) {
			return false
		}
	}
	return true
}
