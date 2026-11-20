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

// CheckGroup says how EvalCI counts a check.
type CheckGroup int

const (
	GroupFailing CheckGroup = iota // counts, and failed
	GroupHeld                      // ACTION_REQUIRED: waiting for a person to act
	GroupRunning                   // counts, and pending
	GroupIgnored                   // matched an ignore_checks glob
	GroupPassed                    // success, neutral or skipped
)

// CheckRow is one check and the group it falls in.
type CheckRow struct {
	Check   github.Check
	Group   CheckGroup
	Pattern string // the ignore glob it matched, for GroupIgnored
}

// Classify puts every check in the group that says how EvalCI counts it, in
// the order GitHub listed them. Checks whose name matches an ignore glob are
// ignored, even when they're also held. Checks held for a person to act
// (ACTION_REQUIRED) don't count: on mono that's a job waiting for a reviewer
// to approve the head, so it mustn't keep the PR from the reviewer. Every
// other check counts, required by branch protection or not, because an
// optional check that fails still needs fixing before the PR is worth a
// review.
func Classify(checks []github.Check, ignore []string) []CheckRow {
	rows := make([]CheckRow, 0, len(checks))
	for _, c := range checks {
		row := CheckRow{Check: c}
		switch g, ok := ignoredBy(c.Name, ignore); {
		case ok:
			row.Group, row.Pattern = GroupIgnored, g
		case c.State == github.CheckActionRequired:
			row.Group = GroupHeld
		case c.State == github.CheckFailure:
			row.Group = GroupFailing
		case c.State == github.CheckPending:
			row.Group = GroupRunning
		default:
			row.Group = GroupPassed
		}
		rows = append(rows, row)
	}
	return rows
}

// EvalCI rolls checks up into one state, from Classify: any failing check
// makes CI failed, else any running one makes it pending, else it's green. A
// PR with no checks, or only ones that don't count, is green.
func EvalCI(checks []github.Check, ignore []string) CI {
	return rollup(Classify(checks, ignore))
}

func rollup(rows []CheckRow) CI {
	pending := false
	for _, r := range rows {
		switch r.Group {
		case GroupFailing:
			return CIFailedState
		case GroupRunning:
			pending = true
		}
	}
	if pending {
		return CIPending
	}
	return CIGreen
}

// ignoredBy returns the first glob that matches name.
func ignoredBy(name string, globs []string) (string, bool) {
	flat := flatten(name)
	for _, g := range globs {
		if ok, err := path.Match(flatten(g), flat); err == nil && ok {
			return g, true
		}
	}
	return "", false
}

func flatten(s string) string { return strings.ReplaceAll(s, "/", "\x00") }
