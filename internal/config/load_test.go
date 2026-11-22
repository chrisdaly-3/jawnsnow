package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func assertEq[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaults(t *testing.T) {
	d := Defaults()
	assertEq(t, d.Mode.Default, "watcher")
	assertEq(t, d.GitHub.PollInterval.Duration, 3*time.Minute)
	assertEq(t, d.GitHub.IdlePollInterval(), 15*time.Minute)
	assertEq(t, len(d.Feeds), 1)
	assertEq(t, d.Feeds[0].Name, "review-requested")
	assertEq(t, d.Feeds[0].Query, "is:pr is:open review-requested:@me")
	assertEq(t, d.Feeds[0].Since.Duration, 14*24*time.Hour)
	assertEq(t, d.Done.KeepFor.Duration, 30*24*time.Hour)
	assertEq(t, d.Review.QuietPeriod.Duration, 5*time.Minute)
	assertEq(t, d.Review.MaxInFlight, 2)
	assertEq(t, d.Review.MaxReviewsPerPRPerDay, 4)
	assertEq(t, d.Review.RunTimeout.Duration, 25*time.Minute)
	assertEq(t, d.Review.Budget.PerReviewUSD, 15.0)
	assertEq(t, d.Review.Budget.DailyUSD, 250.0)
	assertEq(t, d.Review.FirstPass, Tier{"claude-opus-5-5", "high"})
	assertEq(t, d.Review.FollowUp, Tier{"claude-sonnet-5-5", "medium"})
}

func TestLoadOverlaysFileOnDefaults(t *testing.T) {
	cfg, warns, err := Load(writeConfig(t, "[github]\npoll_interval = \"1m\"\n"))
	if err != nil || len(warns) != 0 {
		t.Fatalf("err=%v warns=%v", err, warns)
	}
	assertEq(t, cfg.GitHub.PollInterval.Duration, time.Minute)
	assertEq(t, cfg.GitHub.IdlePollInterval(), 5*time.Minute)
	assertEq(t, len(cfg.Feeds), 1)
}

func TestLoadFeedsReplaceDefaultFeed(t *testing.T) {
	cfg, _, err := Load(writeConfig(t, `
[[feeds]]
name = "team"
query = "is:pr team-review-requested:o/t"
since = "14d"

[[feeds]]
name = "asked-me"
query = "is:pr review-requested:@me"
since = "36h"
`))
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, len(cfg.Feeds), 2)
	assertEq(t, cfg.Feeds[0].Name, "team")
	assertEq(t, cfg.Feeds[1].Name, "asked-me")
	assertEq(t, cfg.Feeds[1].Since.Duration, 36*time.Hour)
}

func TestLoadMissingFile(t *testing.T) {
	cfg, _, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
	if !reflect.DeepEqual(cfg, Defaults()) {
		t.Errorf("cfg = %+v, want Defaults()", cfg)
	}
}

// A config written before the keys below were removed still loads; each
// removed key comes back as a warning.
func TestLoadRemovedKeysWarn(t *testing.T) {
	cfg, warns, err := Load(writeConfig(t, `
[github]
idle_poll_interval = "15m"

[[feeds]]
name = "team"
query = "is:pr"
team = true
include_drafts = true

[review]
max_turns = 50
worktree_idle = "1h"
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	want := []string{
		`unknown key "github.idle_poll_interval"`,
		`unknown key "feeds.team"`,
		`unknown key "feeds.include_drafts"`,
		`unknown key "review.max_turns"`,
		`unknown key "review.worktree_idle"`,
	}
	if !reflect.DeepEqual(warns, want) {
		t.Errorf("warnings = %q, want %q", warns, want)
	}
}

func TestLoadUnknownKeyWarns(t *testing.T) {
	_, warns, err := Load(writeConfig(t, "[github]\npol_interval=\"1m\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{`unknown key "github.pol_interval"`}; !reflect.DeepEqual(warns, want) {
		t.Errorf("warnings = %q, want %q", warns, want)
	}
}

func TestLoadOverridesAndTheme(t *testing.T) {
	cfg, warns, err := Load(writeConfig(t, `
[[review.overrides]]
tier = "any"
match = { author = "*[bot]", min_lines = 5 }
model = "m"
per_review_usd = 0

[[review.overrides]]
tier = "any"

[theme.status.CI_WAITING]
label = "Waiting"
color = "214"

[theme.chrome]
accent = "#ff8800"
`))
	if err != nil || len(warns) != 0 {
		t.Fatalf("err=%v warns=%v", err, warns)
	}
	o := cfg.Review.Overrides
	assertEq(t, len(o), 2)
	assertEq(t, o[0].Match.Author, "*[bot]")
	assertEq(t, o[0].Match.MinLines, 5)
	if o[0].PerReviewUSD == nil || *o[0].PerReviewUSD != 0 {
		t.Errorf("explicit 0 should be set, got %v", o[0].PerReviewUSD)
	}
	if o[1].PerReviewUSD != nil {
		t.Errorf("unset should be nil, got %v", *o[1].PerReviewUSD)
	}
	assertEq(t, cfg.Theme.Status["CI_WAITING"].Color, "214")
	assertEq(t, cfg.Theme.Chrome.Accent, "#ff8800")
}

func TestLoadDraftedTheme(t *testing.T) {
	cfg, warns, err := Load(writeConfig(t, `
[theme.status]
DRAFTED = { label = "Unposted", color = "200", spinner = "none" }
`))
	if err != nil || len(warns) != 0 {
		t.Fatalf("err=%v warns=%v", err, warns)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	assertEq(t, cfg.Theme.Status["DRAFTED"], StatusStyle{Label: "Unposted", Color: "200", Spinner: "none"})
}

func TestIgnoreChecksForCombines(t *testing.T) {
	c := Config{
		GitHub: GitHubConfig{IgnoreChecks: []string{"a"}},
		Repos:  map[string]Repo{"o/r": {IgnoreChecks: []string{"b"}}},
	}
	if got := c.IgnoreChecksFor("o/r"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("o/r = %v", got)
	}
	if got := c.IgnoreChecksFor("x/y"); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("x/y = %v", got)
	}
	assertEq(t, c.RepoFor("x/y").Scope, "full")
}

func TestLoadRepoScopeDefaultsToFull(t *testing.T) {
	p := writeConfig(t, "[repos.\"chainguard-dev/mono\"]\npath = \"~/code/mono\"\n")
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, cfg.Repos["chainguard-dev/mono"].Scope, "full")
	assertEq(t, cfg.RepoFor("chainguard-dev/mono").Scope, "full")
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestLoadFeedSinceDefaultsTo14d(t *testing.T) {
	p := writeConfig(t, "[[feeds]]\nname = \"team\"\nquery = \"is:pr is:open team-review-requested:org/team\"\n\n[[feeds]]\nname = \"mine\"\nquery = \"is:pr author:@me\"\nsince = \"36h\"\n")
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, cfg.Feeds[0].Since.Duration, 14*24*time.Hour)
	assertEq(t, cfg.Feeds[1].Since.Duration, 36*time.Hour)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}
