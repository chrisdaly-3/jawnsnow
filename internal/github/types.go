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

// same reports whether r and o are one review, fetched twice. The query
// doesn't ask for review IDs, so every field must match.
func (r Review) same(o Review) bool {
	return r.Author == o.Author && r.State == o.State && r.Body == o.Body &&
		r.CommitOID == o.CommitOID && r.SubmittedAt.Equal(o.SubmittedAt)
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
	RollupState                      string // the head's combined check state; "" with no checks
	Additions, Deletions             int
	Labels                           []string
	UpdatedAt, MergedAt, ClosedAt    time.Time
	Checks                           []Check
	Reviews                          []Review
	Comments                         []Comment
	Commits                          []Commit // oldest to newest
}

// Fingerprint is the little a cheap query can say about a PR: enough to tell
// whether it changed since its last Snapshot.
type Fingerprint struct {
	ID          string
	UpdatedAt   time.Time
	HeadOID     string
	RollupState string
}

// Same reports whether f and o describe the same PR at the same point.
func (f Fingerprint) Same(o Fingerprint) bool {
	return f.ID == o.ID && f.UpdatedAt.Equal(o.UpdatedAt) && f.HeadOID == o.HeadOID && f.RollupState == o.RollupState
}

// Fingerprint is the snapshot's own fingerprint, to compare with a fresh one.
func (s Snapshot) Fingerprint() Fingerprint {
	return Fingerprint{ID: s.ID, UpdatedAt: s.UpdatedAt, HeadOID: s.HeadOID, RollupState: s.RollupState}
}

// SkippedPR is a requested PR a call left out because GitHub answered it
// with an error, such as FORBIDDEN when an SSO session has expired.
type SkippedPR struct {
	ID, Type, Message string
}

type RateLimit struct {
	Remaining, Cost int
	ResetAt         time.Time
}
