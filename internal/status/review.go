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
