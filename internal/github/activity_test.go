package github

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseActivity(t *testing.T) {
	d := &fakeDoer{respond: func(int, string, map[string]any) []byte { return fixture(t, "activity.json") }}
	items, rl, err := NewClient(d).Activity(context.Background(), "PR_7")
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, rl.Cost, 1)
	assertEq(t, d.calls[0].vars["id"], any("PR_7"))
	// An unknown kind is left out; the rest stay oldest first.
	assertEq(t, len(items), 6)
	assertEq(t, items[0], ActivityItem{Kind: ActivityCommit, Author: "alice", At: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC),
		URL: "https://github.com/acme/widgets/pull/7/commits/aaa111", Text: "Add the retry budget", OID: "aaa111"})
	assertEq(t, items[1].Kind, ActivityComment)
	assertEq(t, items[1].Bot, true)
	assertEq(t, items[1].Text, "Preview deployed.\nDetails inside.")
	assertEq(t, items[2], ActivityItem{Kind: ActivityReview, Author: "me", At: time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC),
		URL: "https://github.com/acme/widgets/pull/7#pullrequestreview-2", Text: "Two problems, see inline.",
		State: "CHANGES_REQUESTED", InlineComments: 3})
	assertEq(t, items[3], ActivityItem{Kind: ActivityForcePush, Author: "alice", At: time.Date(2026, 11, 18, 11, 0, 0, 0, time.UTC),
		OID: "bbb222", BeforeOID: "aaa111"})
	// A commit by a bot has no linked user and a [bot] name (G8).
	assertEq(t, items[4].Author, "renovate[bot]")
	assertEq(t, items[4].Bot, true)
	assertEq(t, items[5].Author, "")
	assertEq(t, items[5].Bot, false)
}

func TestActivityQuery(t *testing.T) {
	for _, want := range []string{
		"timelineItems(last: 50, itemTypes: [PULL_REQUEST_COMMIT, HEAD_REF_FORCE_PUSHED_EVENT, PULL_REQUEST_REVIEW, ISSUE_COMMENT])",
		"author { __typename login } bodyText comments { totalCount }",
		"rateLimit{remaining cost resetAt}",
	} {
		if !strings.Contains(ActivityQuery, want) {
			t.Errorf("ActivityQuery missing %q", want)
		}
	}
}
