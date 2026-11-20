package github

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseDetail(t *testing.T) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(fixture(t, "detail.json"), &raw); err != nil {
		t.Fatal(err)
	}
	snap, err := parsePR(raw["p0"])
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, snap.Repo, "chainguard-dev/mono")
	assertEq(t, snap.Number, 85180)
	assertEq(t, snap.HeadOID, "c3")
	assertEq(t, snap.State, PROpen)
	assertEq(t, snap.Author, "chrisdaly-3")
	assertEq(t, snap.HeadCommittedAt.UTC().Format("15:04"), "09:00")
	assertEq(t, len(snap.Checks), 2)
	assertEq(t, snap.Checks[0], Check{Name: "ci/build", State: CheckPending, Required: true, URL: "https://github.com/chainguard-dev/mono/actions/runs/1/job/2"})
	assertEq(t, snap.Checks[1], Check{Name: "lint", State: CheckSuccess})
	// The viewer's own review arrives only in "mine"; the other "mine" entry
	// repeats alice's and is dropped. Reviews run oldest to newest.
	assertEq(t, len(snap.Reviews), 5)
	assertEq(t, snap.Reviews[0].Author, "me")
	// A review by a GitHub App is marked; a person's isn't.
	assertEq(t, snap.Reviews[2].Author, "renovate")
	assertEq(t, snap.Reviews[2].Bot, true)
	assertEq(t, snap.Reviews[3].CommitOID, "c2")
	assertEq(t, snap.Reviews[3].Author, "bob")
	assertEq(t, snap.Reviews[3].Bot, false)
	// A deleted user and a missing commit parse to empty strings.
	assertEq(t, snap.Reviews[4].Author, "")
	assertEq(t, snap.Reviews[4].CommitOID, "")
	assertEq(t, len(snap.Comments), 2)
	assertEq(t, snap.Comments[1].Author, "")
	assertEq(t, len(snap.Commits), 3)
	assertEq(t, snap.Commits[0].OID, "c1")
	assertEq(t, len(snap.Commits[2].Parents), 2)
	assertEq(t, len(snap.Labels), 2)
	assertEq(t, snap.Labels[0], "ci/run:agent-evals")

	p1, err := parsePR(raw["p1"])
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, p1.State, PRMerged)
	if p1.MergedAt.IsZero() || !p1.ClosedAt.Equal(p1.MergedAt) {
		t.Errorf("MergedAt/ClosedAt not set: %v %v", p1.MergedAt, p1.ClosedAt)
	}
	assertEq(t, len(p1.Checks), 0) // null statusCheckRollup
}

func TestCheckStateMapping(t *testing.T) {
	for _, c := range []struct {
		status, concl string
		want          CheckState
	}{
		{"IN_PROGRESS", "", CheckPending}, {"QUEUED", "", CheckPending},
		{"COMPLETED", "SUCCESS", CheckSuccess}, {"COMPLETED", "NEUTRAL", CheckNeutral},
		{"COMPLETED", "SKIPPED", CheckSkipped}, {"COMPLETED", "FAILURE", CheckFailure},
		{"COMPLETED", "CANCELLED", CheckFailure}, {"COMPLETED", "TIMED_OUT", CheckFailure},
		{"COMPLETED", "ACTION_REQUIRED", CheckActionRequired}, {"COMPLETED", "STARTUP_FAILURE", CheckFailure},
		{"COMPLETED", "STALE", CheckFailure},
	} {
		assertEq(t, checkRunState(c.status, c.concl), c.want)
	}
	for in, want := range map[string]CheckState{
		"PENDING": CheckPending, "EXPECTED": CheckPending, "SUCCESS": CheckSuccess,
		"ERROR": CheckFailure, "FAILURE": CheckFailure,
	} {
		assertEq(t, statusContextState(in), want)
	}
}

func TestParseNewThreadsFromMine(t *testing.T) {
	review := func(comments string) string {
		return `{"author":{"login":"me"},"state":"COMMENTED","body":"","submittedAt":"%s","commit":{"oid":"c1"}` + comments + `}`
	}
	node := func(at, mineComments string) string {
		return fmt.Sprintf(`{"id":"P","reviews":{"nodes":[%s]},"mine":{"nodes":[%s]}}`,
			fmt.Sprintf(review(""), at), fmt.Sprintf(review(mineComments), at))
	}
	inline := `,"comments":{"nodes":[{"replyTo":null},{"replyTo":{"id":"x"}}]}`
	reply := `,"comments":{"nodes":[{"replyTo":{"id":"x"}},{"replyTo":{"id":"y"}}]}`

	snap, err := parsePR(json.RawMessage(node("2026-11-16T09:00:00Z", inline)))
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, len(snap.Reviews), 1)
	assertEq(t, snap.Reviews[0].NewThreads, 1)

	snap, err = parsePR(json.RawMessage(node("2026-11-16T09:00:00Z", reply)))
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, len(snap.Reviews), 1)
	assertEq(t, snap.Reviews[0].NewThreads, 0)
}
