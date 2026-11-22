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
	fresh := map[string]github.Fingerprint{}
	defer func() {
		// A NothingReachedError has set Unreachable already.
		if res.Unreachable == nil {
			res.Unreachable = sortedByID(unreachable)
		}
		res.Reached = reachedIDs(fresh, res.Snapshots)
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
	if err := res.searchFeeds(ctx, c, req, fresh); err != nil {
		res.Err = err
		return res
	}

	if lookup := unfound(req.Watch, fresh); len(lookup) > 0 {
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

	if ids := detailIDs(req, fresh); len(ids) > 0 {
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
		res.Unreachable = sortedByID(unreachable)
		res.Err = &NothingReachedError{FeedErrs: res.FeedErrs, Unreachable: res.Unreachable}
	}
	return res
}

// searchFeeds runs each feed's search, putting every PR it returns in fresh
// and the feed's name in r.Found. A failed search is added to r.FeedErrs and
// skipped. A rate limit stops the searches and is returned.
func (r *Result) searchFeeds(ctx context.Context, c github.Client, req Request, fresh map[string]github.Fingerprint) error {
	for _, f := range req.Feeds {
		fps, rl, err := c.Search(ctx, github.FeedQuery(f, req.Now))
		r.observe(rl)
		if err != nil {
			err = fmt.Errorf("search feed %q: %w", f.Name, err)
			if github.IsRateLimited(err) {
				return err
			}
			r.FeedErrs = append(r.FeedErrs, err)
			continue
		}
		for _, fp := range fps {
			fresh[fp.ID] = fp
			if !slices.Contains(r.Found[fp.ID], f.Name) {
				r.Found[fp.ID] = append(r.Found[fp.ID], f.Name)
			}
		}
	}
	return nil
}

// unfound lists the watched PRs no feed returned, sorted and once each.
func unfound(watch []string, fresh map[string]github.Fingerprint) []string {
	var ids []string
	for _, id := range watch {
		if _, ok := fresh[id]; !ok {
			ids = append(ids, id)
		}
	}
	return sortUnique(ids)
}

// detailIDs lists the PRs due a full fetch, sorted and once each: those in
// req.DetailIDs, and those in fresh that are new or have moved since
// req.Known.
func detailIDs(req Request, fresh map[string]github.Fingerprint) []string {
	ids := slices.Clone(req.DetailIDs)
	for id, fp := range fresh {
		if known, ok := req.Known[id]; !ok || !known.Same(fp) {
			ids = append(ids, id)
		}
	}
	return sortUnique(ids)
}

// reachedIDs lists the PRs a search, lookup or detail fetch returned, sorted
// and once each.
func reachedIDs(fresh map[string]github.Fingerprint, snaps []github.Snapshot) []string {
	ids := slices.Collect(maps.Keys(fresh))
	for _, s := range snaps {
		ids = append(ids, s.ID)
	}
	return sortUnique(ids)
}

// sortUnique sorts ids in place and drops repeats.
func sortUnique(ids []string) []string {
	slices.Sort(ids)
	return slices.Compact(ids)
}

// sortedByID returns the skipped PRs ordered by ID, or nil when there are
// none.
func sortedByID(skipped map[string]github.SkippedPR) []github.SkippedPR {
	var out []github.SkippedPR
	for _, id := range slices.Sorted(maps.Keys(skipped)) {
		out = append(out, skipped[id])
	}
	return out
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
