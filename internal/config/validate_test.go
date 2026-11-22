package config

import (
	"strings"
	"testing"
	"time"
)

func TestValidateDefaultsOK(t *testing.T) {
	if err := Defaults().Validate(); err != nil {
		t.Fatalf("Defaults().Validate() = %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"poll too short", func(c *Config) { c.GitHub.PollInterval = Duration{30 * time.Second} }, "github.poll_interval"},
		{"bad mode", func(c *Config) { c.Mode.Default = "manual" }, "mode.default"},
		{"duplicate feed name", func(c *Config) {
			c.Feeds = append(c.Feeds, Feed{Name: c.Feeds[0].Name, Query: "q", Since: Duration{time.Hour}})
		}, "feeds[1].name"},
		{"empty feed query", func(c *Config) { c.Feeds[0].Query = "" }, "feeds[0].query"},
		{"zero since", func(c *Config) { c.Feeds[0].Since = Duration{} }, "feeds[0].since"},
		{"bad scope", func(c *Config) { c.Repos = map[string]Repo{"a/b": {Scope: "partial"}} }, `repos."a/b".scope`},
		{"bad effort", func(c *Config) { c.Review.FirstPass.Effort = "huge" }, "review.first_pass.effort"},
		{"bad override tier", func(c *Config) { c.Review.Overrides = []Override{{Tier: "second"}} }, "review.overrides[0].tier"},
		{"bad override effort", func(c *Config) { c.Review.Overrides = []Override{{Tier: "any", Effort: "huge"}} }, "review.overrides[0].effort"},
		{"bad override budget", func(c *Config) {
			z := 0.0
			c.Review.Overrides = []Override{{Tier: "any", PerReviewUSD: &z}}
		}, "review.overrides[0].per_review_usd"},
		{"color 0-15", func(c *Config) {
			c.Theme.Status = map[string]StatusStyle{"CI_WAITING": {Color: "9"}}
		}, "0–15"},
		{"color 256", func(c *Config) {
			c.Theme.Status = map[string]StatusStyle{"CI_WAITING": {Color: "256"}}
		}, "theme.status.CI_WAITING.color"},
		{"bad hex", func(c *Config) { c.Theme.Chrome.Accent = "#12345" }, "theme.chrome.accent"},
		{"unknown status", func(c *Config) { c.Theme.Status = map[string]StatusStyle{"BOGUS": {}} }, "theme.status.BOGUS"},
		{"bad spinner", func(c *Config) {
			c.Theme.Status = map[string]StatusStyle{"CI_WAITING": {Spinner: "spin"}}
		}, "theme.status.CI_WAITING.spinner"},
		{"zero budget", func(c *Config) { c.Review.Budget.DailyUSD = 0 }, "review.budget.daily_usd"},
		{"zero in flight", func(c *Config) { c.Review.MaxInFlight = 0 }, "review.max_in_flight"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Defaults()
			tt.mutate(&c)
			err := c.Validate()
			if err == nil {
				t.Fatal("Validate() = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestValidateAcceptsColors(t *testing.T) {
	c := Defaults()
	c.Theme.Status = map[string]StatusStyle{
		"CI_WAITING": {Color: ""},
		"CI_FAILED":  {Color: "16", Spinner: "none"},
		"MERGED":     {Color: "255"},
		"CLOSED":     {Color: "#a1B2c3"},
	}
	c.Theme.Chrome = Chrome{Accent: "214", Muted: "#000000"}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

// DRAFTED is the card pill for a finished, unposted review; a theme styles
// it like a status.
func TestValidateAcceptsDrafted(t *testing.T) {
	c := Defaults()
	c.Theme.Status = map[string]StatusStyle{"DRAFTED": {Label: "Unposted", Color: "200", Spinner: "none"}}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	c.Theme.Status = map[string]StatusStyle{"DRAFTED": {Color: "3"}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "theme.status.DRAFTED.color") {
		t.Errorf("DRAFTED color 3: Validate() = %v", err)
	}
	c.Theme.Status = map[string]StatusStyle{"DRAFT": {}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "theme.status.DRAFT: unknown status") {
		t.Errorf("DRAFT: Validate() = %v, want an unknown status", err)
	}
}

func TestValidateReportsAllProblems(t *testing.T) {
	c := Defaults()
	c.GitHub.PollInterval = Duration{time.Second}
	c.Review.FirstPass.Effort = "huge"
	err := c.Validate()
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"github.poll_interval", "review.first_pass.effort"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestValidateMarkdownStyle(t *testing.T) {
	for _, ok := range []string{"", "dark", "light"} {
		c := Defaults()
		c.Theme.Markdown = ok
		if err := c.Validate(); err != nil {
			t.Errorf("markdown %q: Validate() = %v", ok, err)
		}
	}
	c := Defaults()
	c.Theme.Markdown = "solarized"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), `theme.markdown must be "dark" or "light"`) {
		t.Errorf("markdown solarized: Validate() = %v", err)
	}
}
