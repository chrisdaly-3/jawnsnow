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

// fakeClient answers SearchIDs by matching the feed query as a prefix of the
// search string FeedQuery builds.
type fakeClient struct {
	search     map[string][]string
	searchRate map[string]github.RateLimit
	feedErr    map[string]error // by feed query
	detailRate github.RateLimit
	detailErr  error

	detailCalls [][]string
}

func (f *fakeClient) Viewer(context.Context) (string, error) { return "me", nil }

func (f *fakeClient) SearchIDs(_ context.Context, q string) ([]string, github.RateLimit, error) {
	for prefix, err := range f.feedErr {
		if strings.HasPrefix(q, prefix+" ") {
			return nil, github.RateLimit{}, err
		}
	}
	for prefix, ids := range f.search {
		if strings.HasPrefix(q, prefix+" ") {
			return ids, f.searchRate[prefix], nil
		}
	}
	return nil, github.RateLimit{}, nil
}

func (f *fakeClient) Details(_ context.Context, ids []string) ([]github.Snapshot, github.RateLimit, error) {
	f.detailCalls = append(f.detailCalls, slices.Clone(ids))
	if f.detailErr != nil {
		return nil, github.RateLimit{}, f.detailErr
	}
	out := make([]github.Snapshot, len(ids))
	for i, id := range ids {
		out[i] = github.Snapshot{ID: id}
	}
	return out, f.detailRate, nil
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
	res := Fetch(context.Background(), c, Request{Feeds: []config.Feed{feedA, feedB}, DetailIDs: []string{"4"}, Now: now})
	if res.Err != nil {
		t.Fatalf("Err = %v", res.Err)
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
	if !IsRateLimited(res.Err) {
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
