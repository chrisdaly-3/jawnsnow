// Package demo is fake data for recording the README's demo: a GitHub
// client, a poster and a Claude reviewer that answer from canned values,
// and a Seed that fills a store with PRs, reviews and a month of costs.
// Nothing in it reaches the network or runs another program.
package demo

import (
	"crypto/sha1"
	"fmt"
	"time"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
)

// Viewer is the demo's gh user.
const Viewer = "you"

// oid is a stable fake commit ID.
func oid(name string) string { return fmt.Sprintf("%x", sha1.Sum([]byte(name))) }

func prID(repo string, number int) string {
	return fmt.Sprintf("PR_demo_%s_%d", repo[len("acme/"):], number)
}

func green(names ...string) []github.Check {
	out := make([]github.Check, len(names))
	for i, n := range names {
		out[i] = github.Check{Name: n, State: github.CheckSuccess, Required: true}
	}
	return out
}

// pr is one demo PR before the per-status fields.
type pr struct {
	repo   string
	number int
	title  string
	author string
	// age is how long ago the head was pushed; updated is how long ago the
	// PR last changed.
	age, updated time.Duration
	add, del     int
	labels       []string
}

func (p pr) snapshot(now time.Time) github.Snapshot {
	head := oid(fmt.Sprintf("%s#%d head", p.repo, p.number))
	first := oid(fmt.Sprintf("%s#%d first", p.repo, p.number))
	return github.Snapshot{
		ID: prID(p.repo, p.number), Repo: p.repo, Number: p.number,
		Title: p.title, Author: p.author,
		URL:   fmt.Sprintf("https://github.com/%s/pull/%d", p.repo, p.number),
		State: github.PROpen, HeadOID: head, BaseRef: "main", ReviewDecision: "REVIEW_REQUIRED",
		HeadCommittedAt: now.Add(-p.age), RollupState: "SUCCESS",
		Additions: p.add, Deletions: p.del, Labels: p.labels,
		UpdatedAt: now.Add(-p.updated),
		Checks:    green("build", "test", "lint"),
		Commits:   []github.Commit{{OID: first}, {OID: head, Parents: []string{first}}},
	}
}

// snapshots is the demo's PRs as GitHub would return them at now: one or
// more for each status the Wall has, and one merged PR for the Done view.
func snapshots(now time.Time) []github.Snapshot {
	var out []github.Snapshot
	add := func(p pr, change func(*github.Snapshot)) {
		s := p.snapshot(now)
		if change != nil {
			change(&s)
		}
		out = append(out, s)
	}
	pending := func(s *github.Snapshot) {
		s.Checks = append(green("build", "lint"), github.Check{Name: "test", State: github.CheckPending, Required: true})
		s.RollupState = "PENDING"
	}
	yours := func(state, body string, at time.Duration, commit func(*github.Snapshot) string) func(*github.Snapshot) {
		return func(s *github.Snapshot) {
			s.Reviews = append(s.Reviews, github.Review{
				Author: Viewer, State: state, Body: body, CommitOID: commit(s), SubmittedAt: now.Add(-at),
			})
		}
	}
	atHead := func(s *github.Snapshot) string { return s.HeadOID }
	atFirst := func(s *github.Snapshot) string { return s.Commits[0].OID }

	// Waiting on CI: checks still running, so the card spins.
	add(pr{repo: "acme/api", number: 412, title: "Add a retry budget to the webhook dispatcher", author: "alice",
		age: 6 * time.Minute, updated: 5 * time.Minute, add: 184, del: 31}, pending)
	add(pr{repo: "acme/web", number: 238, title: "Lazy-load the billing settings page", author: "bob",
		age: 14 * time.Minute, updated: 12 * time.Minute, add: 96, del: 40}, pending)
	// CI failed.
	add(pr{repo: "acme/infra", number: 97, title: "Raise the worker Postgres connection pool", author: "carol",
		age: 50 * time.Minute, updated: 40 * time.Minute, add: 12, del: 4}, func(s *github.Snapshot) {
		// An optional check: it fails the PR all the same.
		s.Checks = append(green("build", "lint"), github.Check{Name: "terraform-plan", State: github.CheckFailure})
		s.RollupState = "FAILURE"
	})
	// Ready for you. The code-owner gate waits on a review and the demo
	// config ignores it; the deploy preview waits for a reviewer to approve.
	// Neither keeps the PR out of the lane.
	add(pr{repo: "acme/api", number: 405, title: "Verify webhook signatures before parsing the body", author: "bob",
		age: 3 * time.Hour, updated: 2 * time.Hour, add: 142, del: 58, labels: []string{"security"}}, func(s *github.Snapshot) {
		s.Checks = append(green("build", "lint", "test"),
			github.Check{Name: "codeowners-gate", State: github.CheckPending, Required: true},
			github.Check{Name: "deploy-preview", State: github.CheckActionRequired})
		s.RollupState = "PENDING"
	})
	add(pr{repo: "acme/web", number: 241, title: "Fix the date picker in Safari", author: "carol",
		age: 90 * time.Minute, updated: 80 * time.Minute, add: 38, del: 11}, nil)
	add(pr{repo: "acme/infra", number: 101, title: "Move the staging DNS records to Terraform", author: "alice",
		age: 5 * time.Hour, updated: 4 * time.Hour, add: 210, del: 0}, nil)
	// New changes since your review.
	add(pr{repo: "acme/api", number: 398, title: "Paginate the audit log export", author: "carol",
		age: 45 * time.Minute, updated: 30 * time.Minute, add: 167, del: 49},
		yours("COMMENTED", "The cursor needs a tiebreak; details inline.", 20*time.Hour, atFirst))
	// Waiting on author: you asked for changes at the head.
	add(pr{repo: "acme/web", number: 230, title: "Drop the legacy session cookie", author: "alice",
		age: 26 * time.Hour, updated: 22 * time.Hour, add: 23, del: 117},
		yours("CHANGES_REQUESTED", "Old sessions are logged out with no warning; see inline.", 22*time.Hour, atHead))
	// Approved by two people.
	add(pr{repo: "acme/infra", number: 95, title: "Pin the Terraform provider versions", author: "bob",
		age: 28 * time.Hour, updated: 3 * time.Hour, add: 18, del: 18}, func(s *github.Snapshot) {
		s.ReviewDecision = "APPROVED"
		s.Reviews = []github.Review{
			{Author: "carol", State: "APPROVED", CommitOID: s.HeadOID, SubmittedAt: now.Add(-4 * time.Hour)},
			{Author: "dave", State: "APPROVED", CommitOID: s.HeadOID, SubmittedAt: now.Add(-3 * time.Hour)},
		}
	})
	// Approved by you, still waiting on another reviewer.
	add(pr{repo: "acme/api", number: 401, title: "Add request IDs to the structured logs", author: "alice",
		age: 30 * time.Hour, updated: 7 * time.Hour, add: 74, del: 9},
		yours("APPROVED", "", 7*time.Hour, atHead))
	// Merged, for the Done view.
	add(pr{repo: "acme/web", number: 226, title: "Upgrade the charting library", author: "bob",
		age: 30 * time.Hour, updated: 5 * time.Hour, add: 61, del: 64}, func(s *github.Snapshot) {
		s.State, s.MergedAt, s.ReviewDecision = github.PRMerged, now.Add(-5*time.Hour), "APPROVED"
	})
	return out
}

// contexts are the PR bodies and changed files a review reads.
var contexts = map[string]github.PRContext{
	prID("acme/api", 405): {
		Body:  "Checks `X-Acme-Signature` before the handler decodes anything, so an unsigned delivery is rejected without parsing its body.",
		Files: []string{"internal/webhook/handler.go", "internal/webhook/verify.go", "internal/webhook/verify_test.go"},
	},
	prID("acme/web", 241): {
		Body:  "Safari parses `YYYY-MM-DD` as UTC, so the picker showed the day before in US time zones. Parse dates as local.",
		Files: []string{"src/components/DatePicker.tsx", "src/lib/dates.ts", "src/lib/dates.test.ts"},
	},
	prID("acme/infra", 101): {
		Body:  "Imports the staging zone's records into Terraform. Applied in staging; the plan is clean.",
		Files: []string{"dns/staging.tf", "dns/variables.tf"},
	},
	prID("acme/api", 398): {
		Body:  "Streams the audit log export a page at a time instead of loading it whole.",
		Files: []string{"internal/audit/export.go", "internal/audit/export_test.go"},
	},
}

func contextFor(id string) github.PRContext {
	if c, ok := contexts[id]; ok {
		return c
	}
	return github.PRContext{Body: "A demo pull request.", Files: []string{"README.md"}}
}
