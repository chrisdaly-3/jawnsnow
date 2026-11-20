package github

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// ActivityKind is what an ActivityItem records.
type ActivityKind string

const (
	ActivityCommit    ActivityKind = "commit"
	ActivityForcePush ActivityKind = "force-push"
	ActivityReview    ActivityKind = "review"
	ActivityComment   ActivityKind = "comment"
)

// ActivityItem is one entry in a PR's timeline (plan 5): a commit, a
// force-push, a review or a conversation comment.
type ActivityItem struct {
	Kind   ActivityKind
	Author string // the login, or a commit's git author name; "" for a deleted user
	Bot    bool
	At     time.Time
	URL    string // "" for a force-push, which has no page of its own
	// Text is a commit's headline, or a review's or comment's body.
	Text string
	// State is a review's: APPROVED, CHANGES_REQUESTED, COMMENTED or
	// DISMISSED.
	State          string
	OID, BeforeOID string // a commit's, or a force-push's after and before
	InlineComments int    // a review's inline comments
}

// ActivityLimit is how many timeline items ActivityQuery asks for. A PR with
// that many may have older ones not fetched.
const ActivityLimit = 50

// ActivityQuery fetches a PR's last 50 commits, force-pushes, reviews and
// conversation comments, as the 2026-11-20 live call ran it.
const ActivityQuery = `query Activity($id: ID!) {
  node(id: $id) { ... on PullRequest {
    timelineItems(last: 50, itemTypes: [PULL_REQUEST_COMMIT, HEAD_REF_FORCE_PUSHED_EVENT, PULL_REQUEST_REVIEW, ISSUE_COMMENT]) {
      nodes {
        __typename
        ... on PullRequestCommit { url commit { oid messageHeadline committedDate author { name user { login } } } }
        ... on HeadRefForcePushedEvent { createdAt actor { __typename login } beforeCommit { oid } afterCommit { oid } }
        ... on PullRequestReview { url state submittedAt author { __typename login } bodyText comments { totalCount } }
        ... on IssueComment { url createdAt author { __typename login } bodyText }
      }
    }
  } }
  ` + rateLimitFields + `
}`

type oidNode struct {
	OID string `json:"oid"`
}

type activityNode struct {
	Typename string `json:"__typename"`
	URL      string `json:"url"`
	Commit   *struct {
		OID           string    `json:"oid"`
		Headline      string    `json:"messageHeadline"`
		CommittedDate time.Time `json:"committedDate"`
		Author        *struct {
			Name string     `json:"name"`
			User *loginNode `json:"user"`
		} `json:"author"`
	} `json:"commit"`
	CreatedAt    time.Time  `json:"createdAt"`
	SubmittedAt  time.Time  `json:"submittedAt"`
	Actor        *loginNode `json:"actor"`
	Author       *loginNode `json:"author"`
	BeforeCommit *oidNode   `json:"beforeCommit"`
	AfterCommit  *oidNode   `json:"afterCommit"`
	State        string     `json:"state"`
	BodyText     string     `json:"bodyText"`
	Comments     *struct {
		TotalCount int `json:"totalCount"`
	} `json:"comments"`
}

func isBot(a *loginNode) bool { return a != nil && a.Typename == "Bot" }

func oid(n *oidNode) string {
	if n == nil {
		return ""
	}
	return n.OID
}

// item converts a node, or reports false for a kind the tab doesn't list.
func (n activityNode) item() (ActivityItem, bool) {
	switch n.Typename {
	case "PullRequestCommit":
		if n.Commit == nil {
			return ActivityItem{}, false
		}
		it := ActivityItem{Kind: ActivityCommit, URL: n.URL, At: n.Commit.CommittedDate, Text: n.Commit.Headline, OID: n.Commit.OID}
		if a := n.Commit.Author; a != nil {
			// A commit has a git author, not an account: a bot is one with
			// no linked user whose name ends in [bot] (G8).
			it.Author = a.Name
			if a.User != nil {
				it.Author = a.User.Login
			} else {
				it.Bot = strings.HasSuffix(a.Name, "[bot]")
			}
		}
		return it, true
	case "HeadRefForcePushedEvent":
		return ActivityItem{Kind: ActivityForcePush, Author: login(n.Actor), Bot: isBot(n.Actor), At: n.CreatedAt,
			OID: oid(n.AfterCommit), BeforeOID: oid(n.BeforeCommit)}, true
	case "PullRequestReview":
		it := ActivityItem{Kind: ActivityReview, Author: login(n.Author), Bot: isBot(n.Author), At: n.SubmittedAt,
			URL: n.URL, Text: n.BodyText, State: n.State}
		if n.Comments != nil {
			it.InlineComments = n.Comments.TotalCount
		}
		return it, true
	case "IssueComment":
		return ActivityItem{Kind: ActivityComment, Author: login(n.Author), Bot: isBot(n.Author), At: n.CreatedAt,
			URL: n.URL, Text: n.BodyText}, true
	}
	return ActivityItem{}, false
}

func parseActivity(raw json.RawMessage) ([]ActivityItem, error) {
	var n struct {
		TimelineItems struct {
			Nodes []activityNode `json:"nodes"`
		} `json:"timelineItems"`
	}
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil, err
	}
	var out []ActivityItem
	for _, node := range n.TimelineItems.Nodes {
		if it, ok := node.item(); ok {
			out = append(out, it)
		}
	}
	return out, nil
}

func (c *client) Activity(ctx context.Context, id string) ([]ActivityItem, RateLimit, error) {
	raw, _, rl, err := c.do(ctx, ActivityQuery, map[string]any{"id": id}, nil)
	if err != nil {
		return nil, rl, err
	}
	items, err := parseActivity(raw["node"])
	return items, rl, err
}
