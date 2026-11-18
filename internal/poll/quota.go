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
	maxBackoff       = 30 * time.Minute
)

// Quota remembers the most recent rate limit GitHub reported, and any run of
// polls GitHub refused for rate limiting.
type Quota struct {
	last github.RateLimit
	seen bool

	limited      int       // consecutive rate-limited polls
	limitedUntil time.Time // when GitHub said to retry; zero if it didn't
}

// Observe records rl. An empty RateLimit, from a call that failed before
// GitHub answered, carries no information and is ignored.
func (q *Quota) Observe(rl github.RateLimit) {
	if rl == (github.RateLimit{}) {
		return
	}
	q.last, q.seen = rl, true
}

// ObserveRateLimited records a poll GitHub refused for rate limiting.
// resetAt is when GitHub said to retry, or zero when it didn't say.
func (q *Quota) ObserveRateLimited(resetAt time.Time) {
	q.limited++
	q.limitedUntil = resetAt
}

// ObserveSuccess ends a run of rate-limited polls.
func (q *Quota) ObserveSuccess() {
	q.limited, q.limitedUntil = 0, time.Time{}
}

// Limited reports whether the last poll was refused for rate limiting.
func (q Quota) Limited() bool { return q.limited > 0 }

// Remaining is the last reported remaining budget, and false before any.
func (q Quota) Remaining() (int, bool) { return q.last.Remaining, q.seen }

// Next returns how long to wait before the next poll, and a message for the
// status bar when the wait is longer than base. It never waits less than base.
// After a refused poll it waits until GitHub's reset time, or without one
// backs off 2×, 4×, 8× base and so on, up to 30m.
func (q Quota) Next(base time.Duration, now time.Time) (wait time.Duration, reason string) {
	switch {
	case q.limited > 0:
		if untilReset := q.limitedUntil.Add(resetGrace).Sub(now); !q.limitedUntil.IsZero() && untilReset > 0 {
			wait = max(untilReset, base)
		} else {
			wait = max(base, min(base<<min(q.limited, 16), maxBackoff))
		}
		return wait, "GitHub rate limit low — resuming " + now.Add(wait).Format("15:04")
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
