package poll

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/github"
)

// Request is one poll: the feeds to search and the tracked PRs whose detail
// is due whether or not a feed returns them.
type Request struct {
	Feeds     []config.Feed
	DetailIDs []string
	Now       time.Time
}

// Result is what a poll fetched. Found maps a PR node ID to the names of the
// feeds that returned it. Rate has the lowest Remaining seen during the poll
// and the summed cost. FeedErrs are the searches that failed; the poll went
// on without those feeds. When Err is set the rest is partial.
type Result struct {
	Found     map[string][]string
	Snapshots []github.Snapshot
	Rate      github.RateLimit
	FeedErrs  []error
	Err       error
}

// Fetch searches every feed, then fetches detail for the union of the found
// IDs and req.DetailIDs. A feed whose search fails is skipped and reported in
// FeedErrs, so one bad query doesn't stop the others. A rate limit, or a
// failed detail fetch, ends the poll.
func Fetch(ctx context.Context, c github.Client, req Request) Result {
	res := Result{Found: map[string][]string{}}
	for _, f := range req.Feeds {
		ids, rl, err := c.SearchIDs(ctx, github.FeedQuery(f, req.Now))
		res.observe(rl)
		if err != nil {
			err = fmt.Errorf("search feed %q: %w", f.Name, err)
			if IsRateLimited(err) {
				res.Err = err
				return res
			}
			res.FeedErrs = append(res.FeedErrs, err)
			continue
		}
		for _, id := range ids {
			if !slices.Contains(res.Found[id], f.Name) {
				res.Found[id] = append(res.Found[id], f.Name)
			}
		}
	}

	ids := slices.Clone(req.DetailIDs)
	for id := range res.Found {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) == 0 {
		return res
	}
	snaps, rl, err := c.Details(ctx, ids)
	res.observe(rl)
	if err != nil {
		res.Err = fmt.Errorf("fetch PR detail: %w", err)
		return res
	}
	res.Snapshots = snaps
	return res
}

func (r *Result) observe(rl github.RateLimit) {
	if rl == (github.RateLimit{}) {
		return
	}
	cost := r.Rate.Cost + rl.Cost
	if r.Rate == (github.RateLimit{}) || rl.Remaining < r.Rate.Remaining {
		r.Rate = rl
	}
	r.Rate.Cost = cost
}
