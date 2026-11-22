package poll

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/github"
)

// Request is one poll. Known is the fingerprint of every tracked PR's last
// snapshot. Watch are the tracked PRs due a check whether or not a feed
// returns them. DetailIDs get full detail whatever their fingerprint says.
// Viewer is the gh user, whose own reviews detail always includes.
type Request struct {
	Viewer    string
	Feeds     []config.Feed
	Known     map[string]github.Fingerprint
	Watch     []string
	DetailIDs []string
	Now       time.Time
}

// Result is what a poll fetched. Found maps a PR node ID to the names of the
// feeds that returned it. Snapshots are only for the PRs that changed. Rate
// has the lowest Remaining seen during the poll and the summed cost.
// FeedErrs are the searches that failed; the poll went on without those
// feeds. Unreachable are the PRs GitHub answered with an error, sorted by
// ID; they keep their last snapshot. Reached are the sorted IDs of the PRs
// a search, lookup or detail fetch returned. When Err is set the rest is
// partial.
type Result struct {
	Found       map[string][]string
	Snapshots   []github.Snapshot
	Rate        github.RateLimit
	FeedErrs    []error
	Unreachable []github.SkippedPR
	Reached     []string
	Err         error
}

// Fetch searches every feed and looks up the fingerprints of the watched PRs
// no feed returned. It fetches full detail only for PRs that are new, whose
// fingerprint moved since Known, or that are in DetailIDs: a full fetch
// costs far more than the fingerprint. A feed whose search fails is skipped
// and reported in FeedErrs, so one bad query doesn't stop the others. A rate
// limit, or a failed lookup or detail fetch, ends the poll. A PR GitHub
// answers with an error is left out and listed in Unreachable. The poll
// still succeeds unless it reached nothing: see NothingReachedError.
func Fetch(ctx context.Context, c github.Client, req Request) (res Result) {
	res.Found = map[string][]string{}
	unreachable := map[string]github.SkippedPR{}
	sortedUnreachable := func() []github.SkippedPR {
		var out []github.SkippedPR
		for _, id := range slices.Sorted(maps.Keys(unreachable)) {
			out = append(out, unreachable[id])
		}
		return out
	}
	fresh := map[string]github.Fingerprint{}
	defer func() {
		res.Unreachable = sortedUnreachable()
		reached := slices.Collect(maps.Keys(fresh))
		for _, s := range res.Snapshots {
			reached = append(reached, s.ID)
		}
		slices.Sort(reached)
		res.Reached = slices.Compact(reached)
	}()
	// asked counts the PRs the lookup and detail calls asked for, and
	// missed the ones they skipped.
	var asked, missed int
	note := func(requested int, skipped []github.SkippedPR) {
		asked += requested
		missed += len(skipped)
		for _, s := range skipped {
			if _, ok := unreachable[s.ID]; !ok {
				unreachable[s.ID] = s
			}
		}
	}
	for _, f := range req.Feeds {
		fps, rl, err := c.Search(ctx, github.FeedQuery(f, req.Now))
		res.observe(rl)
		if err != nil {
			err = fmt.Errorf("search feed %q: %w", f.Name, err)
			if github.IsRateLimited(err) {
				res.Err = err
				return res
			}
			res.FeedErrs = append(res.FeedErrs, err)
			continue
		}
		for _, fp := range fps {
			fresh[fp.ID] = fp
			if !slices.Contains(res.Found[fp.ID], f.Name) {
				res.Found[fp.ID] = append(res.Found[fp.ID], f.Name)
			}
		}
	}

	var lookup []string
	for _, id := range req.Watch {
		if _, ok := fresh[id]; !ok {
			lookup = append(lookup, id)
		}
	}
	slices.Sort(lookup)
	lookup = slices.Compact(lookup)
	if len(lookup) > 0 {
		fps, skipped, rl, err := c.Fingerprints(ctx, lookup)
		res.observe(rl)
		if err != nil {
			res.Err = fmt.Errorf("check tracked PRs: %w", err)
			return res
		}
		note(len(lookup), skipped)
		for _, fp := range fps {
			fresh[fp.ID] = fp
		}
	}

	ids := slices.Clone(req.DetailIDs)
	for id, fp := range fresh {
		if known, ok := req.Known[id]; !ok || !known.Same(fp) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) > 0 {
		snaps, skipped, rl, err := c.Details(ctx, req.Viewer, ids)
		res.observe(rl)
		if err != nil {
			res.Err = fmt.Errorf("fetch PR detail: %w", err)
			return res
		}
		note(len(ids), skipped)
		res.Snapshots = snaps
	}
	// A search that succeeds reached GitHub, even an empty one.
	if len(req.Feeds) > 0 && len(res.FeedErrs) == len(req.Feeds) && (asked == 0 || missed == asked) {
		res.Err = &NothingReachedError{FeedErrs: res.FeedErrs, Unreachable: sortedUnreachable()}
	}
	return res
}

// NothingReachedError fails a poll that reached nothing: every feed search
// failed, and the lookups had nothing to check or GitHub refused every PR
// they asked for, as when an SSO session expires.
type NothingReachedError struct {
	FeedErrs    []error
	Unreachable []github.SkippedPR
}

func (e *NothingReachedError) Error() string {
	msgs := make([]string, len(e.FeedErrs))
	for i, err := range e.FeedErrs {
		msgs[i] = err.Error()
	}
	s := "all feeds failed (" + strings.Join(msgs, "; ") + ")"
	switch n := len(e.Unreachable); n {
	case 0:
		return s
	case 1:
		return s + "; 1 PR unreachable: " + github.SkippedTypes(e.Unreachable)
	default:
		return fmt.Sprintf("%s; %d PRs unreachable: %s", s, n, github.SkippedTypes(e.Unreachable))
	}
}

func (e *NothingReachedError) Unwrap() []error { return e.FeedErrs }

func (r *Result) observe(rl github.RateLimit) { r.Rate.Add(rl) }
