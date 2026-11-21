package demo

import (
	"context"
	"fmt"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/review"
	"github.com/chrisdaly-3/jawnsnow/internal/status"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
)

// Reviewer is a tui.Reviewer that runs no claude. Run waits Delay, sending
// a few made-up tool calls, then saves a canned review of the PR and its
// cost row, as the review engine would.
type Reviewer struct {
	Store *store.Store
	Delay time.Duration    // 0 waits 1.5s
	Now   func() time.Time // nil is time.Now
}

func (r *Reviewer) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reviewer) Run(ctx context.Context, req review.Request, onEvent func(review.Event)) (store.Review, error) {
	pr := req.PR.PR
	choice := review.Resolve(config.Defaults().Review, review.ResolveInput{
		Repo: pr.Repo, Author: pr.Author, Labels: pr.Labels, Lines: pr.Additions + pr.Deletions,
		HasYourReview: status.YourLatestReview(pr.Reviews, Viewer) != nil,
		Session:       req.Session, OneRun: req.OneRun,
	})
	rv := store.Review{
		ID: review.NewID(), PRID: pr.ID, Repo: pr.Repo, Number: pr.Number, Author: pr.Author,
		HeadOID: pr.HeadOID, BaseOID: oid(pr.Repo + " main"), MergeBaseOID: oid(pr.Repo + " main"),
		Tier: string(choice.Tier), PassKind: string(review.PassFull), Mode: string(req.Mode),
		Model: choice.Model, Effort: choice.Effort, StartedAt: r.now(),
	}
	rv.SessionID = rv.ID + "-session"

	delay := r.Delay
	if delay == 0 {
		delay = 1500 * time.Millisecond
	}
	files := contextFor(pr.ID).Files
	steps := len(files) + 1
	var err error
	for i := range steps {
		ev := review.Event{Tool: "Read"}
		if i < len(files) {
			ev.Path = files[i]
		} else {
			ev = review.Event{Tool: "Grep", Path: "."}
		}
		onEvent(ev)
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-time.After(delay / time.Duration(steps)):
		}
		if err != nil {
			break
		}
	}

	runOutcome := "complete"
	if err != nil {
		runOutcome = "failed"
		rv.Outcome, rv.Error = "failed", err.Error()
	} else {
		cannedReview(&rv)
	}
	ended := r.now()
	if err := r.Store.AddCost(context.WithoutCancel(ctx), store.CostRow{
		At: ended, ReviewID: rv.ID, PRID: rv.PRID, Repo: rv.Repo, Author: rv.Author,
		Tier: rv.Tier, PassKind: rv.PassKind, Mode: rv.Mode, Model: rv.Model, Effort: rv.Effort,
		Outcome: runOutcome, CostUSD: rv.CostUSD, Duration: ended.Sub(rv.StartedAt),
		TokensIn: 152_000, TokensOut: 7_400, TokensCached: 540_000,
	}); err != nil {
		return rv, fmt.Errorf("record cost: %w", err)
	}
	rv.EndedAt = ended
	if serr := r.Store.SaveReview(context.WithoutCancel(ctx), rv); serr != nil {
		return rv, fmt.Errorf("save review: %w", serr)
	}
	return rv, err
}

// cannedReview fills rv with the demo's answer: a review written for the
// Safari date picker PR, or a short one for any other.
func cannedReview(rv *store.Review) {
	rv.Outcome = "complete"
	if rv.PRID != prID("acme/web", 241) {
		rv.CostUSD = 0.64
		rv.Verdict, rv.VerdictReason = "comment", "One point to check; nothing blocks the merge."
		rv.Understanding = "A demo pull request."
		rv.Findings = []store.Finding{{Path: "README.md", Line: 1, Category: "design", Ledger: "new",
			Body: "nit: the first line still names the old command."}}
		return
	}
	rv.CostUSD = 0.92
	rv.Verdict = "comment"
	rv.VerdictReason = "The fix is right for the picker; `parseISODate` has one more caller that still parses as UTC."
	rv.Understanding = "Safari parses a bare `YYYY-MM-DD` as UTC midnight, which is the previous evening in US time zones. " +
		"The PR adds `parseLocalDate` in `src/lib/dates.ts` and uses it in the picker."
	rv.Assumptions = []store.Assumption{
		{Claim: "Dates reach the picker as `YYYY-MM-DD` strings.", Evidence: "`DatePicker` props type `value` as `IsoDate`."},
	}
	rv.Findings = []store.Finding{
		{Path: "src/components/DatePicker.tsx", Line: 57, Category: "bug", Ledger: "new",
			Body: "`minDate` still goes through `new Date(minDate)`, so the earliest selectable day is off by one in the same time zones. Use `parseLocalDate` here too:\n\n```suggestion\n  const min = parseLocalDate(minDate);\n```"},
		{Path: "src/lib/dates.test.ts", Line: 8, Category: "test-gap", Ledger: "new",
			Body: "The tests run in UTC, where the old and new parsing agree. Set `process.env.TZ = \"America/New_York\"` in this file so the test fails without the fix."},
	}
}

// ReleasePR has nothing to release: the demo makes no worktrees.
func (r *Reviewer) ReleasePR(context.Context, string, int) error { return nil }

// PruneIdle has nothing to prune.
func (r *Reviewer) PruneIdle(context.Context) error { return nil }
