// Package poll fetches one round of feed searches and PR detail from GitHub,
// and paces polling against GitHub's rate limit.
package poll

import (
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
)

const (
	lowRemaining     = 200
	slowRemaining    = 1000
	resetGrace       = 5 * time.Second
	slowPollMultiple = 2
)

// Quota remembers the most recent rate limit GitHub reported.
type Quota struct {
	last github.RateLimit
	seen bool
}

// Observe records rl. An empty RateLimit, from a call that failed before
// GitHub answered, carries no information and is ignored.
func (q *Quota) Observe(rl github.RateLimit) {
	if rl == (github.RateLimit{}) {
		return
	}
	q.last, q.seen = rl, true
}

// Remaining is the last reported remaining budget, and false before any.
func (q Quota) Remaining() (int, bool) { return q.last.Remaining, q.seen }

// Next returns how long to wait before the next poll, and a message for the
// status bar when the wait is longer than base. It never waits less than base.
func (q Quota) Next(base time.Duration, now time.Time) (wait time.Duration, reason string) {
	switch {
	case !q.seen:
		return base, ""
	case q.last.Remaining < lowRemaining:
		wait = max(q.last.ResetAt.Add(resetGrace).Sub(now), base)
		return wait, "GitHub rate limit low — resuming " + now.Add(wait).Format("15:04")
	case q.last.Remaining < slowRemaining:
		return slowPollMultiple * base, "GitHub rate limit getting low — polling slower"
	}
	return base, ""
}
