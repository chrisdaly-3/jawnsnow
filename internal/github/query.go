package github

import (
	"fmt"
	"strings"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
)

const rateLimitFields = `rateLimit{remaining cost resetAt}`

const viewerQuery = `query Viewer { viewer { login } ` + rateLimitFields + ` }`

const searchQuery = `query Search($q: String!, $after: String) {
  search(query: $q, type: ISSUE, first: 50, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes { ... on PullRequest { id } }
  }
  ` + rateLimitFields + `
}`

// checkContextNodes is the selection for a page of check contexts. %s is the
// variable holding the pull request ID that isRequired is evaluated against.
const checkContextNodes = `pageInfo{hasNextPage endCursor} nodes{__typename ` +
	`... on CheckRun{name status conclusion isRequired(pullRequestId: %[1]s)} ` +
	`... on StatusContext{context state isRequired(pullRequestId: %[1]s)}}`

// Each alias inlines its own ID variable because isRequired takes the pull
// request ID, which rules out a shared fragment. %[1]d is the alias index.
const prSelection = `id number title url state isDraft updatedAt mergedAt closedAt additions deletions baseRefName headRefOid reviewDecision
    author{login} repository{nameWithOwner} labels(first:20){nodes{name}}
    head: commits(last:1){nodes{commit{oid committedDate statusCheckRollup{contexts(first:100){` + checkContextNodes + `}}}}}
    commits(last:100){nodes{commit{oid parents(first:2){nodes{oid}}}}}
    reviews(last:50){nodes{author{login} state body submittedAt commit{oid}}}
    comments(last:20){nodes{author{login} createdAt}}`

// checksPageQuery fetches the next page of check contexts for one pull request.
var checksPageQuery = `query ChecksPage($id: ID!, $after: String) {
  node(id: $id) { ... on PullRequest { commits(last:1){nodes{commit{statusCheckRollup{contexts(first:100, after: $after){` +
	fmt.Sprintf(checkContextNodes, "$id") + `}}}}} } }
  ` + rateLimitFields + `
}`

// FeedQuery turns a feed into a GitHub search string, bounded to PRs updated
// within the feed's window. The bound is truncated to the UTC day (or hour for
// windows under a day) so the string, and so the search cache key, is stable.
func FeedQuery(f config.Feed, now time.Time) string {
	q := f.Query
	if !f.IncludeDrafts {
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
	return q
}

// DetailQuery builds a query that fetches n pull requests as aliases p0..p{n-1},
// taking their node IDs from variables $id0..$id{n-1}.
func DetailQuery(n int) string {
	var decl, body strings.Builder
	for i := range n {
		if i > 0 {
			decl.WriteString(", ")
		}
		fmt.Fprintf(&decl, "$id%d: ID!", i)
		fmt.Fprintf(&body, "  p%d: node(id: $id%d) { ... on PullRequest {\n    %s\n  } }\n",
			i, i, fmt.Sprintf(prSelection, fmt.Sprintf("$id%d", i)))
	}
	return fmt.Sprintf("query Details(%s) {\n%s  %s\n}", decl.String(), body.String(), rateLimitFields)
}
