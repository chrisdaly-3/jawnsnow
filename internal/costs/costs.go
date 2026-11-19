// Package costs buckets, groups and summarises cost rows for the Cost view.
// It is pure: it reads no clock and touches no store.
package costs

import (
	"sort"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/store"
)

type Bucket string

const (
	Hour Bucket = "hour"
	Day  Bucket = "day"
	Week Bucket = "week"
)

type GroupBy string

const (
	ByNone   GroupBy = ""
	ByModel  GroupBy = "model"
	ByAuthor GroupBy = "author"
	ByRepo   GroupBy = "repo"
	ByTier   GroupBy = "tier"
)

// maxSeries matches the 6-color series palette. With more groups than that,
// the maxSeries-1 biggest spenders stay and the rest fold into "other".
const maxSeries = 6

const otherName = "other"

type Query struct {
	Filter     store.CostFilter
	Bucket     Bucket
	GroupBy    GroupBy
	Cumulative bool
}

type Point struct {
	Start time.Time
	USD   float64
}

// Series holds the same bucket starts as every other series, zeros filled in.
type Series struct {
	Name   string
	Points []Point
}

// TopPR is one of the most expensive PRs. CostRow carries only the PR's
// GraphQL node ID, so Number is never set here; the Cost view resolves it.
type TopPR struct {
	PRID, Repo string
	Number     int
	USD        float64
}

// Summary totals only rows with a known cost. Unknown counts the rows whose
// cost is unknown; they add nothing to the USD fields but still count as
// Reviews. AvgPerReview is over the reviews with at least one known cost.
type Summary struct {
	Total, AvgPerReview, FirstPass, FollowUp float64
	Reviews, Unknown                         int
	Top                                      []TopPR
}

// BucketStart returns the start of t's bucket in time.Local, so a stored UTC
// time lands on the user's local day. A week starts Monday 00:00.
func BucketStart(t time.Time, b Bucket) time.Time {
	l := t.In(time.Local)
	y, m, d := l.Date()
	switch b {
	case Day:
		return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	case Week:
		return time.Date(y, m, d-(int(l.Weekday())+6)%7, 0, 0, 0, 0, time.Local)
	default:
		// Instant arithmetic keeps the repeated hour of a fall-back distinct.
		return l.Add(-(time.Duration(l.Minute())*time.Minute +
			time.Duration(l.Second())*time.Second + time.Duration(l.Nanosecond())))
	}
}

// nextBucket uses calendar arithmetic so a DST day is 23 or 25 hours.
func nextBucket(start time.Time, b Bucket) time.Time {
	y, m, d := start.Date()
	switch b {
	case Day:
		return time.Date(y, m, d+1, 0, 0, 0, 0, time.Local)
	case Week:
		return time.Date(y, m, d+7, 0, 0, 0, 0, time.Local)
	default:
		return start.Add(time.Hour)
	}
}

func contains(set []string, v string) bool {
	if len(set) == 0 {
		return true
	}
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

func keep(r store.CostRow, f store.CostFilter) bool {
	if !f.From.IsZero() && r.At.Before(f.From) {
		return false
	}
	if !f.To.IsZero() && !r.At.Before(f.To) {
		return false
	}
	return contains(f.Authors, r.Author) && contains(f.Repos, r.Repo) &&
		contains(f.Models, r.Model) && contains(f.Efforts, r.Effort) &&
		contains(f.Tiers, r.Tier) && contains(f.Modes, r.Mode) &&
		contains(f.Outcomes, r.Outcome)
}

func groupOf(r store.CostRow, g GroupBy) string {
	var v string
	switch g {
	case ByModel:
		v = r.Model
	case ByAuthor:
		v = r.Author
	case ByRepo:
		v = r.Repo
	case ByTier:
		v = r.Tier
	default:
		return "total"
	}
	if v == "" {
		return "(none)"
	}
	return v
}

// usd is the row's real cost; an unknown cost is not a cost.
func usd(r store.CostRow) float64 {
	if r.CostUnknown {
		return 0
	}
	return r.CostUSD
}

func Aggregate(rows []store.CostRow, q Query) ([]Series, Summary) {
	var kept []store.CostRow
	for _, r := range rows {
		if keep(r, q.Filter) {
			kept = append(kept, r)
		}
	}
	sum := summarise(kept)

	// Bucket range: the filter's, else the rows' own extent.
	from, to := q.Filter.From, q.Filter.To
	if len(kept) > 0 {
		lo, hi := kept[0].At, kept[0].At
		for _, r := range kept {
			if r.At.Before(lo) {
				lo = r.At
			}
			if r.At.After(hi) {
				hi = r.At
			}
		}
		if from.IsZero() {
			from = lo
		}
		if to.IsZero() {
			to = hi.Add(time.Nanosecond)
		}
	}
	var starts []time.Time
	if !from.IsZero() && !to.IsZero() {
		for s := BucketStart(from, q.Bucket); s.Before(to); s = nextBucket(s, q.Bucket) {
			starts = append(starts, s)
		}
	}
	index := make(map[int64]int, len(starts))
	for i, s := range starts {
		index[s.UnixNano()] = i
	}

	// Totals per group decide which groups stay and which fold into other.
	spend := map[string]float64{}
	for _, r := range kept {
		spend[groupOf(r, q.GroupBy)] += usd(r)
	}
	names := make([]string, 0, len(spend))
	for n := range spend {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if spend[names[i]] != spend[names[j]] {
			return spend[names[i]] > spend[names[j]]
		}
		return names[i] < names[j]
	})
	folded := map[string]bool{}
	if len(names) > maxSeries {
		for _, n := range names[maxSeries-1:] {
			folded[n] = true
		}
		names = names[:maxSeries-1]
	}
	sort.Strings(names)
	if len(folded) > 0 {
		names = append(names, otherName)
	}
	if len(names) == 0 {
		names = []string{"total"}
	}

	byName := make(map[string]*Series, len(names))
	series := make([]Series, len(names))
	for i, n := range names {
		series[i] = Series{Name: n, Points: make([]Point, len(starts))}
		for j, s := range starts {
			series[i].Points[j].Start = s
		}
		byName[n] = &series[i]
	}
	for _, r := range kept {
		n := groupOf(r, q.GroupBy)
		if folded[n] {
			n = otherName
		}
		if i, ok := index[BucketStart(r.At, q.Bucket).UnixNano()]; ok {
			byName[n].Points[i].USD += usd(r)
		}
	}
	if q.Cumulative {
		for i := range series {
			var run float64
			for j := range series[i].Points {
				run += series[i].Points[j].USD
				series[i].Points[j].USD = run
			}
		}
	}
	return series, sum
}

func summarise(rows []store.CostRow) Summary {
	var s Summary
	reviews, priced := map[string]bool{}, map[string]bool{}
	perPR := map[string]*TopPR{}
	for _, r := range rows {
		reviews[r.ReviewID] = true
		if r.CostUnknown {
			s.Unknown++
		} else {
			priced[r.ReviewID] = true
		}
		c := usd(r)
		s.Total += c
		switch r.Tier {
		case "first_pass":
			s.FirstPass += c
		case "follow_up":
			s.FollowUp += c
		}
		p := perPR[r.PRID]
		if p == nil {
			p = &TopPR{PRID: r.PRID, Repo: r.Repo}
			perPR[r.PRID] = p
		}
		p.USD += c
	}
	s.Reviews = len(reviews)
	if len(priced) > 0 {
		s.AvgPerReview = s.Total / float64(len(priced))
	}
	top := make([]TopPR, 0, len(perPR))
	for _, p := range perPR {
		if p.USD > 0 {
			top = append(top, *p)
		}
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].USD != top[j].USD {
			return top[i].USD > top[j].USD
		}
		return top[i].PRID < top[j].PRID
	})
	if len(top) > 5 {
		top = top[:5]
	}
	s.Top = top
	return s
}
