package github

import (
	"encoding/json"
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
	assertEq(t, snap.Checks[0], Check{"ci/build", CheckPending, true})
	assertEq(t, snap.Checks[1], Check{"lint", CheckSuccess, false})
	assertEq(t, len(snap.Reviews), 3)
	assertEq(t, snap.Reviews[1].CommitOID, "c2")
	assertEq(t, snap.Reviews[1].Author, "bob")
	// A deleted user and a missing commit parse to empty strings.
	assertEq(t, snap.Reviews[2].Author, "")
	assertEq(t, snap.Reviews[2].CommitOID, "")
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
		{"COMPLETED", "ACTION_REQUIRED", CheckFailure}, {"COMPLETED", "STARTUP_FAILURE", CheckFailure},
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
