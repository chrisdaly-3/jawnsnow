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

func TestEvalCIRequiredOnly(t *testing.T) {
	checks := []github.Check{
		{Name: "lint", State: github.CheckFailure},
		{Name: "build", State: github.CheckSuccess, Required: true},
	}
	if got := EvalCI(checks, nil); got != CIGreen {
		t.Errorf("got %q, want green", got)
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
