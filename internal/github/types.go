// Package github is a read-only GraphQL client that searches a user's PR
// feeds and fetches PR detail as Snapshots.
package github

import "time"

type PRState string

const (
	PROpen   PRState = "OPEN"
	PRMerged PRState = "MERGED"
	PRClosed PRState = "CLOSED"
)

type CheckState string

const (
	CheckPending CheckState = "PENDING"
	CheckSuccess CheckState = "SUCCESS"
	CheckFailure CheckState = "FAILURE"
	CheckNeutral CheckState = "NEUTRAL"
	CheckSkipped CheckState = "SKIPPED"
)

type Check struct {
	Name     string
	State    CheckState
	Required bool
}

// Review State is one of APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED, PENDING.
type Review struct {
	Author, State, Body, CommitOID string
	SubmittedAt                    time.Time
}

type Comment struct {
	Author string
	At     time.Time
}

type Commit struct {
	OID     string
	Parents []string
}

type Snapshot struct {
	ID, Repo                         string
	Number                           int
	Title, Author, URL               string
	State                            PRState
	IsDraft                          bool
	HeadOID, BaseRef, ReviewDecision string
	HeadCommittedAt                  time.Time
	Additions, Deletions             int
	Labels                           []string
	UpdatedAt, MergedAt, ClosedAt    time.Time
	Checks                           []Check
	Reviews                          []Review
	Comments                         []Comment
	Commits                          []Commit // oldest to newest
}

type RateLimit struct {
	Remaining, Cost int
	ResetAt         time.Time
}
