package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/cli/go-gh/v2/pkg/api"
)

const (
	searchPageSize   = 50
	searchMaxPages   = 2
	fingerprintBatch = 100 // the most IDs nodes(ids:) takes
	// A batch of mono PRs, each with ~50 required checks, takes 5–13s; 10
	// keeps a batch well inside GitHub's GraphQL timeout.
	detailBatch    = 10
	detailParallel = 3
)

type Client interface {
	Viewer(ctx context.Context) (string, error)
	// Search returns the fingerprints of up to 100 PRs, 2 pages of 50.
	Search(ctx context.Context, query string) ([]Fingerprint, RateLimit, error)
	// Fingerprints looks PRs up by node ID, 100 to a query. A PR that no
	// longer resolves is left out.
	Fingerprints(ctx context.Context, ids []string) ([]Fingerprint, RateLimit, error)
	// Details fetches full Snapshots in batches of aliases, a few batches at
	// a time, in the order of ids. A PR that no longer resolves is left out.
	Details(ctx context.Context, viewer string, ids []string) ([]Snapshot, RateLimit, error)
}

// Doer is the slice of go-gh's GraphQLClient the client uses.
type Doer interface {
	DoWithContext(ctx context.Context, query string, vars map[string]any, resp any) error
}

type client struct{ d Doer }

func NewClient(d Doer) Client { return &client{d: d} }

// NewGHClient builds a client that authenticates with the user's gh login.
func NewGHClient() (Client, error) {
	g, err := api.DefaultGraphQLClient()
	if err != nil {
		return nil, fmt.Errorf("github: gh auth: %w", err)
	}
	return NewClient(g), nil
}

// do runs a query and returns the top-level fields, the call's rate limit,
// and the GraphQL errors it let through. go-gh returns a GraphQL error
// whenever the response lists errors, even when it also carries data. If
// skippable accepts the path of every error, do keeps the data and returns
// those errors instead; the caller must skip what each one names, since the
// error may sit deep inside data that isn't null.
func (c *client) do(ctx context.Context, query string, vars map[string]any, skippable func(path []any) bool) (map[string]json.RawMessage, []api.GraphQLErrorItem, RateLimit, error) {
	var raw map[string]json.RawMessage
	var skipped []api.GraphQLErrorItem
	if err := c.d.DoWithContext(ctx, query, vars, &raw); err != nil {
		var ok bool
		if skipped, ok = skippableErrors(err, skippable); raw == nil || !ok {
			return nil, nil, RateLimit{}, err
		}
	}
	rl, err := splitRateLimit(raw)
	if err != nil {
		return nil, nil, RateLimit{}, err
	}
	delete(raw, "rateLimit")
	return raw, skipped, rl, nil
}

// skippableErrors returns the items of err if it is a GraphQL error whose
// every item has a path skippable accepts. A rate limit is never skippable.
func skippableErrors(err error, skippable func(path []any) bool) ([]api.GraphQLErrorItem, bool) {
	var gqlErr *api.GraphQLError
	if skippable == nil || !errors.As(err, &gqlErr) || len(gqlErr.Errors) == 0 {
		return nil, false
	}
	for _, e := range gqlErr.Errors {
		if e.Type == "RATE_LIMITED" || len(e.Path) == 0 || !skippable(e.Path) {
			return nil, false
		}
	}
	return gqlErr.Errors, true
}

// nodeIndex is the index i an error path ["nodes", i, …] names in a
// nodes(ids:) query, if it is one of n.
func nodeIndex(path []any, n int) (int, bool) {
	if len(path) < 2 || path[0] != "nodes" {
		return 0, false
	}
	// encoding/json decodes a number in an []any as a float64.
	f, ok := path[1].(float64)
	if !ok || f != float64(int(f)) {
		return 0, false
	}
	i := int(f)
	return i, i >= 0 && i < n
}

// add folds another call's rate limit into the total: the lowest remaining
// with its reset, and the summed cost. Calls run concurrently, so "latest"
// has no meaning. An empty RateLimit, from a failed call, is ignored.
func (r *RateLimit) add(next RateLimit) {
	if next == (RateLimit{}) {
		return
	}
	cost := r.Cost + next.Cost
	if *r == (RateLimit{}) || next.Remaining < r.Remaining {
		r.Remaining, r.ResetAt = next.Remaining, next.ResetAt
	}
	r.Cost = cost
}

func (c *client) Viewer(ctx context.Context) (string, error) {
	raw, _, _, err := c.do(ctx, viewerQuery, nil, nil)
	if err != nil {
		return "", err
	}
	var v loginNode
	if err := json.Unmarshal(raw["viewer"], &v); err != nil {
		return "", err
	}
	return v.Login, nil
}

func (c *client) Search(ctx context.Context, query string) ([]Fingerprint, RateLimit, error) {
	var fps []Fingerprint
	var total RateLimit
	var after any
	for range searchMaxPages {
		raw, _, rl, err := c.do(ctx, searchQuery, map[string]any{"q": query, "after": after}, nil)
		if err != nil {
			return nil, total, err
		}
		total.add(rl)
		var s struct {
			PageInfo pageInfo           `json:"pageInfo"`
			Nodes    []*fingerprintNode `json:"nodes"`
		}
		if err := json.Unmarshal(raw["search"], &s); err != nil {
			return nil, total, err
		}
		for _, n := range s.Nodes {
			if f, ok := n.fingerprint(); ok {
				fps = append(fps, f)
			}
		}
		if !s.PageInfo.HasNextPage {
			break
		}
		after = s.PageInfo.EndCursor
	}
	return fps, total, nil
}

func (c *client) Fingerprints(ctx context.Context, ids []string) ([]Fingerprint, RateLimit, error) {
	var fps []Fingerprint
	var total RateLimit
	for start := 0; start < len(ids); start += fingerprintBatch {
		batch := ids[start:min(start+fingerprintBatch, len(ids))]
		// A node that no longer resolves is null, with an error at its
		// index. An error deeper in a node skips it too.
		inNodes := func(path []any) bool {
			_, ok := nodeIndex(path, len(batch))
			return ok
		}
		raw, errs, rl, err := c.do(ctx, fingerprintsQuery, map[string]any{"ids": batch}, inNodes)
		if err != nil {
			return nil, total, err
		}
		total.add(rl)
		skip := map[int]bool{}
		for _, e := range errs {
			i, _ := nodeIndex(e.Path, len(batch))
			skip[i] = true
		}
		var nodes []*fingerprintNode
		if err := json.Unmarshal(raw["nodes"], &nodes); err != nil {
			return nil, total, err
		}
		for i, n := range nodes {
			if skip[i] {
				continue
			}
			if f, ok := n.fingerprint(); ok {
				fps = append(fps, f)
			}
		}
	}
	return fps, total, nil
}

func (c *client) Details(ctx context.Context, viewer string, ids []string) ([]Snapshot, RateLimit, error) {
	var batches [][]string
	for start := 0; start < len(ids); start += detailBatch {
		batches = append(batches, ids[start:min(start+detailBatch, len(ids))])
	}
	type result struct {
		snaps []Snapshot
		rl    RateLimit
	}
	results := make([]result, len(batches))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		sem      = make(chan struct{}, detailParallel)
		errOnce  sync.Once
		firstErr error
	)
	for i, batch := range batches {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return // another batch failed
			}
			snaps, rl, err := c.fetchBatch(ctx, viewer, batch)
			results[i] = result{snaps, rl}
			if err != nil {
				// The first error is the cause; the rest are its cancellation.
				errOnce.Do(func() { firstErr = err })
				cancel()
			}
		})
	}
	wg.Wait()

	var snaps []Snapshot
	var total RateLimit
	for _, r := range results {
		snaps = append(snaps, r.snaps...)
		total.add(r.rl)
	}
	if firstErr != nil {
		return nil, total, firstErr
	}
	return snaps, total, nil
}

// fetchBatch fetches one batch of aliases, then any further pages of each
// PR's check contexts.
func (c *client) fetchBatch(ctx context.Context, viewer string, batch []string) ([]Snapshot, RateLimit, error) {
	var snaps []Snapshot
	var total RateLimit
	vars := make(map[string]any, len(batch)+1)
	vars["login"] = viewer
	aliases := make(map[string]bool, len(batch))
	for i, id := range batch {
		vars[fmt.Sprintf("id%d", i)] = id
		aliases[fmt.Sprintf("p%d", i)] = true
	}
	// A PR that was deleted, or that the viewer lost access to, comes back
	// null with an error at its alias. An error deeper in a PR leaves a null
	// that would parse as missing data, such as a dropped check, so any
	// error under an alias skips that PR. Skipping keeps the rest of the
	// batch; the tracker keeps the skipped PR's last snapshot.
	atAlias := func(path []any) bool {
		s, ok := path[0].(string)
		return ok && aliases[s]
	}
	raw, errs, rl, err := c.do(ctx, DetailQuery(len(batch)), vars, atAlias)
	if err != nil {
		return nil, total, err
	}
	total.add(rl)
	skip := map[string]bool{}
	for _, e := range errs {
		skip[e.Path[0].(string)] = true
	}
	for i := range batch {
		alias := fmt.Sprintf("p%d", i)
		node, ok := raw[alias]
		if !ok || string(node) == "null" || skip[alias] {
			continue
		}
		snap, next, err := parsePRPage(node)
		if err != nil {
			return nil, total, err
		}
		if snap.ID == "" {
			continue // the ID is not a pull request
		}
		for next.HasNextPage {
			more, nextPage, rl, err := c.checksPage(ctx, snap.ID, next.EndCursor)
			if err != nil {
				return nil, total, err
			}
			total.add(rl)
			snap.Checks = append(snap.Checks, more...)
			next = nextPage
		}
		snaps = append(snaps, snap)
	}
	return snaps, total, nil
}

func (c *client) checksPage(ctx context.Context, id, after string) ([]Check, pageInfo, RateLimit, error) {
	raw, _, rl, err := c.do(ctx, checksPageQuery, map[string]any{"id": id, "after": after}, nil)
	if err != nil {
		return nil, pageInfo{}, rl, err
	}
	checks, next, err := parseChecksPage(raw["node"])
	return checks, next, rl, err
}
