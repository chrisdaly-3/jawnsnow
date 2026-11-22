package review

import (
	"testing"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/status"
)

func TestResolveTier(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults().Review
	tests := []struct {
		name          string
		hasReview     bool
		tier          Tier
		model, effort string
	}{
		{"first pass", false, "first_pass", "claude-opus-5-5", "high"},
		{"follow up", true, "follow_up", "claude-sonnet-5-5", "medium"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(cfg, ResolveInput{HasYourReview: tt.hasReview})
			want := Choice{Tier: tt.tier, Model: tt.model, Effort: tt.effort, PerReviewUSD: 15, Override: -1}
			if got != want {
				t.Fatalf("Resolve = %+v, want %+v", got, want)
			}
		})
	}
}

func TestResolveFirstMatchingOverrideWins(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults().Review
	cfg.Overrides = []config.Override{
		{Tier: "any", Match: config.Match{Repo: "acme/*"}, Model: "claude-haiku-5-5", Effort: "low"},
		{Tier: "any", Match: config.Match{Repo: "acme/web"}, Model: "claude-fable-5-2", Effort: "max"},
	}
	got := Resolve(cfg, ResolveInput{Repo: "acme/web"})
	if got.Override != 0 || got.Model != "claude-haiku-5-5" || got.Effort != "low" {
		t.Fatalf("Resolve = %+v, want override 0 haiku/low", got)
	}
}

func TestResolveOverrideTierFilter(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults().Review
	cfg.Overrides = []config.Override{
		{Tier: "follow_up", Model: "claude-haiku-5-5"},
		{Tier: "first_pass", Model: "claude-fable-5-2"},
	}
	got := Resolve(cfg, ResolveInput{})
	if got.Override != 1 || got.Model != "claude-fable-5-2" || got.Effort != "high" {
		t.Fatalf("first pass = %+v, want override 1 fable, effort kept", got)
	}
	got = Resolve(cfg, ResolveInput{HasYourReview: true})
	if got.Override != 0 || got.Model != "claude-haiku-5-5" {
		t.Fatalf("follow up = %+v, want override 0 haiku", got)
	}
}

func TestResolveMatchKeys(t *testing.T) {
	t.Parallel()
	base := ResolveInput{
		Repo: "acme/web", Author: "dependabot[bot]", Labels: []string{"bug", "infra"},
		Files: []string{"deploy/a/main.tf", "README.md"}, Lines: 100,
	}
	tests := []struct {
		name  string
		match config.Match
		in    func(ResolveInput) ResolveInput
		fits  bool
	}{
		{"repo glob fits", config.Match{Repo: "acme/*"}, nil, true},
		{"repo glob misses", config.Match{Repo: "other/*"}, nil, false},
		{"author glob fits", config.Match{Author: "*[bot]"}, nil, true},
		{"author glob misses", config.Match{Author: "chris"}, nil, false},
		{"label fits", config.Match{Label: "infra"}, nil, true},
		{"label is exact", config.Match{Label: "inf"}, nil, false},
		{"label misses with none", config.Match{Label: "infra"}, func(r ResolveInput) ResolveInput { r.Labels = nil; return r }, false},
		{"paths ** nested fits", config.Match{Paths: []string{"**/*.tf"}}, nil, true},
		{"paths ** misses", config.Match{Paths: []string{"**/*.go"}}, nil, false},
		{"paths any of several globs", config.Match{Paths: []string{"**/*.go", "*.md"}}, nil, true},
		{"paths dir prefix", config.Match{Paths: []string{"deploy/**"}}, nil, true},
		{"paths ** zero segments", config.Match{Paths: []string{"**/*.tf"}}, func(r ResolveInput) ResolveInput { r.Files = []string{"main.tf"}; return r }, true},
		{"paths ** one segment", config.Match{Paths: []string{"**/*.tf"}}, func(r ResolveInput) ResolveInput { r.Files = []string{"a/main.tf"}; return r }, true},
		{"paths ** middle", config.Match{Paths: []string{"a/**/c.tf"}}, func(r ResolveInput) ResolveInput { r.Files = []string{"a/b/x/c.tf"}; return r }, true},
		{"paths single * stays in segment", config.Match{Paths: []string{"*.tf"}}, func(r ResolveInput) ResolveInput { r.Files = []string{"a/main.tf"}; return r }, false},
		{"paths no files", config.Match{Paths: []string{"**/*.tf"}}, func(r ResolveInput) ResolveInput { r.Files = nil; return r }, false},
		{"min_lines inclusive fits", config.Match{MinLines: 100}, nil, true},
		{"min_lines misses", config.Match{MinLines: 101}, nil, false},
		{"max_lines inclusive fits", config.Match{MaxLines: 100}, nil, true},
		{"max_lines misses", config.Match{MaxLines: 99}, nil, false},
		{"all keys fit", config.Match{Repo: "acme/web", Author: "*[bot]", Label: "bug", Paths: []string{"**/*.tf"}, MinLines: 1, MaxLines: 500}, nil, true},
		{"one key misses", config.Match{Repo: "acme/web", Label: "nope"}, nil, false},
		{"empty match fits", config.Match{}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Defaults().Review
			cfg.Overrides = []config.Override{{Tier: "any", Match: tt.match, Model: "claude-haiku-5-5"}}
			in := base
			if tt.in != nil {
				in = tt.in(in)
			}
			got := Resolve(cfg, in)
			if fits := got.Override == 0; fits != tt.fits {
				t.Fatalf("fits = %v, want %v (choice %+v)", fits, tt.fits, got)
			}
		})
	}
}

func TestResolveOverrideBudgetOnlyWhenSet(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults().Review
	cfg.Overrides = []config.Override{{Tier: "any", Model: "claude-haiku-5-5"}}
	if got := Resolve(cfg, ResolveInput{}); got.PerReviewUSD != 15 {
		t.Fatalf("PerReviewUSD = %v, want 15", got.PerReviewUSD)
	}
	cfg.Overrides = []config.Override{{Tier: "any", PerReviewUSD: new(float64(2))}}
	if got := Resolve(cfg, ResolveInput{}); got.PerReviewUSD != 2 {
		t.Fatalf("PerReviewUSD = %v, want 2", got.PerReviewUSD)
	}
	cfg.Overrides = []config.Override{{Tier: "any", PerReviewUSD: new(float64(0))}}
	if got := Resolve(cfg, ResolveInput{}); got.PerReviewUSD != 0 {
		t.Fatalf("explicit 0 budget = %v, want 0", got.PerReviewUSD)
	}
}

func TestResolveSessionThenOneRun(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults().Review
	in := ResolveInput{
		HasYourReview: true,
		Session: SessionTiers{
			FirstPass: &config.Tier{Model: "claude-fable-5-2", Effort: "low"},
			FollowUp:  &config.Tier{Model: "claude-haiku-5-5"},
		},
	}
	got := Resolve(cfg, in)
	if got.Model != "claude-haiku-5-5" || got.Effort != "medium" {
		t.Fatalf("session pick = %+v, want haiku/medium", got)
	}
	in.HasYourReview = false
	got = Resolve(cfg, in)
	if got.Model != "claude-fable-5-2" || got.Effort != "low" {
		t.Fatalf("first_pass session pick = %+v, want fable/low", got)
	}
	in.HasYourReview = true
	in.OneRun = &config.Tier{Model: "claude-opus-5-5", Effort: "max"}
	got = Resolve(cfg, in)
	if got.Model != "claude-opus-5-5" || got.Effort != "max" {
		t.Fatalf("one-run pick = %+v, want opus/max", got)
	}
	in.OneRun = &config.Tier{Effort: "xhigh"}
	got = Resolve(cfg, in)
	if got.Model != "claude-haiku-5-5" || got.Effort != "xhigh" {
		t.Fatalf("partial one-run = %+v, want haiku/xhigh", got)
	}
}

func TestInlineOnlyCommentResolvesToFollowUp(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults().Review
	r := github.Review{Author: "me", State: "COMMENTED", NewThreads: 1}
	in := ResolveInput{HasYourReview: status.YourLatestReview([]github.Review{r}, "me") != nil}
	got := Resolve(cfg, in)
	if want := Resolve(cfg, ResolveInput{HasYourReview: true}); got != want {
		t.Fatalf("inline-only comment resolved to %+v, want the follow_up tier %+v", got, want)
	}
	if got == Resolve(cfg, ResolveInput{}) {
		t.Fatal("resolved to first_pass")
	}
}
