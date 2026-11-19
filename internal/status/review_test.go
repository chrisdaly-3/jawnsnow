package status

import (
	"testing"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
)

func TestYourLatestReviewSkipsPendingAndReplies(t *testing.T) {
	other := rev("APPROVED", "", "B", 50)
	other.Author = "someone"
	reviews := []github.Review{
		rev("CHANGES_REQUESTED", "", "A", 1),
		rev("COMMENTED", "  \n", "A", 2), // thread reply
		rev("PENDING", "draft", "B", 3),
		other,
	}
	got := YourLatestReview(reviews, viewer)
	if got == nil || got.State != "CHANGES_REQUESTED" {
		t.Fatalf("got %+v, want the CHANGES_REQUESTED review", got)
	}
}

func TestYourLatestReviewUsesSubmittedAtNotOrder(t *testing.T) {
	reviews := []github.Review{
		rev("APPROVED", "", "B", 9),
		rev("CHANGES_REQUESTED", "", "A", 1),
	}
	got := YourLatestReview(reviews, viewer)
	if got == nil || got.State != "APPROVED" {
		t.Fatalf("got %+v, want APPROVED", got)
	}
}

func TestYourLatestReviewCountsCommentWithBodyAndDismissed(t *testing.T) {
	if got := YourLatestReview([]github.Review{rev("COMMENTED", "looks odd", "A", 1)}, viewer); got == nil {
		t.Error("COMMENTED with body should count")
	}
	if got := YourLatestReview([]github.Review{rev("DISMISSED", "", "A", 1)}, viewer); got == nil {
		t.Error("DISMISSED should count")
	}
	if got := YourLatestReview(nil, viewer); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

func TestOnlyBaseMergesSince(t *testing.T) {
	commits := []github.Commit{
		{OID: "A", Parents: []string{"base"}},
		{OID: "B", Parents: []string{"A"}},
		{OID: "M", Parents: []string{"B", "mainTip"}},
		{OID: "C", Parents: []string{"M"}},
		{OID: "M2", Parents: []string{"M", "B"}}, // both parents in PR
	}
	tests := []struct {
		name  string
		since string
		cs    []github.Commit
		want  bool
	}{
		{"base merge only", "B", commits[:3], true},
		{"real commit after merge", "B", commits[:4], false},
		{"merge of own commits only", "C", commits, false},
		{"unknown since (force-push)", "X", commits, false},
		{"nothing after since", "B", commits[:2], true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OnlyBaseMergesSince(tt.cs, tt.since); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCountsTable(t *testing.T) {
	withThreads := func(state, body string, n int) github.Review {
		r := rev(state, body, "A", 1)
		r.NewThreads = n
		return r
	}
	tests := []struct {
		name string
		r    github.Review
		want bool
	}{
		{"inline-only comment review", withThreads("COMMENTED", "", 1), true},
		{"thread reply", withThreads("COMMENTED", "", 0), false},
		{"blank body, no thread", withThreads("COMMENTED", " \n", 0), false},
		{"comment with body", withThreads("COMMENTED", "hi", 0), true},
		{"pending with threads", withThreads("PENDING", "", 2), false},
		{"approved", withThreads("APPROVED", "", 0), true},
		{"changes requested", withThreads("CHANGES_REQUESTED", "", 0), true},
		{"dismissed", withThreads("DISMISSED", "", 0), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := YourLatestReview([]github.Review{tt.r}, viewer) != nil
			if got != tt.want {
				t.Errorf("counts = %v, want %v", got, tt.want)
			}
		})
	}
}
