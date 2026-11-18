package poll

import (
	"context"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/github"
)

// fakeClient answers Search by matching the feed query as a prefix of the
// search string FeedQuery builds. Every PR's fingerprint comes from fps, or
// is just its ID.
type fakeClient struct {
	search     map[string][]string
	searchRate map[string]github.RateLimit
	feedErr    map[string]error // by feed query
	fps        map[string]github.Fingerprint
	fpErr      error
	fpSkip     map[string]string // ID → GraphQL error type
	detailRate github.RateLimit
	detailErr  error
	detailSkip map[string]string // ID → GraphQL error type

	fpCalls     [][]string
	detailCalls [][]string
	viewers     []string
}

func (f *fakeClient) Viewer(context.Context) (string, error) { return "me", nil }

func (f *fakeClient) fingerprint(id string) github.Fingerprint {
	if fp, ok := f.fps[id]; ok {
		return fp
	}
	return github.Fingerprint{ID: id}
}

func (f *fakeClient) Search(_ context.Context, q string) ([]github.Fingerprint, github.RateLimit, error) {
	for prefix, err := range f.feedErr {
		if strings.HasPrefix(q, prefix+" ") {
			return nil, github.RateLimit{}, err
		}
	}
	for prefix, ids := range f.search {
		if strings.HasPrefix(q, prefix+" ") {
			var out []github.Fingerprint
			for _, id := range ids {
				out = append(out, f.fingerprint(id))
			}
			return out, f.searchRate[prefix], nil
		}
	}
	return nil, github.RateLimit{}, nil
}

// Fingerprints leaves out "gone", as the client does a PR that no longer
// resolves, and reports it as skipped with NOT_FOUND; likewise the PRs in
// fpSkip, with their type.
func (f *fakeClient) Fingerprints(_ context.Context, ids []string) ([]github.Fingerprint, []github.SkippedPR, github.RateLimit, error) {
	f.fpCalls = append(f.fpCalls, slices.Clone(ids))
	if f.fpErr != nil {
		return nil, nil, github.RateLimit{}, f.fpErr
	}
	var out []github.Fingerprint
	var skipped []github.SkippedPR
	for _, id := range ids {
		if id == "gone" {
			skipped = append(skipped, github.SkippedPR{ID: id, Type: "NOT_FOUND"})
			continue
		}
		if typ, ok := f.fpSkip[id]; ok {
			skipped = append(skipped, github.SkippedPR{ID: id, Type: typ})
			continue
		}
		out = append(out, f.fingerprint(id))
	}
	return out, skipped, github.RateLimit{}, nil
}

// Details leaves out the PRs in detailSkip and reports them as skipped.
func (f *fakeClient) Details(_ context.Context, viewer string, ids []string) ([]github.Snapshot, []github.SkippedPR, github.RateLimit, error) {
	f.detailCalls = append(f.detailCalls, slices.Clone(ids))
	f.viewers = append(f.viewers, viewer)
	if f.detailErr != nil {
		return nil, nil, github.RateLimit{}, f.detailErr
	}
	var out []github.Snapshot
	var skipped []github.SkippedPR
	for _, id := range ids {
		if typ, ok := f.detailSkip[id]; ok {
			skipped = append(skipped, github.SkippedPR{ID: id, Type: typ})
			continue
		}
		out = append(out, github.Snapshot{ID: id})
	}
	return out, skipped, f.detailRate, nil
}

var (
	feedA = config.Feed{Name: "A", Query: "is:pr repo:a"}
	feedB = config.Feed{Name: "B", Query: "is:pr repo:b"}
	now   = time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)
)

func TestFetchUnionsFeedsAndDetails(t *testing.T) {
	c := &fakeClient{search: map[string][]string{
		feedA.Query: {"1", "2"},
		feedB.Query: {"2", "3"},
	}}
	res := Fetch(context.Background(), c, Request{Viewer: "me", Feeds: []config.Feed{feedA, feedB}, DetailIDs: []string{"4"}, Now: now})
	if res.Err != nil {
		t.Fatalf("Err = %v", res.Err)
	}
	if !reflect.DeepEqual(c.viewers, []string{"me"}) {
		t.Errorf("Details viewers = %v, want [me]", c.viewers)
	}
	wantFound := map[string][]string{"1": {"A"}, "2": {"A", "B"}, "3": {"B"}}
	if !reflect.DeepEqual(res.Found, wantFound) {
		t.Errorf("Found = %v, want %v", res.Found, wantFound)
	}
	if len(c.detailCalls) != 1 {
		t.Fatalf("Details called %d times, want 1", len(c.detailCalls))
	}
	if got, want := c.detailCalls[0], []string{"1", "2", "3", "4"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Details ids = %v, want %v", got, want)
	}
	if len(res.Snapshots) != 4 {
		t.Errorf("got %d snapshots, want 4", len(res.Snapshots))
	}
}

func TestFetchKeepsLowestRemaining(t *testing.T) {
	resetA := now.Add(10 * time.Minute)
	resetB := now.Add(20 * time.Minute)
	c := &fakeClient{
		search: map[string][]string{feedA.Query: {"1"}, feedB.Query: {"2"}},
		searchRate: map[string]github.RateLimit{
			feedA.Query: {Remaining: 4000, Cost: 1, ResetAt: resetA},
			feedB.Query: {Remaining: 3900, Cost: 1, ResetAt: resetB},
		},
		detailRate: github.RateLimit{Remaining: 3950, Cost: 2, ResetAt: resetA},
	}
	res := Fetch(context.Background(), c, Request{Feeds: []config.Feed{feedA, feedB}, Now: now})
	if res.Err != nil {
		t.Fatalf("Err = %v", res.Err)
	}
	want := github.RateLimit{Remaining: 3900, Cost: 4, ResetAt: resetB}
	if res.Rate != want {
		t.Errorf("Rate = %+v, want %+v", res.Rate, want)
	}
}

func TestFetchSkipsDetailsWhenNothingToFetch(t *testing.T) {
	c := &fakeClient{}
	res := Fetch(context.Background(), c, Request{Feeds: []config.Feed{feedA}, Now: now})
	if res.Err != nil {
		t.Fatalf("Err = %v", res.Err)
	}
	if len(c.detailCalls) != 0 {
		t.Errorf("Details called with %v, want no call", c.detailCalls)
	}
}

func TestFetchSkipsAFailingFeed(t *testing.T) {
	c := &fakeClient{
		search:  map[string][]string{feedB.Query: {"2"}},
		feedErr: map[string]error{feedA.Query: io.EOF},
	}
	res := Fetch(context.Background(), c, Request{Feeds: []config.Feed{feedA, feedB}, DetailIDs: []string{"4"}, Now: now})
	if res.Err != nil {
		t.Fatalf("Err = %v, want the poll to carry on without feed A", res.Err)
	}
	if want := map[string][]string{"2": {"B"}}; !reflect.DeepEqual(res.Found, want) {
		t.Errorf("Found = %v, want %v", res.Found, want)
	}
	if len(c.detailCalls) != 1 || !reflect.DeepEqual(c.detailCalls[0], []string{"2", "4"}) {
		t.Errorf("Details calls = %v, want [[2 4]]", c.detailCalls)
	}
	if len(res.FeedErrs) != 1 || !errors.Is(res.FeedErrs[0], io.EOF) || !strings.Contains(res.FeedErrs[0].Error(), `"A"`) {
		t.Errorf("FeedErrs = %v, want one error naming feed A", res.FeedErrs)
	}
}

func TestFetchFailsOnSearchRateLimit(t *testing.T) {
	limited := &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: "RATE_LIMITED"}}}
	c := &fakeClient{
		search:  map[string][]string{feedB.Query: {"2"}},
		feedErr: map[string]error{feedA.Query: limited},
	}
	res := Fetch(context.Background(), c, Request{Feeds: []config.Feed{feedA, feedB}, DetailIDs: []string{"4"}, Now: now})
	if !github.IsRateLimited(res.Err) {
		t.Fatalf("Err = %v, want a rate-limit error", res.Err)
	}
	if !strings.Contains(res.Err.Error(), `"A"`) {
		t.Errorf("Err = %q, want it to name the feed", res.Err)
	}
	if len(c.detailCalls) != 0 {
		t.Errorf("Details called after a rate-limited search")
	}
}

func TestFetchReturnsDetailError(t *testing.T) {
	c := &fakeClient{detailErr: io.ErrUnexpectedEOF}
	res := Fetch(context.Background(), c, Request{DetailIDs: []string{"4"}, Now: now})
	if !errors.Is(res.Err, io.ErrUnexpectedEOF) {
		t.Fatalf("Err = %v, want io.ErrUnexpectedEOF", res.Err)
	}
}

var (
	fpAt      = time.Date(2026, 11, 18, 14, 0, 0, 0, time.UTC)
	unchanged = github.Fingerprint{ID: "1", UpdatedAt: fpAt, HeadOID: "h1", RollupState: "SUCCESS"}
)

func TestFetchSkipsDetailForUnchangedPRs(t *testing.T) {
	c := &fakeClient{
		search: map[string][]string{feedA.Query: {"1"}},
		fps:    map[string]github.Fingerprint{"1": unchanged, "2": {ID: "2", HeadOID: "h2"}},
	}
	res := Fetch(context.Background(), c, Request{
		Feeds: []config.Feed{feedA}, Now: now,
		Known: map[string]github.Fingerprint{"1": unchanged, "2": {ID: "2", HeadOID: "h2"}},
		Watch: []string{"1", "2"},
	})
	if res.Err != nil {
		t.Fatalf("Err = %v", res.Err)
	}
	if len(c.detailCalls) != 0 {
		t.Errorf("Details calls = %v, want none for unchanged PRs", c.detailCalls)
	}
	// 1 came back from the search, so only 2 needs a lookup.
	if !reflect.DeepEqual(c.fpCalls, [][]string{{"2"}}) {
		t.Errorf("Fingerprints calls = %v, want [[2]]", c.fpCalls)
	}
}

func TestFetchDetailsChangedAndNewPRs(t *testing.T) {
	for name, fresh := range map[string]github.Fingerprint{
		"rollup":    {ID: "1", UpdatedAt: fpAt, HeadOID: "h1", RollupState: "FAILURE"},
		"updatedAt": {ID: "1", UpdatedAt: fpAt.Add(time.Minute), HeadOID: "h1", RollupState: "SUCCESS"},
		"head":      {ID: "1", UpdatedAt: fpAt, HeadOID: "h2", RollupState: "SUCCESS"},
	} {
		t.Run(name, func(t *testing.T) {
			c := &fakeClient{
				search: map[string][]string{feedA.Query: {"1", "new"}},
				fps:    map[string]github.Fingerprint{"1": fresh, "3": {ID: "3"}},
			}
			res := Fetch(context.Background(), c, Request{
				Feeds: []config.Feed{feedA}, Now: now,
				Known: map[string]github.Fingerprint{"1": unchanged, "3": {ID: "3"}},
				Watch: []string{"1", "3"},
			})
			if res.Err != nil {
				t.Fatalf("Err = %v", res.Err)
			}
			if !reflect.DeepEqual(c.detailCalls, [][]string{{"1", "new"}}) {
				t.Errorf("Details calls = %v, want [[1 new]]", c.detailCalls)
			}
		})
	}
}

func TestFetchDetailsWatchedPRThatChanged(t *testing.T) {
	moved := unchanged
	moved.HeadOID = "h9"
	c := &fakeClient{fps: map[string]github.Fingerprint{"1": moved}}
	res := Fetch(context.Background(), c, Request{
		Feeds: []config.Feed{feedA}, Now: now,
		Known: map[string]github.Fingerprint{"1": unchanged, "gone": {ID: "gone"}},
		Watch: []string{"1", "gone"},
	})
	if res.Err != nil {
		t.Fatalf("Err = %v", res.Err)
	}
	if !reflect.DeepEqual(c.fpCalls, [][]string{{"1", "gone"}}) {
		t.Errorf("Fingerprints calls = %v, want [[1 gone]]", c.fpCalls)
	}
	// A PR that no longer resolves gets no detail call and keeps its state.
	if !reflect.DeepEqual(c.detailCalls, [][]string{{"1"}}) {
		t.Errorf("Details calls = %v, want [[1]]", c.detailCalls)
	}
}

func TestFetchDetailIDsAreAlwaysFetched(t *testing.T) {
	c := &fakeClient{fps: map[string]github.Fingerprint{"1": unchanged}}
	Fetch(context.Background(), c, Request{
		Now: now, Known: map[string]github.Fingerprint{"1": unchanged}, Watch: []string{"1"}, DetailIDs: []string{"1"},
	})
	if !reflect.DeepEqual(c.detailCalls, [][]string{{"1"}}) {
		t.Errorf("Details calls = %v, want [[1]] for a forced refresh", c.detailCalls)
	}
}

func TestFetchReturnsFingerprintError(t *testing.T) {
	c := &fakeClient{fpErr: io.ErrUnexpectedEOF}
	res := Fetch(context.Background(), c, Request{Now: now, Watch: []string{"1"}})
	if !errors.Is(res.Err, io.ErrUnexpectedEOF) {
		t.Fatalf("Err = %v, want io.ErrUnexpectedEOF", res.Err)
	}
	if len(c.detailCalls) != 0 {
		t.Errorf("Details called after a failed lookup")
	}
}

func TestFetchReportsUnreachablePRs(t *testing.T) {
	moved := unchanged
	moved.HeadOID = "h9"
	c := &fakeClient{
		fps:        map[string]github.Fingerprint{"1": moved},
		detailSkip: map[string]string{"1": "FORBIDDEN", "gone": "NOT_FOUND"},
	}
	res := Fetch(context.Background(), c, Request{
		Now: now, Known: map[string]github.Fingerprint{"1": unchanged, "gone": {ID: "gone"}},
		Watch: []string{"1", "gone"}, DetailIDs: []string{"gone"},
	})
	if res.Err != nil {
		t.Fatalf("Err = %v, want a poll that went on without the unreachable PRs", res.Err)
	}
	// gone is skipped by both the lookup and the detail fetch.
	want := []github.SkippedPR{{ID: "1", Type: "FORBIDDEN"}, {ID: "gone", Type: "NOT_FOUND"}}
	if !reflect.DeepEqual(res.Unreachable, want) {
		t.Errorf("Unreachable = %+v, want %+v", res.Unreachable, want)
	}
}

func TestFetchFailsWhenEveryFeedFails(t *testing.T) {
	c := &fakeClient{feedErr: map[string]error{feedA.Query: io.EOF, feedB.Query: io.ErrUnexpectedEOF}}
	res := Fetch(context.Background(), c, Request{Feeds: []config.Feed{feedA, feedB}, Now: now})
	if !errors.Is(res.Err, io.EOF) || !errors.Is(res.Err, io.ErrUnexpectedEOF) {
		t.Fatalf("Err = %v, want both feed errors", res.Err)
	}
	if len(res.FeedErrs) != 2 {
		t.Errorf("FeedErrs = %v, want both", res.FeedErrs)
	}
}

func TestFetchWithEveryFeedFailingStillChecksTrackedPRs(t *testing.T) {
	c := &fakeClient{feedErr: map[string]error{feedA.Query: io.EOF}}
	res := Fetch(context.Background(), c, Request{Feeds: []config.Feed{feedA}, Watch: []string{"1"}, Now: now})
	if res.Err != nil {
		t.Fatalf("Err = %v, want the tracked PR checked without the feed", res.Err)
	}
	if !reflect.DeepEqual(c.fpCalls, [][]string{{"1"}}) {
		t.Errorf("Fingerprints calls = %v, want [[1]]", c.fpCalls)
	}
}

func TestFetchPrefersARateLimitWhenEveryFeedFails(t *testing.T) {
	limited := &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: "RATE_LIMITED"}}}
	c := &fakeClient{feedErr: map[string]error{feedA.Query: io.EOF, feedB.Query: limited}}
	res := Fetch(context.Background(), c, Request{Feeds: []config.Feed{feedA, feedB}, Now: now})
	if !github.IsRateLimited(res.Err) {
		t.Fatalf("Err = %v, want the rate limit", res.Err)
	}
}

// A feed that reached PRs makes the poll a success even when every watched
// PR it didn't return is unreachable.
func TestFetchSucceedsWhenOnlyWatchedPRsAreUnreachable(t *testing.T) {
	c := &fakeClient{
		search: map[string][]string{feedA.Query: {"1"}},
		fpSkip: map[string]string{"2": "FORBIDDEN", "3": "FORBIDDEN"},
	}
	res := Fetch(context.Background(), c, Request{Feeds: []config.Feed{feedA}, Watch: []string{"2", "3"}, Now: now})
	if res.Err != nil {
		t.Fatalf("Err = %v, want the feed's results", res.Err)
	}
	if len(res.Snapshots) != 1 || res.Snapshots[0].ID != "1" {
		t.Errorf("Snapshots = %+v, want PR 1 from the feed", res.Snapshots)
	}
	if len(res.Unreachable) != 2 {
		t.Errorf("Unreachable = %+v, want PRs 2 and 3", res.Unreachable)
	}
}

func TestFetchFailsWhenItReachesNoPR(t *testing.T) {
	for name, feedErr := range map[string]map[string]error{
		"feeds fail":         {feedA.Query: io.EOF},
		"feeds find nothing": nil,
	} {
		t.Run(name, func(t *testing.T) {
			c := &fakeClient{
				feedErr:    feedErr,
				fps:        map[string]github.Fingerprint{"1": unchanged},
				fpSkip:     map[string]string{"1": "FORBIDDEN"},
				detailSkip: map[string]string{"1": "FORBIDDEN", "2": "NOT_FOUND"},
			}
			res := Fetch(context.Background(), c, Request{
				Feeds: []config.Feed{feedA}, Now: now, Watch: []string{"1"}, DetailIDs: []string{"1", "2"},
			})
			if res.Err == nil || res.Err.Error() != "all 2 PRs unreachable: FORBIDDEN, NOT_FOUND" {
				t.Fatalf("Err = %v, want the poll failed naming both types", res.Err)
			}
			if github.IsRateLimited(res.Err) {
				t.Error("an unreachable PR reads as a rate limit")
			}
			if len(res.Unreachable) != 2 {
				t.Errorf("Unreachable = %+v, want PRs 1 and 2", res.Unreachable)
			}
		})
	}
}

// One lookup reaching a PR is enough, even when the feeds fail.
func TestFetchSucceedsWhenALookupReachesAPR(t *testing.T) {
	c := &fakeClient{
		feedErr: map[string]error{feedA.Query: io.EOF},
		fpSkip:  map[string]string{"2": "FORBIDDEN"},
	}
	res := Fetch(context.Background(), c, Request{Feeds: []config.Feed{feedA}, Watch: []string{"1", "2"}, Now: now})
	if res.Err != nil {
		t.Fatalf("Err = %v, want PR 1's lookup to count", res.Err)
	}
	if len(res.Unreachable) != 1 || res.Unreachable[0].ID != "2" {
		t.Errorf("Unreachable = %+v, want PR 2", res.Unreachable)
	}
}
