package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
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
	if c.GitHub.IdlePollInterval.Duration < c.GitHub.PollInterval.Duration {
		bad("github.idle_poll_interval %s: must be at least github.poll_interval (%s)",
			c.GitHub.IdlePollInterval, c.GitHub.PollInterval)
	}

	seen := map[string]bool{}
	for i, f := range c.Feeds {
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

	repos := make([]string, 0, len(c.Repos))
	for name := range c.Repos {
		repos = append(repos, name)
	}
	sort.Strings(repos)
	for _, name := range repos {
		if s := c.Repos[name].Scope; s != "full" && s != "pr-only" {
			bad("repos.%q.scope %q: must be \"full\" or \"pr-only\"", name, s)
		}
	}

	r := c.Review
	if r.MaxInFlight < 1 {
		bad("review.max_in_flight %d: must be at least 1", r.MaxInFlight)
	}
	if r.Budget.PerReviewUSD <= 0 {
		bad("review.budget.per_review_usd %v: must be greater than 0", r.Budget.PerReviewUSD)
	}
	if r.Budget.DailyUSD <= 0 {
		bad("review.budget.daily_usd %v: must be greater than 0", r.Budget.DailyUSD)
	}
	if !slices.Contains(efforts, r.FirstPass.Effort) {
		bad("review.first_pass.effort %q: must be one of %v", r.FirstPass.Effort, efforts)
	}
	if !slices.Contains(efforts, r.FollowUp.Effort) {
		bad("review.follow_up.effort %q: must be one of %v", r.FollowUp.Effort, efforts)
	}
	for i, o := range r.Overrides {
		if !slices.Contains([]string{"first_pass", "follow_up", "any"}, o.Tier) {
			bad("review.overrides[%d].tier %q: must be \"first_pass\", \"follow_up\" or \"any\"", i, o.Tier)
		}
		if o.Effort != "" && !slices.Contains(efforts, o.Effort) {
			bad("review.overrides[%d].effort %q: must be one of %v", i, o.Effort, efforts)
		}
		if o.PerReviewUSD != nil && *o.PerReviewUSD <= 0 {
			bad("review.overrides[%d].per_review_usd %v: must be greater than 0", i, *o.PerReviewUSD)
		}
	}

	if m := c.Theme.Markdown; m != "" && m != "dark" && m != "light" {
		bad("theme.markdown must be \"dark\" or \"light\", got %q", m)
	}

	keys := make([]string, 0, len(c.Theme.Status))
	for k := range c.Theme.Status {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := c.Theme.Status[k]
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
	if err := checkColor(c.Theme.Chrome.Accent); err != nil {
		bad("theme.chrome.accent %q: %v", c.Theme.Chrome.Accent, err)
	}
	if err := checkColor(c.Theme.Chrome.Muted); err != nil {
		bad("theme.chrome.muted %q: %v", c.Theme.Chrome.Muted, err)
	}

	return errors.Join(errs...)
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
