// Package schedule decides whether auto mode should start a Claude review of
// a PR right now, and says why not when it shouldn't. It does no I/O.
package schedule

import (
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/status"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
)

// Candidate is a tracked PR plus what the caller knows about its reviews.
type Candidate struct {
	T tracker.Tracked
	// Latest is the newest review of any outcome, nil if none.
	Latest *store.Review
	// AutoToday counts today's auto reviews of this PR, failed ones included.
	AutoToday int
	// Failures and LastFailure describe the run of failed reviews at the head.
	Failures    int
	LastFailure time.Time
	Running     bool
}

// Decision is the verdict. Reason is one of "running", "status", "quiet",
// "reviewed", "cap", "budget", "backoff", "stopped", or "" when
// Review is true.
type Decision struct {
	Review bool
	Reason string
}

// Decide applies the spec's rules in order and returns the first that blocks.
func Decide(c Candidate, cfg config.Config, spentToday float64, viewer string, now time.Time) Decision {
	no := func(reason string) Decision { return Decision{Reason: reason} }
	pr := c.T.PR

	if c.Running {
		return no("running")
	}
	st := c.T.Status
	if st.Status != status.ReadyForReview && !(st.Status == status.NewChanges && st.CI == status.CIGreen) {
		return no("status")
	}
	// A head with no known arrival time might have just been pushed.
	if c.T.HeadSeenAt.IsZero() || now.Sub(c.T.HeadSeenAt) < cfg.Review.QuietPeriod.Duration {
		return no("quiet")
	}
	atHead := c.Latest != nil && c.Latest.HeadOID == pr.HeadOID
	if atHead && c.Latest.Outcome != "failed" {
		return no("reviewed")
	}
	if c.AutoToday >= cfg.Review.MaxReviewsPerPRPerDay {
		return no("cap")
	}
	if spentToday >= cfg.Review.Budget.DailyUSD {
		return no("budget")
	}
	if atHead && c.Latest.Outcome == "failed" {
		wait, stop := Backoff(c.Failures)
		if stop {
			return no("stopped")
		}
		if now.Sub(c.LastFailure) < wait {
			return no("backoff")
		}
	}
	return Decision{Review: true}
}

// Backoff is how long to wait after the given number of consecutive failures
// before trying again; from the fourth failure on it stops until the user
// forces a review.
func Backoff(failures int) (wait time.Duration, stop bool) {
	switch failures {
	case 1:
		return time.Minute, false
	case 2:
		return 5 * time.Minute, false
	case 3:
		return 30 * time.Minute, false
	}
	if failures < 1 {
		return 0, false
	}
	return 0, true
}

// DayStart is the local midnight that began now's day, when the daily budget
// resets. It uses the local zone whatever now's own location is.
func DayStart(now time.Time) time.Time {
	local := now.Local()
	y, m, d := local.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}
