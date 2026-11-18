package status

import (
	"testing"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
)

const viewer = "me"

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func snap(mut func(*github.Snapshot)) github.Snapshot {
	s := github.Snapshot{
		State:   github.PROpen,
		HeadOID: "B",
		Commits: []github.Commit{{OID: "A", Parents: []string{"base"}}, {OID: "B", Parents: []string{"A"}}},
	}
	if mut != nil {
		mut(&s)
	}
	return s
}

func rev(state, body, oid string, at int) github.Review {
	return github.Review{Author: viewer, State: state, Body: body, CommitOID: oid, SubmittedAt: t0.Add(time.Duration(at) * time.Minute)}
}

func TestDerivePrecedence(t *testing.T) {
	failing := []github.Check{{Name: "build", State: github.CheckFailure}}
	pending := []github.Check{{Name: "build", State: github.CheckPending}}
	green := []github.Check{{Name: "build", State: github.CheckSuccess}}

	tests := []struct {
		name     string
		pr       github.Snapshot
		want     Status
		spinning bool
	}{
		{"merged beats everything", snap(func(s *github.Snapshot) { s.State = github.PRMerged; s.Checks = failing }), Merged, false},
		{"closed", snap(func(s *github.Snapshot) { s.State = github.PRClosed; s.Checks = failing }), Closed, false},
		{"approved overall beats CI failed", snap(func(s *github.Snapshot) { s.ReviewDecision = "APPROVED"; s.Checks = failing }), Approved, false},
		{"CI failed", snap(func(s *github.Snapshot) { s.Checks = failing }), CIFailed, false},
		{"CI failed beats your review", snap(func(s *github.Snapshot) {
			s.Checks = failing
			s.Reviews = []github.Review{rev("APPROVED", "", "B", 1)}
		}), CIFailed, false},
		{"push after your review while CI runs", snap(func(s *github.Snapshot) {
			s.Checks = pending
			s.Reviews = []github.Review{rev("APPROVED", "", "A", 1)}
		}), NewChanges, true},
		{"push after your review, CI green", snap(func(s *github.Snapshot) {
			s.Checks = green
			s.Reviews = []github.Review{rev("APPROVED", "", "A", 1)}
		}), NewChanges, false},
		{"you approved at head, needs more", snap(func(s *github.Snapshot) {
			s.ReviewDecision = "REVIEW_REQUIRED"
			s.Reviews = []github.Review{rev("APPROVED", "", "B", 1)}
		}), ApprovedByYou, false},
		{"you requested changes at head", snap(func(s *github.Snapshot) {
			s.Reviews = []github.Review{rev("CHANGES_REQUESTED", "", "B", 1)}
		}), ChangesRequested, false},
		{"thread reply only is not a review", snap(func(s *github.Snapshot) {
			s.Checks = green
			s.Reviews = []github.Review{rev("COMMENTED", "", "B", 1)}
		}), ReadyForReview, false},
		{"force-push removed reviewed commit", snap(func(s *github.Snapshot) {
			s.Reviews = []github.Review{rev("APPROVED", "", "X", 1)}
		}), NewChanges, false},
		{"base merge only after your review", snap(func(s *github.Snapshot) {
			s.HeadOID = "M"
			s.Commits = append(s.Commits, github.Commit{OID: "M", Parents: []string{"B", "mainTip"}})
			s.Reviews = []github.Review{rev("CHANGES_REQUESTED", "", "B", 1)}
		}), ChangesRequested, false},
		{"unreviewed, CI pending", snap(func(s *github.Snapshot) { s.Checks = pending }), CIWaiting, true},
		{"unreviewed, no checks", snap(nil), ReadyForReview, false},
		{"other users' reviews ignored", snap(func(s *github.Snapshot) {
			r := rev("APPROVED", "", "B", 1)
			r.Author = "someone"
			s.Reviews = []github.Review{r}
		}), ReadyForReview, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Derive(Input{PR: tt.pr, Viewer: viewer})
			if got.Status != tt.want {
				t.Errorf("Status = %s, want %s", got.Status, tt.want)
			}
			if got.Spinning != tt.spinning {
				t.Errorf("Spinning = %v, want %v", got.Spinning, tt.spinning)
			}
		})
	}
}

func TestDeriveReportsCIAndYourReview(t *testing.T) {
	pr := snap(func(s *github.Snapshot) {
		s.Checks = []github.Check{{Name: "build", State: github.CheckFailure}}
		s.Reviews = []github.Review{rev("APPROVED", "", "B", 1)}
	})
	got := Derive(Input{PR: pr, Viewer: viewer})
	if got.CI != CIFailedState {
		t.Errorf("CI = %q, want %q", got.CI, CIFailedState)
	}
	if got.YourReview == nil || got.YourReview.State != "APPROVED" {
		t.Errorf("YourReview = %+v, want the APPROVED review", got.YourReview)
	}
}

func TestDeriveHonorsIgnoreChecks(t *testing.T) {
	pr := snap(func(s *github.Snapshot) {
		s.Checks = []github.Check{{Name: "codeowners-gate / review", State: github.CheckFailure, Required: true}}
	})
	got := Derive(Input{PR: pr, Viewer: viewer, IgnoreChecks: []string{"codeowners-gate*"}})
	if got.Status != ReadyForReview {
		t.Errorf("Status = %s, want READY_FOR_REVIEW", got.Status)
	}
}

func TestLanes(t *testing.T) {
	tests := map[Status]Lane{
		CIWaiting: LaneCI, CIFailed: LaneCI,
		ReadyForReview:   LaneReady,
		NewChanges:       LaneNew,
		ChangesRequested: LaneAuthor,
		Approved:         LaneApproved, ApprovedByYou: LaneApproved,
		Merged: LaneDone, Closed: LaneDone,
	}
	for s, want := range tests {
		if got := s.Lane(); got != want {
			t.Errorf("%s.Lane() = %v, want %v", s, got, want)
		}
	}
}
