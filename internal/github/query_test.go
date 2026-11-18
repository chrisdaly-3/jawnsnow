package github

import (
	"strings"
	"testing"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
)

func assertEq[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestFeedQuery(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	f := config.Feed{Query: "is:pr is:open review-requested:@me", Since: config.Duration{Duration: 36 * time.Hour}}
	assertEq(t, FeedQuery(f, now), "is:pr is:open review-requested:@me draft:false updated:>=2026-11-17T00:00:00Z")
	f.IncludeDrafts = true
	assertEq(t, FeedQuery(f, now), "is:pr is:open review-requested:@me updated:>=2026-11-17T00:00:00Z")
	f.Since = config.Duration{Duration: 90 * time.Minute}
	assertEq(t, FeedQuery(f, now), "is:pr is:open review-requested:@me updated:>=2026-11-18T10:00:00Z")
}

func TestDetailQueryAliasesEachPR(t *testing.T) {
	q := DetailQuery(2)
	for _, want := range []string{
		"p0: node(id: $id0)", "p1: node(id: $id1)",
		"isRequired(pullRequestId: $id0)", "isRequired(pullRequestId: $id1)",
		"rateLimit", "$id0: ID!", "$id1: ID!",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("DetailQuery(2) missing %q", want)
		}
	}
	if strings.Contains(q, "p2:") {
		t.Error("DetailQuery(2) has a third alias")
	}
}
