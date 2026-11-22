package github

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
)

const rateLimitFields = `rateLimit{remaining cost resetAt}`

const viewerQuery = `query Viewer { viewer { login } ` + rateLimitFields + ` }`

// fingerprintFields is the cheap selection that tells whether a PR changed:
// no check contexts, so no isRequired.
const fingerprintFields = `... on PullRequest { id updatedAt headRefOid commits(last:1){nodes{commit{statusCheckRollup{state}}}} }`

const searchQuery = `query Search($q: String!, $after: String) {
  search(query: $q, type: ISSUE, first: 50, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes { ` + fingerprintFields + ` }
  }
  ` + rateLimitFields + `
}`

const fingerprintsQuery = `query Fingerprints($ids: [ID!]!) {
  nodes(ids: $ids) { ` + fingerprintFields + ` }
  ` + rateLimitFields + `
}`

// checkContextNodes is the selection for a page of check contexts. %s is the
// variable holding the pull request ID that isRequired is evaluated against.
const checkContextNodes = `pageInfo{hasNextPage endCursor} nodes{__typename ` +
	`... on CheckRun{name status conclusion detailsUrl isRequired(pullRequestId: %[1]s)} ` +
	`... on StatusContext{context state targetUrl isRequired(pullRequestId: %[1]s)}}`

const reviewNodes = `nodes{author{__typename login} state body submittedAt commit{oid}}`

const mineReviewNodes = `nodes{author{__typename login} state body submittedAt commit{oid} comments(first:5){nodes{replyTo{id}}}}`

// Each alias inlines its own ID variable because isRequired takes the pull
// request ID, which rules out a shared fragment. %[1]s is that variable.
// Thread replies and bot comments are reviews too, so on a busy PR the
// viewer's own review can fall outside the last 50; "mine" fetches it by
// author. Only "mine" asks for each review's first five comments, to tell an
// inline-only review from an empty thread reply without paying for it on all
// 50. A review whose first five comments are all replies yet has more is rare
// enough that it's counted as having no new threads.
const prSelection = `id number title url state isDraft updatedAt mergedAt closedAt additions deletions baseRefName headRefOid reviewDecision
    author{login} repository{nameWithOwner} labels(first:20){nodes{name}}
    head: commits(last:1){nodes{commit{oid committedDate statusCheckRollup{state contexts(first:100){` + checkContextNodes + `}}}}}
    commits(last:100){nodes{commit{oid parents(first:2){nodes{oid}}}}}
    reviews(last:50){` + reviewNodes + `}
    mine: reviews(last: 10, author: $login){` + mineReviewNodes + `}
    comments(last:20){nodes{author{login} createdAt}}`

// checksPageQuery fetches the next page of check contexts for one pull request.
var checksPageQuery = `query ChecksPage($id: ID!, $after: String) {
  node(id: $id) { ... on PullRequest { commits(last:1){nodes{commit{statusCheckRollup{contexts(first:100, after: $after){` +
	fmt.Sprintf(checkContextNodes, "$id") + `}}}}} } }
  ` + rateLimitFields + `
}`

// draftQualifier finds a draft: or -draft: qualifier in a search string.
var draftQualifier = regexp.MustCompile(`(^|\s)-?draft:`)

// FeedQuery turns a feed into a GitHub search string. It adds draft:false
// unless the feed's query has its own draft: qualifier, and bounds the search
// to PRs updated within the feed's window. The bound is truncated to the UTC
// day (or hour for windows under a day) so the string, and so the search
// cache key, is stable.
// Search returns at most 100 PRs; sorting by update makes the ones it drops
// the least recently active.
func FeedQuery(f config.Feed, now time.Time) string {
	q := f.Query
	if !draftQualifier.MatchString(q) {
		q += " draft:false"
	}
	if f.Since.Duration > 0 {
		unit := time.Hour
		if f.Since.Duration >= 24*time.Hour {
			unit = 24 * time.Hour
		}
		since := now.Add(-f.Since.Duration).UTC().Truncate(unit)
		q += " updated:>=" + since.Format(time.RFC3339)
	}
	return q + " sort:updated-desc"
}

// DetailQuery builds a query that fetches n pull requests as aliases p0..p{n-1},
// taking their node IDs from variables $id0..$id{n-1} and the viewer's login
// from $login.
func DetailQuery(n int) string {
	var decl, body strings.Builder
	decl.WriteString("$login: String!")
	for i := range n {
		fmt.Fprintf(&decl, ", $id%d: ID!", i)
		fmt.Fprintf(&body, "  p%d: node(id: $id%d) { ... on PullRequest {\n    %s\n  } }\n",
			i, i, fmt.Sprintf(prSelection, fmt.Sprintf("$id%d", i)))
	}
	return fmt.Sprintf("query Details(%s) {\n%s  %s\n}", decl.String(), body.String(), rateLimitFields)
}
