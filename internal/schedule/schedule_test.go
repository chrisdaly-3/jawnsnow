package schedule

import (
	"testing"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/status"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
)

var now = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

// base is a candidate that would be reviewed; each row breaks one thing.
func base() Candidate {
	return Candidate{T: tracker.Tracked{
		PR:         github.Snapshot{HeadOID: "h1", Author: "alice"},
		Status:     status.Result{Status: status.ReadyForReview, CI: status.CIGreen},
		HeadSeenAt: now.Add(-time.Hour),
	}}
}

func TestDecideOrder(t *testing.T) {
	cfg := config.Defaults()
	failed := &store.Review{HeadOID: "h1", Outcome: "failed"}
	tests := []struct {
		name  string
		mod   func(*Candidate)
		spent float64
		want  Decision
	}{
		{"running", func(c *Candidate) { c.Running = true; c.T.Status.Status = status.CIWaiting }, 0, Decision{false, "running"}},
		{"status", func(c *Candidate) { c.T.Status.Status = status.NewChanges; c.T.Status.CI = status.CIFailedState }, 0, Decision{false, "status"}},
		{"quiet", func(c *Candidate) { c.T.HeadSeenAt = now.Add(-time.Minute) }, 0, Decision{false, "quiet"}},
		{"unknown head time is quiet", func(c *Candidate) { c.T.HeadSeenAt = time.Time{} }, 0, Decision{false, "quiet"}},
		{"reviewed", func(c *Candidate) { c.Latest = &store.Review{HeadOID: "h1", Outcome: "complete"} }, 0, Decision{false, "reviewed"}},
		{"reviewed incomplete", func(c *Candidate) { c.Latest = &store.Review{HeadOID: "h1", Outcome: "incomplete"} }, 0, Decision{false, "reviewed"}},
		{"another reviewer at head does not block", func(c *Candidate) {
			c.T.PR.Reviews = []github.Review{{Author: "bob", State: "APPROVED", CommitOID: "h1"}}
		}, 0, Decision{true, ""}},
		{"cap", func(c *Candidate) { c.AutoToday = 4 }, 0, Decision{false, "cap"}},
		{"budget", func(c *Candidate) {}, 250, Decision{false, "budget"}},
		{"stopped", func(c *Candidate) { c.Latest = failed; c.Failures = 4; c.LastFailure = now.Add(-24 * time.Hour) }, 0, Decision{false, "stopped"}},
		{"backoff", func(c *Candidate) { c.Latest = failed; c.Failures = 1; c.LastFailure = now.Add(-30 * time.Second) }, 0, Decision{false, "backoff"}},
		{"backoff elapsed", func(c *Candidate) { c.Latest = failed; c.Failures = 1; c.LastFailure = now.Add(-2 * time.Minute) }, 0, Decision{true, ""}},
		{"failure at older head does not block", func(c *Candidate) {
			c.Latest = &store.Review{HeadOID: "h0", Outcome: "failed"}
			c.Failures = 5
			c.LastFailure = now
		}, 0, Decision{true, ""}},
		{"new changes with green CI", func(c *Candidate) { c.T.Status.Status = status.NewChanges }, 0, Decision{true, ""}},
		{"ready", func(c *Candidate) {}, 0, Decision{true, ""}},
		{"order: running before status", func(c *Candidate) { c.Running = true; c.T.Status.Status = status.CIWaiting; c.AutoToday = 9 }, 999, Decision{false, "running"}},
		{"order: cap before budget", func(c *Candidate) { c.AutoToday = 4 }, 999, Decision{false, "cap"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := base()
			tt.mod(&c)
			if got := Decide(c, cfg, tt.spent, "me", now); got != tt.want {
				t.Fatalf("Decide = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestBackoff(t *testing.T) {
	tests := []struct {
		failures int
		wait     time.Duration
		stop     bool
	}{
		{1, time.Minute, false},
		{2, 5 * time.Minute, false},
		{3, 30 * time.Minute, false},
		{4, 0, true},
		{9, 0, true},
	}
	for _, tt := range tests {
		wait, stop := Backoff(tt.failures)
		if stop != tt.stop || (!stop && wait != tt.wait) {
			t.Errorf("Backoff(%d) = %v, %v; want %v, %v", tt.failures, wait, stop, tt.wait, tt.stop)
		}
	}
}

// inZone makes loc the local zone for the rest of the test.
func inZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	was := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = was })
	return loc
}

func TestBudgetResetsAtLocalMidnight(t *testing.T) {
	ny := inZone(t, "America/New_York")
	// 00:30 on the 18th in New York is 05:30 UTC the same day.
	at := time.Date(2026, 11, 18, 0, 30, 0, 0, ny)
	want := time.Date(2026, 11, 18, 0, 0, 0, 0, ny)
	if got := DayStart(at); !got.Equal(want) {
		t.Fatalf("DayStart(%v) = %v, want %v", at, got, want)
	}
	// 23:30 on the 17th local is already the 18th in UTC; still the 17th's day.
	late := time.Date(2026, 11, 17, 23, 30, 0, 0, ny)
	if got, want := DayStart(late), time.Date(2026, 11, 17, 0, 0, 0, 0, ny); !got.Equal(want) {
		t.Fatalf("DayStart(%v) = %v, want %v", late, got, want)
	}
}

func TestDayStartOfAUTCTimeIsLocalMidnight(t *testing.T) {
	ny := inZone(t, "America/New_York")
	// 03:00 UTC on the 18th is 22:00 on the 17th in New York.
	at := time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC)
	if got, want := DayStart(at), time.Date(2026, 11, 17, 0, 0, 0, 0, ny); !got.Equal(want) {
		t.Fatalf("DayStart(%v) = %v, want %v", at, got, want)
	}
}
