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
		{"merge of own commits only", "M", commits[3:], false},
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
