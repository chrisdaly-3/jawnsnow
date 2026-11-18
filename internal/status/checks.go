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
// glob are dropped first. If any remaining check is required, only required
// checks count. A PR with no checks is green.
func EvalCI(checks []github.Check, ignore []string) CI {
	var kept []github.Check
	anyRequired := false
	for _, c := range checks {
		if ignored(c.Name, ignore) {
			continue
		}
		kept = append(kept, c)
		anyRequired = anyRequired || c.Required
	}

	pending := false
	for _, c := range kept {
		if anyRequired && !c.Required {
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

// ignored matches name against the globs with path.Match. Check names such as
// "codeowners-gate / review" contain "/", which path.Match's "*" refuses to
// cross, so "/" is swapped for a plain character on both sides first.
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
