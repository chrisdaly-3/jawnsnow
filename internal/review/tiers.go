// Package review decides how and when Claude reviews a PR.
package review

import (
	"path"
	"slices"
	"strings"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
)

// Tier is the kind of review pass a model and effort are chosen for.
type Tier string

const (
	TierFirstPass Tier = "first_pass"
	TierFollowUp  Tier = "follow_up"
)

// PassKind says how much of the PR a run reads.
type PassKind string

const (
	PassFull        PassKind = "full"
	PassIncremental PassKind = "incremental"
)

// Mode says whether a run was started by snow or by hand.
type Mode string

const (
	ModeAuto   Mode = "auto"
	ModeManual Mode = "manual"
)

// SessionTiers holds the Agent view's picks, which last for this session only.
type SessionTiers struct{ FirstPass, FollowUp *config.Tier }

// ResolveInput describes the PR and the picks that apply to one run.
type ResolveInput struct {
	Repo, Author  string
	Labels, Files []string
	Lines         int // additions + deletions
	HasYourReview bool
	Session       SessionTiers
	OneRun        *config.Tier // the `c` picker; nil otherwise
}

// Choice is the model, effort and budget a run will use.
type Choice struct {
	Tier         Tier
	Model        string
	Effort       string
	PerReviewUSD float64
	Override     int // index of the matching rule, -1 if none
}

// Resolve picks the model and effort for a run. It starts from the tier's
// config, applies the first matching override, then the session pick, then
// the one-run pick. An empty Model or Effort in any of these keeps the
// earlier value.
func Resolve(cfg config.ReviewConfig, in ResolveInput) Choice {
	c := Choice{Tier: TierFirstPass, Override: -1, PerReviewUSD: cfg.Budget.PerReviewUSD}
	base, session := cfg.FirstPass, in.Session.FirstPass
	if in.HasYourReview {
		c.Tier = TierFollowUp
		base, session = cfg.FollowUp, in.Session.FollowUp
	}
	c.Model, c.Effort = base.Model, base.Effort

	for i, o := range cfg.Overrides {
		if o.Tier != "any" && o.Tier != string(c.Tier) {
			continue
		}
		if !matches(o.Match, in) {
			continue
		}
		c.Override = i
		c.apply(o.Model, o.Effort)
		if o.PerReviewUSD != nil {
			c.PerReviewUSD = *o.PerReviewUSD
		}
		break
	}
	if session != nil {
		c.apply(session.Model, session.Effort)
	}
	if in.OneRun != nil {
		c.apply(in.OneRun.Model, in.OneRun.Effort)
	}
	return c
}

func (c *Choice) apply(model, effort string) {
	if model != "" {
		c.Model = model
	}
	if effort != "" {
		c.Effort = effort
	}
}

func matches(m config.Match, in ResolveInput) bool {
	if m.Repo != "" && !globMatch(m.Repo, in.Repo) {
		return false
	}
	if m.Author != "" && !authorMatch(m.Author, in.Author) {
		return false
	}
	if m.Label != "" && !contains(in.Labels, m.Label) {
		return false
	}
	if len(m.Paths) > 0 && !anyPathMatches(m.Paths, in.Files) {
		return false
	}
	if m.MinLines > 0 && in.Lines < m.MinLines {
		return false
	}
	if m.MaxLines > 0 && in.Lines > m.MaxLines {
		return false
	}
	return true
}

// globMatch is path.Match where a malformed pattern matches nothing.
func globMatch(pattern, name string) bool {
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}

// authorMatch matches a login against a glob. Bot logins end in "[bot]", and
// path.Match reads "[bot]" as a character class, so a pattern such as "*[bot]"
// would otherwise never match one. The pattern fits if it matches as written
// or with its brackets taken literally.
func authorMatch(pattern, login string) bool {
	if globMatch(pattern, login) {
		return true
	}
	literal := strings.NewReplacer("[", `\[`, "]", `\]`).Replace(pattern)
	return globMatch(literal, login)
}

func contains(list []string, s string) bool {
	return slices.Contains(list, s)
}

func anyPathMatches(globs, files []string) bool {
	for _, g := range globs {
		pat := strings.Split(g, "/")
		for _, f := range files {
			if segmentsMatch(pat, strings.Split(f, "/")) {
				return true
			}
		}
	}
	return false
}

// segmentsMatch matches name segments against pattern segments. A "**"
// segment matches zero or more whole segments; any other segment uses
// path.Match.
func segmentsMatch(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			rest := pat[1:]
			for i := 0; i <= len(name); i++ {
				if segmentsMatch(rest, name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 || !globMatch(pat[0], name[0]) {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}
