package status

import (
	"slices"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
)

// ActivityEntry is one line of the Activity tab: an item, or a run of
// commits by one author with nothing between them (G4), newest first.
type ActivityEntry struct {
	Item    github.ActivityItem   // the item, or the run's newest commit
	Commits []github.ActivityItem // the run, newest first; nil for a lone item
}

// ActivitySide is one side of the divider: people's entries, then the
// bots', which the tab folds (plan 5).
type ActivitySide struct {
	Entries, Bots []ActivityEntry
}

// ActivityView is a PR's timeline as the Activity tab shows it. Since is
// everything after your latest review, or everything when you haven't
// reviewed; Before is the rest. Yours is the divider, nil without one.
type ActivityView struct {
	Since, Before ActivitySide
	Yours         *github.Review
}

// GroupActivity lays out items (oldest first, as GitHub returns them)
// around your latest review: newest first, each side's commit runs
// collapsed and its bots moved to a fold, so no bot crosses the divider.
// Your review itself is the divider, not an entry.
func GroupActivity(items []github.ActivityItem, yours *github.Review, viewer string) ActivityView {
	newest := slices.Clone(items)
	slices.Reverse(newest)
	var since, before []github.ActivityItem
	for _, it := range newest {
		switch {
		case yours == nil || it.At.After(yours.SubmittedAt):
			since = append(since, it)
		case it.Kind == github.ActivityReview && it.Author == viewer && it.At.Equal(yours.SubmittedAt):
			// the divider
		default:
			before = append(before, it)
		}
	}
	return ActivityView{Since: side(since), Before: side(before), Yours: yours}
}

// side collapses commit runs, then moves the bots' entries to the fold. A
// bot item between two commits ends the run, as it does on GitHub.
func side(items []github.ActivityItem) ActivitySide {
	var s ActivitySide
	for i := 0; i < len(items); {
		it := items[i]
		e := ActivityEntry{Item: it}
		j := i + 1
		if it.Kind == github.ActivityCommit {
			for j < len(items) && items[j].Kind == github.ActivityCommit && items[j].Author == it.Author {
				j++
			}
			if j-i > 1 {
				e.Commits = slices.Clone(items[i:j])
			}
		}
		if it.Bot {
			s.Bots = append(s.Bots, e)
		} else {
			s.Entries = append(s.Entries, e)
		}
		i = j
	}
	return s
}
