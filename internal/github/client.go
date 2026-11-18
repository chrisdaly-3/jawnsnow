package github

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cli/go-gh/v2/pkg/api"
)

const (
	searchPageSize = 50
	searchMaxPages = 2
	detailBatch    = 20
)

type Client interface {
	Viewer(ctx context.Context) (string, error)
	SearchIDs(ctx context.Context, query string) ([]string, RateLimit, error) // up to 100 IDs, 2 pages of 50
	Details(ctx context.Context, ids []string) ([]Snapshot, RateLimit, error) // batches of 20 aliases
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

// do runs a query and returns the top-level fields plus the call's rate limit.
func (c *client) do(ctx context.Context, query string, vars map[string]any) (map[string]json.RawMessage, RateLimit, error) {
	var raw map[string]json.RawMessage
	if err := c.d.DoWithContext(ctx, query, vars, &raw); err != nil {
		return nil, RateLimit{}, err
	}
	rl, err := splitRateLimit(raw)
	if err != nil {
		return nil, RateLimit{}, err
	}
	delete(raw, "rateLimit")
	return raw, rl, nil
}

// add folds a later call's rate limit into the total: the latest remaining
// and reset, and the summed cost.
func (r *RateLimit) add(next RateLimit) {
	r.Remaining, r.ResetAt = next.Remaining, next.ResetAt
	r.Cost += next.Cost
}

func (c *client) Viewer(ctx context.Context) (string, error) {
	raw, _, err := c.do(ctx, viewerQuery, nil)
	if err != nil {
		return "", err
	}
	var v loginNode
	if err := json.Unmarshal(raw["viewer"], &v); err != nil {
		return "", err
	}
	return v.Login, nil
}

func (c *client) SearchIDs(ctx context.Context, query string) ([]string, RateLimit, error) {
	var ids []string
	var total RateLimit
	var after any
	for range searchMaxPages {
		raw, rl, err := c.do(ctx, searchQuery, map[string]any{"q": query, "after": after})
		if err != nil {
			return nil, total, err
		}
		total.add(rl)
		var s struct {
			PageInfo pageInfo `json:"pageInfo"`
			Nodes    []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		}
		if err := json.Unmarshal(raw["search"], &s); err != nil {
			return nil, total, err
		}
		for _, n := range s.Nodes {
			if n.ID != "" { // a non-PR search hit has no id
				ids = append(ids, n.ID)
			}
		}
		if !s.PageInfo.HasNextPage {
			break
		}
		after = s.PageInfo.EndCursor
	}
	return ids, total, nil
}

func (c *client) Details(ctx context.Context, ids []string) ([]Snapshot, RateLimit, error) {
	var snaps []Snapshot
	var total RateLimit
	for start := 0; start < len(ids); start += detailBatch {
		batch := ids[start:min(start+detailBatch, len(ids))]
		vars := make(map[string]any, len(batch))
		for i, id := range batch {
			vars[fmt.Sprintf("id%d", i)] = id
		}
		raw, rl, err := c.do(ctx, DetailQuery(len(batch)), vars)
		if err != nil {
			return nil, total, err
		}
		total.add(rl)
		for i := range batch {
			node, ok := raw[fmt.Sprintf("p%d", i)]
			if !ok || string(node) == "null" {
				continue // deleted or inaccessible
			}
			snap, next, err := parsePRPage(node)
			if err != nil {
				return nil, total, err
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
	}
	return snaps, total, nil
}

func (c *client) checksPage(ctx context.Context, id, after string) ([]Check, pageInfo, RateLimit, error) {
	raw, rl, err := c.do(ctx, checksPageQuery, map[string]any{"id": id, "after": after})
	if err != nil {
		return nil, pageInfo{}, rl, err
	}
	checks, next, err := parseChecksPage(raw["node"])
	return checks, next, rl, err
}
