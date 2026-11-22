package config

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"time"
)

var (
	efforts  = []string{"low", "medium", "high", "xhigh", "max"}
	spinners = []string{"dot", "line", "minidot", "pulse", "points", "meter", "none"}
	// statusKeys are the nine statuses a theme may restyle, and DRAFTED,
	// the pill a card shows for a finished review not yet posted.
	statusKeys = []string{
		"CI_WAITING", "CI_FAILED", "READY_FOR_REVIEW", "NEW_CHANGES",
		"CHANGES_REQUESTED", "APPROVED", "APPROVED_BY_YOU", "MERGED", "CLOSED",
		"DRAFTED",
	}
	hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

// Validate checks every setting and returns all problems joined together.
// Each message names the TOML key path so the user can find the line.
func (c Config) Validate() error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if c.Mode.Default != "watcher" && c.Mode.Default != "auto" {
		bad("mode.default %q: must be \"watcher\" or \"auto\"", c.Mode.Default)
	}

	if c.GitHub.PollInterval.Duration < time.Minute {
		bad("github.poll_interval %s: must be at least 1m", c.GitHub.PollInterval)
	}

	validateFeeds(c.Feeds, bad)
	for _, name := range slices.Sorted(maps.Keys(c.Repos)) {
		if s := c.Repos[name].Scope; s != "full" && s != "pr-only" {
			bad("repos.%q.scope %q: must be \"full\" or \"pr-only\"", name, s)
		}
	}
	validateReview(c.Review, bad)
	validateTheme(c.Theme, bad)

	return errors.Join(errs...)
}

// reportFunc records one validation problem, formatted as fmt.Errorf does.
type reportFunc func(format string, args ...any)

// validateFeeds checks each feed has a unique name, a query and a window.
func validateFeeds(feeds []Feed, bad reportFunc) {
	seen := map[string]bool{}
	for i, f := range feeds {
		switch {
		case f.Name == "":
			bad("feeds[%d].name: must not be empty", i)
		case seen[f.Name]:
			bad("feeds[%d].name %q: duplicate feed name", i, f.Name)
		}
		seen[f.Name] = true
		if f.Query == "" {
			bad("feeds[%d].query: must not be empty", i)
		}
		if f.Since.Duration <= 0 {
			bad("feeds[%d].since: must be greater than 0", i)
		}
	}
}

// validateReview checks the review limits, budgets, tiers and overrides.
func validateReview(r ReviewConfig, bad reportFunc) {
	if r.MaxInFlight < 1 {
		bad("review.max_in_flight %d: must be at least 1", r.MaxInFlight)
	}
	if r.Budget.PerReviewUSD <= 0 {
		bad("review.budget.per_review_usd %v: must be greater than 0", r.Budget.PerReviewUSD)
	}
	if r.Budget.DailyUSD <= 0 {
		bad("review.budget.daily_usd %v: must be greater than 0", r.Budget.DailyUSD)
	}
	checkEffort("review.first_pass.effort", r.FirstPass.Effort, bad)
	checkEffort("review.follow_up.effort", r.FollowUp.Effort, bad)
	for i, o := range r.Overrides {
		if !slices.Contains([]string{"first_pass", "follow_up", "any"}, o.Tier) {
			bad("review.overrides[%d].tier %q: must be \"first_pass\", \"follow_up\" or \"any\"", i, o.Tier)
		}
		// An override with no effort keeps its tier's.
		if o.Effort != "" {
			checkEffort(fmt.Sprintf("review.overrides[%d].effort", i), o.Effort, bad)
		}
		if o.PerReviewUSD != nil && *o.PerReviewUSD <= 0 {
			bad("review.overrides[%d].per_review_usd %v: must be greater than 0", i, *o.PerReviewUSD)
		}
	}
}

// checkEffort reports effort, set at key, unless it is one of efforts.
func checkEffort(key, effort string, bad reportFunc) {
	if !slices.Contains(efforts, effort) {
		bad("%s %q: must be one of %v", key, effort, efforts)
	}
}

// validateTheme checks the markdown style, each status style and the chrome
// colors. Status keys are checked in sorted order so the output is stable.
func validateTheme(t ThemeConfig, bad reportFunc) {
	if m := t.Markdown; m != "" && m != "dark" && m != "light" {
		bad("theme.markdown must be \"dark\" or \"light\", got %q", m)
	}
	for _, k := range slices.Sorted(maps.Keys(t.Status)) {
		s := t.Status[k]
		if !slices.Contains(statusKeys, k) {
			bad("theme.status.%s: unknown status; must be one of %v", k, statusKeys)
			continue
		}
		if err := checkColor(s.Color); err != nil {
			bad("theme.status.%s.color %q: %v", k, s.Color, err)
		}
		if s.Spinner != "" && !slices.Contains(spinners, s.Spinner) {
			bad("theme.status.%s.spinner %q: must be one of %v", k, s.Spinner, spinners)
		}
	}
	if err := checkColor(t.Chrome.Accent); err != nil {
		bad("theme.chrome.accent %q: %v", t.Chrome.Accent, err)
	}
	if err := checkColor(t.Chrome.Muted); err != nil {
		bad("theme.chrome.muted %q: %v", t.Chrome.Muted, err)
	}
}

// checkColor accepts "" (use the default), a decimal index 16–255, or #rrggbb.
func checkColor(s string) error {
	if s == "" || hexColor.MatchString(s) {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return errors.New("must be a number 16–255 or #rrggbb")
	}
	if n >= 0 && n <= 15 {
		return errors.New("indexes 0–15 are remapped by terminal themes; use 16–255")
	}
	if n < 16 || n > 255 {
		return errors.New("index must be 16–255")
	}
	return nil
}
