package status

import (
	"testing"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
)

func act(kind github.ActivityKind, author string, bot bool, at int, text string) github.ActivityItem {
	return github.ActivityItem{Kind: kind, Author: author, Bot: bot, At: t0.Add(time.Duration(at) * time.Minute), Text: text}
}

func texts(es []ActivityEntry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Item.Text)
	}
	return out
}

func eq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s = %q, want %q", what, got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s = %q, want %q", what, got, want)
			return
		}
	}
}

func TestActivityGroupsAroundYourReview(t *testing.T) {
	yours := rev("CHANGES_REQUESTED", "fix it", "A", 10)
	items := []github.ActivityItem{
		act(github.ActivityComment, "ann", false, 1, "early comment"),
		act(github.ActivityComment, "ci", true, 2, "old bot"),
		act(github.ActivityReview, viewer, false, 10, "fix it"),
		act(github.ActivityComment, "ci", true, 11, "new bot"),
		act(github.ActivityComment, "ann", false, 12, "reply"),
		act(github.ActivityComment, "ci", true, 13, "newer bot"),
	}
	v := GroupActivity(items, &yours, viewer)
	if v.Yours != &yours {
		t.Error("the divider should be your review")
	}
	eq(t, "since", texts(v.Since.Entries), []string{"reply"})
	eq(t, "since bots", texts(v.Since.Bots), []string{"newer bot", "new bot"})
	eq(t, "before", texts(v.Before.Entries), []string{"early comment"})
	eq(t, "before bots", texts(v.Before.Bots), []string{"old bot"})
}

func TestActivityCollapsesCommitRuns(t *testing.T) {
	items := []github.ActivityItem{
		act(github.ActivityCommit, "ann", false, 1, "c1"),
		act(github.ActivityCommit, "ann", false, 2, "c2"),
		act(github.ActivityCommit, "ann", false, 3, "c3"),
		act(github.ActivityCommit, "ben", false, 4, "b1"),
		act(github.ActivityComment, "ben", false, 5, "note"),
		act(github.ActivityCommit, "ben", false, 6, "b2"),
	}
	v := GroupActivity(items, nil, viewer)
	eq(t, "entries", texts(v.Since.Entries), []string{"b2", "note", "b1", "c3"})
	run := v.Since.Entries[3].Commits
	eq(t, "ann's run", []string{run[0].Text, run[1].Text, run[2].Text}, []string{"c3", "c2", "c1"})
	if v.Since.Entries[2].Commits != nil {
		t.Error("a lone commit should have no run")
	}
}

func TestActivityWithoutYourReview(t *testing.T) {
	items := []github.ActivityItem{
		act(github.ActivityReview, viewer, false, 1, "an old review of yours that isn't the latest"),
		act(github.ActivityComment, "ci", true, 2, "bot"),
	}
	v := GroupActivity(items, nil, viewer)
	if v.Yours != nil || len(v.Before.Entries)+len(v.Before.Bots) != 0 {
		t.Errorf("no review: %+v, want everything on one side", v)
	}
	eq(t, "since", texts(v.Since.Entries), []string{"an old review of yours that isn't the latest"})
	eq(t, "bots", texts(v.Since.Bots), []string{"bot"})
}

func TestActivityYourReviewOlderThanWindow(t *testing.T) {
	yours := rev("APPROVED", "", "A", 0)
	items := []github.ActivityItem{act(github.ActivityComment, "ann", false, 5, "later")}
	v := GroupActivity(items, &yours, viewer)
	eq(t, "since", texts(v.Since.Entries), []string{"later"})
	if len(v.Before.Entries)+len(v.Before.Bots) != 0 {
		t.Errorf("before = %+v, want nothing", v.Before)
	}
}
