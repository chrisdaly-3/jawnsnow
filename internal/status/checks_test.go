package status

import (
	"testing"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
)

func TestEvalCIIgnoresGlobs(t *testing.T) {
	checks := []github.Check{{Name: "codeowners-gate / review", State: github.CheckPending, Required: true}}
	if got := EvalCI(checks, []string{"codeowners-gate*"}); got != CIGreen {
		t.Errorf("got %q, want green", got)
	}
}

func TestEvalCIOptionalFailureCounts(t *testing.T) {
	checks := []github.Check{
		{Name: "skillup", State: github.CheckFailure},
		{Name: "codeowners-gate", State: github.CheckPending, Required: true},
	}
	if got := EvalCI(checks, nil); got != CIFailedState {
		t.Errorf("got %q, want failed", got)
	}
}

func TestEvalCIOptionalPendingCounts(t *testing.T) {
	checks := []github.Check{
		{Name: "lint", State: github.CheckPending},
		{Name: "build", State: github.CheckSuccess, Required: true},
	}
	if got := EvalCI(checks, nil); got != CIPending {
		t.Errorf("got %q, want pending", got)
	}
}

func TestEvalCIActionRequiredDoesNotCount(t *testing.T) {
	held := github.Check{Name: "presubmit / tf-verify-sentinel-chainops", State: github.CheckActionRequired}
	if got := EvalCI([]github.Check{held, {Name: "build", State: github.CheckSuccess}}, nil); got != CIGreen {
		t.Errorf("held only: got %q, want green", got)
	}
	if got := EvalCI([]github.Check{held, {Name: "skillup", State: github.CheckFailure}}, nil); got != CIFailedState {
		t.Errorf("held and failing: got %q, want failed", got)
	}
}

func TestEvalCIOnlyIgnoredChecksIsGreen(t *testing.T) {
	checks := []github.Check{
		{Name: "codeowners-gate", State: github.CheckPending, Required: true},
		{Name: "codeowners-gate check", State: github.CheckFailure, Required: true},
	}
	if got := EvalCI(checks, []string{"codeowners-gate*"}); got != CIGreen {
		t.Errorf("got %q, want green", got)
	}
}

func TestEvalCIIgnoringTheOnlyRequiredCheckLeavesTheRest(t *testing.T) {
	checks := []github.Check{
		{Name: "codeowners-gate", State: github.CheckPending, Required: true},
		{Name: "skillup", State: github.CheckFailure},
	}
	if got := EvalCI(checks, []string{"codeowners-gate"}); got != CIFailedState {
		t.Errorf("got %q, want failed", got)
	}
}

func TestEvalCINotIgnoredStillBlocks(t *testing.T) {
	checks := []github.Check{{Name: "build", State: github.CheckPending}}
	if got := EvalCI(checks, []string{"codeowners-gate*"}); got != CIPending {
		t.Errorf("got %q, want pending", got)
	}
}

func TestEvalCIFailureBeatsPending(t *testing.T) {
	checks := []github.Check{
		{Name: "a", State: github.CheckPending},
		{Name: "b", State: github.CheckFailure},
	}
	if got := EvalCI(checks, nil); got != CIFailedState {
		t.Errorf("got %q, want failed", got)
	}
}

func TestEvalCINoChecksIsGreen(t *testing.T) {
	if got := EvalCI(nil, nil); got != CIGreen {
		t.Errorf("got %q, want green", got)
	}
}

func TestEvalCINeutralAndSkippedAreGreen(t *testing.T) {
	checks := []github.Check{{Name: "a", State: github.CheckNeutral}, {Name: "b", State: github.CheckSkipped}}
	if got := EvalCI(checks, nil); got != CIGreen {
		t.Errorf("got %q, want green", got)
	}
}

func TestClassify(t *testing.T) {
	checks := []github.Check{
		{Name: "build", State: github.CheckFailure},
		{Name: "sentinel", State: github.CheckActionRequired},
		{Name: "test", State: github.CheckPending, Required: true},
		{Name: "codeowners-gate", State: github.CheckPending, Required: true},
		{Name: "lint", State: github.CheckSuccess},
		{Name: "docs", State: github.CheckNeutral},
		{Name: "e2e", State: github.CheckSkipped},
		{Name: "codeowners-gate check", State: github.CheckActionRequired},
	}
	want := []CheckRow{
		{Check: checks[0], Group: GroupFailing},
		{Check: checks[1], Group: GroupHeld},
		{Check: checks[2], Group: GroupRunning},
		{Check: checks[3], Group: GroupIgnored, Pattern: "codeowners-gate*"},
		{Check: checks[4], Group: GroupPassed},
		{Check: checks[5], Group: GroupPassed},
		{Check: checks[6], Group: GroupPassed},
		// Ignored wins over held, so the drawer can name the glob.
		{Check: checks[7], Group: GroupIgnored, Pattern: "codeowners-gate*"},
	}
	got := Classify(checks, []string{"nothing", "codeowners-gate*"})
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	if rows := Classify(nil, nil); len(rows) != 0 {
		t.Errorf("nil checks: got %d rows, want none", len(rows))
	}
}

// TestEvalCIMatchesClassify checks the lane and the drawer's account of it
// can't disagree: EvalCI is what the classified rows say.
func TestEvalCIMatchesClassify(t *testing.T) {
	ignore := []string{"codeowners-gate*"}
	cases := map[string][]github.Check{
		"none":          nil,
		"only ignored":  {{Name: "codeowners-gate", State: github.CheckPending}},
		"only held":     {{Name: "s", State: github.CheckActionRequired}},
		"failing":       {{Name: "a", State: github.CheckPending}, {Name: "b", State: github.CheckFailure}},
		"running":       {{Name: "a", State: github.CheckPending}, {Name: "b", State: github.CheckSuccess}},
		"passed":        {{Name: "a", State: github.CheckNeutral}, {Name: "b", State: github.CheckSkipped}},
		"ignored fails": {{Name: "codeowners-gate check", State: github.CheckFailure}},
	}
	want := map[string]CI{
		"none": CIGreen, "only ignored": CIGreen, "only held": CIGreen, "failing": CIFailedState,
		"running": CIPending, "passed": CIGreen, "ignored fails": CIGreen,
	}
	for name, checks := range cases {
		rows := Classify(checks, ignore)
		var failing, running bool
		for _, r := range rows {
			failing = failing || r.Group == GroupFailing
			running = running || r.Group == GroupRunning
		}
		fromRows := CIGreen
		switch {
		case failing:
			fromRows = CIFailedState
		case running:
			fromRows = CIPending
		}
		if got := EvalCI(checks, ignore); got != fromRows || got != want[name] {
			t.Errorf("%s: EvalCI = %q, from rows %q, want %q", name, got, fromRows, want[name])
		}
	}
}
