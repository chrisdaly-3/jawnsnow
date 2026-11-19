package demo

import (
	"context"
	"slices"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
)

// Client is a github.Client over the demo's canned PRs. Every search
// returns every open PR, and every call reports a healthy rate limit.
type Client struct {
	now   time.Time
	snaps []github.Snapshot
}

// NewClient returns a Client whose PRs are dated relative to now.
func NewClient(now time.Time) *Client { return &Client{now: now, snaps: snapshots(now)} }

func (c *Client) snapshots() []github.Snapshot { return slices.Clone(c.snaps) }

func (c *Client) rateLimit() github.RateLimit {
	return github.RateLimit{Remaining: 4870, Cost: 1, ResetAt: c.now.Add(time.Hour)}
}

func (c *Client) find(id string) (github.Snapshot, bool) {
	i := slices.IndexFunc(c.snaps, func(s github.Snapshot) bool { return s.ID == id })
	if i < 0 {
		return github.Snapshot{}, false
	}
	return c.snaps[i], true
}

func (c *Client) Viewer(context.Context) (string, error) { return Viewer, nil }

func (c *Client) Search(context.Context, string) ([]github.Fingerprint, github.RateLimit, error) {
	var out []github.Fingerprint
	for _, s := range c.snaps {
		if s.State == github.PROpen {
			out = append(out, s.Fingerprint())
		}
	}
	return out, c.rateLimit(), nil
}

func (c *Client) Fingerprints(_ context.Context, ids []string) ([]github.Fingerprint, []github.SkippedPR, github.RateLimit, error) {
	var out []github.Fingerprint
	var skipped []github.SkippedPR
	for _, id := range ids {
		if s, ok := c.find(id); ok {
			out = append(out, s.Fingerprint())
		} else {
			skipped = append(skipped, github.SkippedPR{ID: id, Type: "NOT_FOUND", Message: "not a demo PR"})
		}
	}
	return out, skipped, c.rateLimit(), nil
}

func (c *Client) Details(_ context.Context, _ string, ids []string) ([]github.Snapshot, []github.SkippedPR, github.RateLimit, error) {
	var out []github.Snapshot
	var skipped []github.SkippedPR
	for _, id := range ids {
		if s, ok := c.find(id); ok {
			out = append(out, s)
		} else {
			skipped = append(skipped, github.SkippedPR{ID: id, Type: "NOT_FOUND", Message: "not a demo PR"})
		}
	}
	return out, skipped, c.rateLimit(), nil
}

func (c *Client) ReviewContext(_ context.Context, id string) (github.PRContext, github.RateLimit, error) {
	return contextFor(id), c.rateLimit(), nil
}
