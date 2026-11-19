package demo

import (
	"context"
	"fmt"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
)

// Seed writes the demo's PRs, as a poll at now would have tracked them,
// the reviews of some of them, and a month of reviews before that with
// their cost rows.
func Seed(ctx context.Context, st *store.Store, now time.Time) error {
	snaps := snapshots(now)
	found := map[string][]string{}
	for _, s := range snaps {
		found[s.ID] = []string{config.Defaults().Feeds[0].Name}
	}
	prs, _ := tracker.Apply(nil, tracker.PollInput{Found: found, Snapshots: snaps, Viewer: Viewer, Now: now}, config.Defaults())
	var ts []tracker.Tracked
	for _, s := range snaps {
		t := prs[s.ID]
		// As if snow had been running: each head was seen when it was
		// pushed. The Wall orders a lane by it.
		t.FirstSeen, t.HeadSeenAt = s.HeadCommittedAt, s.HeadCommittedAt
		ts = append(ts, t)
	}
	if err := st.SaveTracked(ctx, ts); err != nil {
		return fmt.Errorf("seed PRs: %w", err)
	}

	for _, r := range append(history(now), drawerReviews(now)...) {
		if err := save(ctx, st, r); err != nil {
			return err
		}
	}
	return nil
}

// save writes a review and one cost row per run, as the review engine does.
func save(ctx context.Context, st *store.Store, sr seededReview) error {
	r := sr.review
	if err := st.SaveReview(ctx, r); err != nil {
		return fmt.Errorf("seed review %s: %w", r.ID, err)
	}
	at := r.StartedAt
	for _, run := range sr.runs {
		at = at.Add(run.took)
		if err := st.AddCost(ctx, store.CostRow{
			At: at, ReviewID: r.ID, PRID: r.PRID, Repo: r.Repo, Author: r.Author,
			Tier: r.Tier, PassKind: r.PassKind, Mode: r.Mode, Model: r.Model, Effort: r.Effort,
			Outcome: r.Outcome, CostUSD: run.cost, Duration: run.took,
			TokensIn: int(run.cost * 180_000), TokensOut: int(run.cost * 9_000), TokensCached: int(run.cost * 650_000),
		}); err != nil {
			return fmt.Errorf("seed cost of %s: %w", r.ID, err)
		}
	}
	return nil
}
