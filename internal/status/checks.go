package status

import (
	"path"
	"strings"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
)

// CI is the rolled-up state of a PR's checks.
type CI string

const (
	CIPending     CI = "pending"
	CIFailedState CI = "failed" // CIFailed is already taken by the Status constant
	CIGreen       CI = "green"
)

// EvalCI rolls checks up into one state. Checks whose name matches an ignore
// glob are dropped, and so are checks held for a person to act
// (ACTION_REQUIRED): on mono that's a job waiting for a reviewer to approve
// the head, so it mustn't keep the PR from the reviewer. Every other check
// counts, required by branch protection or not, because an optional check
// that fails still needs fixing before the PR is worth a review. A PR with
// no checks, or only ignored ones, is green.
func EvalCI(checks []github.Check, ignore []string) CI {
	pending := false
	for _, c := range checks {
		if c.State == github.CheckActionRequired || ignored(c.Name, ignore) {
			continue
		}
		switch c.State {
		case github.CheckFailure:
			return CIFailedState
		case github.CheckPending:
			pending = true
		}
	}
	if pending {
		return CIPending
	}
	return CIGreen
}

func ignored(name string, globs []string) bool {
	name = flatten(name)
	for _, g := range globs {
		if ok, err := path.Match(flatten(g), name); err == nil && ok {
			return true
		}
	}
	return false
}

func flatten(s string) string { return strings.ReplaceAll(s, "/", "\x00") }
