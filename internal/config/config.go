package config

import "time"

// Config is the parsed contents of config.toml.
type Config struct {
	Mode   ModeConfig      `toml:"mode"`
	GitHub GitHubConfig    `toml:"github"`
	Feeds  []Feed          `toml:"feeds"`
	Done   DoneConfig      `toml:"done"`
	Repos  map[string]Repo `toml:"repos"`
	Review ReviewConfig    `toml:"review"`
	Theme  ThemeConfig     `toml:"theme"`
}

// ModeConfig sets the mode snow starts in: "watcher" or "auto".
type ModeConfig struct {
	Default string `toml:"default"`
}

// GitHubConfig controls polling and which checks are ignored everywhere.
type GitHubConfig struct {
	PollInterval     Duration `toml:"poll_interval"`
	IdlePollInterval Duration `toml:"idle_poll_interval"`
	IgnoreChecks     []string `toml:"ignore_checks"`
}

// Feed is one GitHub search whose results land on the wall.
type Feed struct {
	Name          string   `toml:"name"`
	Query         string   `toml:"query"`
	Since         Duration `toml:"since"`
	Team          bool     `toml:"team"`
	IncludeDrafts bool     `toml:"include_drafts"`
}

// DoneConfig controls how long finished PRs stay tracked.
type DoneConfig struct {
	KeepFor Duration `toml:"keep_for"`
}

// Repo holds per-repository settings, keyed by "owner/name" in Config.Repos.
type Repo struct {
	Path         string   `toml:"path"`
	Scope        string   `toml:"scope"`
	Context      []string `toml:"context"`
	Focus        []string `toml:"focus"`
	DesignRefs   []string `toml:"design_refs"`
	IgnoreChecks []string `toml:"ignore_checks"`
}

// ReviewConfig controls automatic Claude reviews.
type ReviewConfig struct {
	QuietPeriod           Duration   `toml:"quiet_period"`
	MaxInFlight           int        `toml:"max_in_flight"`
	MaxReviewsPerPRPerDay int        `toml:"max_reviews_per_pr_per_day"`
	MaxTurns              int        `toml:"max_turns"`
	RunTimeout            Duration   `toml:"run_timeout"`
	WorktreeIdle          Duration   `toml:"worktree_idle"`
	Budget                Budget     `toml:"budget"`
	FirstPass             Tier       `toml:"first_pass"`
	FollowUp              Tier       `toml:"follow_up"`
	Overrides             []Override `toml:"overrides"`
}

// Budget caps review spend in US dollars.
type Budget struct {
	PerReviewUSD float64 `toml:"per_review_usd"`
	DailyUSD     float64 `toml:"daily_usd"`
}

// Tier names the model and effort used for one kind of review pass.
type Tier struct {
	Model  string `toml:"model"`
	Effort string `toml:"effort"`
}

// Override replaces a tier's settings for PRs that match Match.
// PerReviewUSD is a pointer so that unset differs from 0.
type Override struct {
	Tier         string   `toml:"tier"`
	Match        Match    `toml:"match"`
	Model        string   `toml:"model"`
	Effort       string   `toml:"effort"`
	PerReviewUSD *float64 `toml:"per_review_usd"`
}

// Match selects PRs for an Override; every field that is set must match.
type Match struct {
	Repo     string   `toml:"repo"`
	Author   string   `toml:"author"`
	Label    string   `toml:"label"`
	Paths    []string `toml:"paths"`
	MinLines int      `toml:"min_lines"`
	MaxLines int      `toml:"max_lines"`
}

// ThemeConfig overrides status labels and colors, and picks the markdown
// style of review text.
type ThemeConfig struct {
	Markdown string                 `toml:"markdown"`
	Status   map[string]StatusStyle `toml:"status"`
	Chrome   Chrome                 `toml:"chrome"`
}

// StatusStyle restyles one status, keyed by its name such as "CI_WAITING".
type StatusStyle struct {
	Label   string `toml:"label"`
	Color   string `toml:"color"`
	Spinner string `toml:"spinner"`
}

// Chrome sets the accent and muted colors of the UI frame.
type Chrome struct {
	Accent string `toml:"accent"`
	Muted  string `toml:"muted"`
}

// DefaultSince is a feed's time gate when it doesn't set one.
const DefaultSince = 14 * 24 * time.Hour

// Defaults returns the configuration used when no file is present. It sets
// every scalar default itself rather than relying on decoding onto it.
func Defaults() Config {
	return Config{
		Mode: ModeConfig{Default: "watcher"},
		GitHub: GitHubConfig{
			PollInterval:     Duration{3 * time.Minute},
			IdlePollInterval: Duration{15 * time.Minute},
		},
		Feeds: []Feed{{
			Name:  "review-requested",
			Query: "is:pr is:open review-requested:@me",
			Since: Duration{DefaultSince},
		}},
		Done: DoneConfig{KeepFor: Duration{30 * 24 * time.Hour}},
		Review: ReviewConfig{
			QuietPeriod:           Duration{5 * time.Minute},
			MaxInFlight:           2,
			MaxReviewsPerPRPerDay: 4,
			MaxTurns:              100,
			RunTimeout:            Duration{25 * time.Minute},
			WorktreeIdle:          Duration{2 * time.Hour},
			Budget:                Budget{PerReviewUSD: 15, DailyUSD: 250},
			FirstPass:             Tier{Model: "claude-opus-5-5", Effort: "high"},
			FollowUp:              Tier{Model: "claude-sonnet-5-5", Effort: "medium"},
		},
	}
}

// IgnoreChecksFor returns github.ignore_checks followed by the repo's own
// ignore_checks.
func (c Config) IgnoreChecksFor(repo string) []string {
	out := append([]string(nil), c.GitHub.IgnoreChecks...)
	return append(out, c.Repos[repo].IgnoreChecks...)
}

// RepoFor returns the repo's entry, or Repo{Scope: "full"} when it has none.
func (c Config) RepoFor(repo string) Repo {
	if r, ok := c.Repos[repo]; ok {
		return r
	}
	return Repo{Scope: "full"}
}
