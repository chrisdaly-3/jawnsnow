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
	assertEq(t, FeedQuery(f, now), "is:pr is:open review-requested:@me draft:false updated:>=2026-11-17T00:00:00Z sort:updated-desc")
	f.IncludeDrafts = true
	assertEq(t, FeedQuery(f, now), "is:pr is:open review-requested:@me updated:>=2026-11-17T00:00:00Z sort:updated-desc")
	f.Since = config.Duration{Duration: 90 * time.Minute}
	assertEq(t, FeedQuery(f, now), "is:pr is:open review-requested:@me updated:>=2026-11-18T10:00:00Z sort:updated-desc")
}

func TestDetailQueryAliasesEachPR(t *testing.T) {
	q := DetailQuery(2)
	for _, want := range []string{
		"p0: node(id: $id0)", "p1: node(id: $id1)",
		"isRequired(pullRequestId: $id0)", "isRequired(pullRequestId: $id1)",
		"rateLimit", "$id0: ID!", "$id1: ID!", "$login: String!",
		"reviews(last:50)", "mine: reviews(last: 10, author: $login)",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("DetailQuery(2) missing %q", want)
		}
	}
	if strings.Contains(q, "p2:") {
		t.Error("DetailQuery(2) has a third alias")
	}
}

func TestFingerprintQueriesAreLight(t *testing.T) {
	for name, q := range map[string]string{"search": searchQuery, "nodes": fingerprintsQuery} {
		for _, want := range []string{"updatedAt", "headRefOid", "statusCheckRollup{state}", "rateLimit"} {
			if !strings.Contains(q, want) {
				t.Errorf("%s query missing %q", name, want)
			}
		}
		if strings.Contains(q, "isRequired") || strings.Contains(q, "reviews") {
			t.Errorf("%s query fetches detail:\n%s", name, q)
		}
	}
	if !strings.Contains(fingerprintsQuery, "nodes(ids: $ids)") {
		t.Errorf("fingerprints query does not batch by node IDs:\n%s", fingerprintsQuery)
	}
}
