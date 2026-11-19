package demo

import (
	"fmt"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/store"
)

const (
	opus   = "claude-opus-5-5"
	sonnet = "claude-sonnet-5-5"
	haiku  = "claude-haiku-5-5"
)

// run is one claude run of a review: its share of the cost and how long
// it took.
type run struct {
	cost float64
	took time.Duration
}

// seededReview is a review Seed saves, with the runs its ledger rows record.
type seededReview struct {
	review store.Review
	runs   []run
}

func effortOf(model string) string {
	switch model {
	case opus:
		return "high"
	case haiku:
		return "low"
	}
	return "medium"
}

// newReview fills the fields every seeded review shares. at is when it
// finished.
func newReview(id string, s pr, head, tier, mode, model, verdict string, at time.Time, runs []run) seededReview {
	var took time.Duration
	var cost float64
	for _, r := range runs {
		took += r.took
		cost += r.cost
	}
	return seededReview{review: store.Review{
		ID: id, PRID: prID(s.repo, s.number), Repo: s.repo, Number: s.number, Author: s.author,
		HeadOID: head, BaseOID: oid(s.repo + " main"), MergeBaseOID: oid(s.repo + " main"),
		Tier: tier, PassKind: "full", Mode: mode, Model: model, Effort: effortOf(model),
		SessionID: id + "-session", Verdict: verdict, Outcome: "complete", CostUSD: cost,
		StartedAt: at.Add(-took), EndedAt: at,
	}, runs: runs}
}

// drawerReviews are the reviews of PRs on the wall, the ones the drawer
// and the post preview show.
func drawerReviews(now time.Time) []seededReview {
	var out []seededReview
	byNumber := map[int]prCommits{}
	for _, s := range snapshots(now) {
		byNumber[s.Number] = prCommits{head: s.HeadOID, first: s.Commits[0].OID}
	}

	webhook := pr{repo: "acme/api", number: 405, author: "bob"}
	r := newReview("demo-review-405", webhook, byNumber[405].head, "first_pass", "manual", opus, "request_changes",
		now.Add(-95*time.Minute), []run{{1.18, 6 * time.Minute}, {0.24, 70 * time.Second}})
	r.review.VerdictReason = "The timing-unsafe signature check and the drained body both need fixing before this merges."
	r.review.Understanding = "Moves signature checking ahead of JSON decoding in the webhook handler. " +
		"`verify` reads the body, checks `X-Acme-Signature` against an HMAC of it, and rejects deliveries older than five minutes."
	r.review.Assumptions = []store.Assumption{
		{Claim: "Every sender sets `X-Acme-Signature`.", Evidence: "docs/webhooks.md lists it as a required header."},
		{Claim: "The shared secret is the same for every endpoint.", Evidence: "`config.WebhookSecret` is the only secret `verify` reads."},
	}
	r.review.Findings = []store.Finding{
		{Path: "internal/webhook/verify.go", Line: 42, Category: "security", Blocking: true, Ledger: "new",
			Body: "**The signature is compared with `bytes.Equal`**, which returns at the first byte that differs, so response times leak how much of a forged signature is right. Compare in constant time:\n\n```suggestion\n\tif !hmac.Equal(got, want) {\n```"},
		{Path: "internal/webhook/handler.go", Line: 88, StartLine: 85, Category: "bug", Blocking: true, Ledger: "new",
			Body: "`verify` drains `r.Body` with `io.ReadAll`, so `json.NewDecoder(r.Body)` on line 88 reads an empty body and every delivery fails with `EOF`. Decode the bytes `verify` already read: have it return them, then `json.Unmarshal(body, &event)`."},
		{Path: "internal/webhook/verify_test.go", Line: 12, Category: "test-gap", Ledger: "new",
			Body: "No test sends a delivery with an old `X-Acme-Timestamp`. Add one six minutes old that expects `401`, so the five-minute window stays covered."},
	}
	out = append(out, r)

	dns := pr{repo: "acme/infra", number: 101, author: "alice"}
	r = newReview("demo-review-101", dns, byNumber[101].head, "first_pass", "auto", opus, "comment",
		now.Add(-3*time.Hour), []run{{0.86, 5 * time.Minute}})
	r.review.VerdictReason = "The records match the zone; two points to settle before the next change."
	r.review.Understanding = "Adds every record in the staging zone to `dns/staging.tf` as `aws_route53_record` resources, so later changes go through review."
	r.review.Findings = []store.Finding{
		{Path: "dns/staging.tf", Line: 30, Category: "design", Ledger: "new",
			Body: "The apex record has `ttl = 60`, where the others use `300`. A short TTL helps during the cutover; raise it afterwards so resolvers don't query the zone every minute."},
		{Path: "dns/staging.tf", Unanchored: true, Category: "design", Ledger: "new",
			Body: "The description says the records were imported, but the diff has no `import` blocks. If the import ran by hand, list the `terraform import` commands in the PR, so the next person to apply doesn't recreate the records."},
	}
	out = append(out, r)

	audit := pr{repo: "acme/api", number: 398, author: "carol"}
	first := newReview("demo-review-398-1", audit, byNumber[398].first, "first_pass", "auto", opus, "comment",
		now.Add(-21*time.Hour), []run{{1.12, 7 * time.Minute}})
	first.review.VerdictReason = "Pages can repeat or skip rows; the cursor needs a tiebreak."
	first.review.Understanding = "Streams the audit log export in pages of 500, keyed by a cursor on `created_at`."
	first.review.Findings = []store.Finding{
		{Path: "internal/audit/export.go", Line: 64, Category: "bug", Blocking: true, Ledger: "new",
			Body: "The cursor is `created_at` alone, and bulk imports write many rows with the same timestamp. A page boundary inside such a run skips or repeats rows. Order and page by `(created_at, id)`."},
	}
	out = append(out, first)
	follow := newReview("demo-review-398-2", audit, byNumber[398].head, "follow_up", "auto", sonnet, "approve",
		now.Add(-25*time.Minute), []run{{0.31, 3 * time.Minute}})
	follow.review.PassKind = "incremental"
	follow.review.VerdictReason = "The cursor now pages by `(created_at, id)` and a test covers equal timestamps."
	follow.review.Understanding = "The new commit pages by `(created_at, id)` and adds a test with 1,200 rows that share a timestamp."
	follow.review.Findings = []store.Finding{
		{Path: "internal/audit/export.go", Line: 64, Category: "bug", Blocking: true, Ledger: "addressed",
			Body: first.review.Findings[0].Body},
		{Path: "internal/audit/export.go", Line: 21, Category: "design", Ledger: "new",
			Body: "nit: `defaultPageSize` is `500`, but the handler's doc comment says pages hold 100 rows. Update the comment."},
	}
	out = append(out, follow)
	return out
}

// prCommits are the two commits a demo PR has.
type prCommits struct{ head, first string }

// history is a month of earlier reviews of PRs that have since merged, so
// the Cost view has a shape and the Reviews view a past.
func history(now time.Time) []seededReview {
	type past struct {
		daysAgo, hour int
		repo          string
		number        int
		author, tier  string
		model         string
		cost          float64
		verdict       string
		findings      int
	}
	rows := []past{
		{29, 10, "acme/api", 351, "alice", "first_pass", opus, 1.64, "request_changes", 3},
		{28, 15, "acme/api", 351, "alice", "follow_up", sonnet, 0.42, "approve", 0},
		{27, 11, "acme/web", 201, "bob", "first_pass", opus, 0.93, "comment", 2},
		{26, 9, "acme/infra", 80, "carol", "first_pass", haiku, 0.08, "approve", 0},
		{24, 14, "acme/web", 203, "carol", "first_pass", opus, 1.21, "comment", 1},
		{23, 16, "acme/web", 203, "carol", "follow_up", sonnet, 0.27, "approve", 0},
		{21, 10, "acme/api", 362, "bob", "first_pass", opus, 1.87, "request_changes", 4},
		{20, 13, "acme/api", 362, "bob", "follow_up", sonnet, 0.55, "comment", 1},
		{19, 11, "acme/api", 362, "bob", "follow_up", haiku, 0.11, "approve", 0},
		{17, 15, "acme/infra", 84, "alice", "first_pass", opus, 0.74, "comment", 1},
		{16, 10, "acme/web", 210, "alice", "first_pass", sonnet, 0.48, "approve", 0},
		{14, 9, "acme/api", 370, "carol", "first_pass", opus, 1.35, "request_changes", 2},
		{13, 14, "acme/api", 370, "carol", "follow_up", sonnet, 0.36, "approve", 0},
		{12, 11, "acme/infra", 88, "bob", "first_pass", haiku, 0.06, "approve", 0},
		{10, 16, "acme/web", 215, "bob", "first_pass", opus, 1.09, "comment", 2},
		{9, 10, "acme/web", 215, "bob", "follow_up", sonnet, 0.29, "approve", 0},
		{7, 13, "acme/api", 383, "alice", "first_pass", opus, 1.52, "request_changes", 3},
		{6, 15, "acme/api", 383, "alice", "follow_up", sonnet, 0.44, "comment", 1},
		{5, 11, "acme/infra", 90, "carol", "first_pass", opus, 0.68, "comment", 1},
		{4, 10, "acme/api", 383, "alice", "follow_up", haiku, 0.09, "approve", 0},
		{3, 14, "acme/web", 222, "carol", "first_pass", opus, 1.26, "comment", 2},
		{2, 9, "acme/web", 222, "carol", "follow_up", sonnet, 0.33, "approve", 0},
		{1, 16, "acme/infra", 92, "bob", "first_pass", sonnet, 0.39, "approve", 0},
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var out []seededReview
	for i, p := range rows {
		at := day.AddDate(0, 0, -p.daysAgo).Add(time.Duration(p.hour) * time.Hour)
		s := pr{repo: p.repo, number: p.number, author: p.author}
		mode := "auto"
		if i%3 == 0 {
			mode = "manual"
		}
		r := newReview(fmt.Sprintf("demo-history-%02d", i), s, oid(fmt.Sprintf("%s#%d %d", p.repo, p.number, i)),
			p.tier, mode, p.model, p.verdict, at, []run{{p.cost, time.Duration(2+i%6) * time.Minute}})
		r.review.Understanding = "An earlier change, reviewed and merged."
		for j := range p.findings {
			f := pastFindings[(i+j)%len(pastFindings)]
			f.Ledger = "new"
			f.Blocking = p.verdict == "request_changes" && j == 0
			r.review.Findings = append(r.review.Findings, f)
		}
		out = append(out, r)
	}
	return out
}

var pastFindings = []store.Finding{
	{Path: "internal/store/cache.go", Line: 51, Category: "bug", Body: "`Get` reads `c.items` without `c.mu`; `Set` writes it under the lock."},
	{Path: "src/api/client.ts", Line: 118, Category: "bug", Body: "The retry loop never resets `delay`, so the third retry waits 8s, not 2s."},
	{Path: "main.tf", Line: 22, Category: "design", Body: "The bucket name is hard-coded; every other module takes it as a variable."},
	{Path: "internal/auth/token_test.go", Line: 40, Category: "test-gap", Body: "No test covers a token that expires mid-request."},
	{Path: "src/pages/Settings.tsx", Line: 73, Category: "bug", Body: "`useEffect` has no dependency array, so it refetches on every render."},
}
