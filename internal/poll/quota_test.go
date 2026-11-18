package poll

import (
	"strings"
	"testing"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
)

func TestQuotaNext(t *testing.T) {
	now := time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)
	base := 3 * time.Minute
	reset := now.Add(20 * time.Minute)

	tests := []struct {
		name       string
		remaining  int
		wantWait   time.Duration
		wantReason string
	}{
		{"plenty", 5000, base, ""},
		{"getting low", 800, 2 * base, "GitHub rate limit getting low — polling slower"},
		{"low", 150, 20*time.Minute + 5*time.Second, "GitHub rate limit low — resuming 15:20"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var q Quota
			q.Observe(github.RateLimit{Remaining: tc.remaining, ResetAt: reset})
			wait, reason := q.Next(base, now)
			if wait != tc.wantWait {
				t.Errorf("wait = %v, want %v", wait, tc.wantWait)
			}
			if reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

func TestQuotaNextBeforeAnyObservation(t *testing.T) {
	var q Quota
	wait, reason := q.Next(time.Minute, time.Now())
	if wait != time.Minute || reason != "" {
		t.Errorf("Next = %v, %q; want 1m0s, \"\"", wait, reason)
	}
}

func TestQuotaIgnoresEmptyRateLimit(t *testing.T) {
	var q Quota
	q.Observe(github.RateLimit{Remaining: 150, ResetAt: time.Now().Add(time.Hour)})
	q.Observe(github.RateLimit{})
	if _, reason := q.Next(time.Minute, time.Now()); !strings.Contains(reason, "resuming") {
		t.Errorf("an empty RateLimit replaced the last real one; reason = %q", reason)
	}
}

func TestQuotaLowNeverPollsFasterThanBase(t *testing.T) {
	now := time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)
	var q Quota
	q.Observe(github.RateLimit{Remaining: 10, ResetAt: now.Add(-time.Minute)})
	if wait, _ := q.Next(3*time.Minute, now); wait != 3*time.Minute {
		t.Errorf("wait = %v with ResetAt in the past, want the base 3m0s", wait)
	}
}

func TestQuotaRateLimitedUntilReset(t *testing.T) {
	now := time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)
	var q Quota
	q.Observe(github.RateLimit{Remaining: 4000, ResetAt: now.Add(time.Hour)})
	q.ObserveRateLimited(now.Add(10 * time.Minute))
	wait, reason := q.Next(3*time.Minute, now)
	if wait != 10*time.Minute+5*time.Second || reason != "GitHub rate limit low — resuming 15:10" {
		t.Errorf("Next = %v, %q; want 10m5s, resuming 15:10", wait, reason)
	}
}

func TestQuotaRateLimitedBacksOffExponentially(t *testing.T) {
	now := time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)
	base := 3 * time.Minute
	var q Quota
	for i, want := range []time.Duration{6 * time.Minute, 12 * time.Minute, 24 * time.Minute, 30 * time.Minute, 30 * time.Minute} {
		q.ObserveRateLimited(time.Time{})
		wait, reason := q.Next(base, now)
		if wait != want {
			t.Errorf("after %d rate-limit errors wait = %v, want %v", i+1, wait, want)
		}
		if !strings.HasPrefix(reason, "GitHub rate limit low — resuming ") {
			t.Errorf("after %d rate-limit errors reason = %q", i+1, reason)
		}
	}
	q.ObserveSuccess()
	if wait, reason := q.Next(base, now); wait != base || reason != "" {
		t.Errorf("after a success Next = %v, %q; want %v, \"\"", wait, reason, base)
	}
}

func TestQuotaRateLimitedPastResetBacksOff(t *testing.T) {
	now := time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)
	var q Quota
	q.ObserveRateLimited(now.Add(-time.Minute))
	if wait, _ := q.Next(3*time.Minute, now); wait != 6*time.Minute {
		t.Errorf("wait = %v with a reset already past, want the 6m0s backoff", wait)
	}
}
