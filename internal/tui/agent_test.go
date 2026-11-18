package tui

import (
	"strings"
	"testing"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/review"
)

var claudeOK = review.ClaudeInfo{Found: true, Path: "/usr/local/bin/claude", Version: "2.1.327", LoggedIn: true, AuthMethod: "claude.ai"}

func TestAgentShowsResolvedTiers(t *testing.T) {
	h := newHarness(t, opts{reviews: true, claude: claudeOK, config: func(c *config.Config) {
		c.Review.Overrides = []config.Override{
			{Tier: "any", Match: config.Match{Repo: "nomatch/*"}, Model: "claude-fable-5-2", Effort: "max"},
			{Tier: "any", Match: config.Match{Repo: "acme/*"}, Model: "claude-haiku-5-5", Effort: "low"},
		}
	}}, wallPR("1"))
	h.finishPoll(t)
	h.press(t, "5")
	c := content(h.m)
	for _, want := range []string{
		"/usr/local/bin/claude", "2.1.327", "logged in · claude.ai",
		"first_pass", "claude-opus-5-5", "high",
		"follow_up", "claude-sonnet-5-5", "medium",
		"override 2", "$0 of $250",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("Agent view missing %q:\n%s", want, c)
		}
	}
}

func TestAgentShowsClaudeProblems(t *testing.T) {
	h := newHarness(t, opts{reviews: true, claude: review.ClaudeInfo{Found: true, Version: "2.1.327", Path: "/x/claude"}})
	h.press(t, "5")
	if c := content(h.m); !strings.Contains(c, `not logged in`) {
		t.Errorf("Agent view does not say claude is logged out:\n%s", c)
	}
}

func TestAgentSessionPickApplies(t *testing.T) {
	h := newHarness(t, opts{reviews: true, claude: claudeOK}, wallPR("1"))
	h.finishPoll(t)
	// follow_up row, then claude-haiku-5-5, then effort low.
	h.press(t, "5", "j", "enter", "j", "j", "enter", "enter")
	want := &config.Tier{Model: "claude-haiku-5-5", Effort: "low"}
	if got := h.m.session.FollowUp; got == nil || *got != *want {
		t.Fatalf("session follow_up = %v, want %v", got, want)
	}
	if h.m.session.FirstPass != nil {
		t.Errorf("session first_pass = %v, want unset", h.m.session.FirstPass)
	}
	if c := content(h.m); !strings.Contains(c, "claude-haiku-5-5") || !strings.Contains(c, "session") {
		t.Errorf("Agent view does not mark the session pick:\n%s", c)
	}
	if len(h.runs) != 0 {
		t.Fatal("a session pick started a review")
	}
	h.press(t, "1")
	h.m, _ = settle(t, h.m, spaceKey())
	h.runReviews(t)
	reqs := h.reviewer.requested()
	if len(reqs) != 1 {
		t.Fatalf("space started %d reviews, want 1", len(reqs))
	}
	if got := reqs[0].Session.FollowUp; got == nil || *got != *want {
		t.Errorf("Request.Session.FollowUp = %v, want %v", got, want)
	}

	h.press(t, "5", "j", "x")
	if h.m.session.FollowUp != nil {
		t.Errorf("x left the session pick: %v", h.m.session.FollowUp)
	}
}

func TestNoClaudeDisablesReviews(t *testing.T) {
	for _, tc := range []struct {
		name string
		info review.ClaudeInfo
		want string
	}{
		{"missing", review.ClaudeInfo{Err: "exec: not found"}, "claude not found on PATH"},
		{"logged out", review.ClaudeInfo{Found: true, Path: "/x/claude", Version: "2.1.327"}, `claude is not logged in — run "claude auth login"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, opts{reviews: true, claude: tc.info}, wallPR("1"))
			h.finishPoll(t)
			h.press(t, "m")
			if h.m.mode != modeWatcher {
				t.Fatalf("m entered %q mode with %s", h.m.mode, tc.name)
			}
			if !strings.Contains(content(h.m), tc.want) {
				t.Errorf("m did not say %q:\n%s", tc.want, content(h.m))
			}
			h.m.notice = ""
			h.m, _ = settle(t, h.m, spaceKey())
			if len(h.runs) != 0 {
				t.Fatal("space started a review with no usable claude")
			}
			if !strings.Contains(content(h.m), tc.want) {
				t.Errorf("space did not say %q:\n%s", tc.want, content(h.m))
			}
		})
	}
}

func TestNoClaudeStartsInWatcherMode(t *testing.T) {
	h := newHarness(t, opts{reviews: true, auto: true, claude: review.ClaudeInfo{Err: "gone"}}, wallPR("1"))
	if h.m.mode != modeWatcher {
		t.Errorf("mode = %q, want watcher with no claude", h.m.mode)
	}
}

func TestAgentEditOpensEditor(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "my-editor")
	h := newHarness(t, opts{reviews: true, claude: claudeOK})
	h.press(t, "5")
	_, cmd := update(t, h.m, key("e"))
	if cmd == nil {
		t.Fatal("e returned no command")
	}
	c := editorCommand()
	if c.Args[0] != "my-editor" || c.Args[len(c.Args)-1] != config.Path() {
		t.Errorf("editor command = %v, want my-editor %s", c.Args, config.Path())
	}
	t.Setenv("VISUAL", "my-visual")
	if got := editorCommand().Args[0]; got != "my-visual" {
		t.Errorf("with VISUAL set the editor is %q, want my-visual", got)
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	if got := editorCommand().Args[0]; got != "vi" {
		t.Errorf("with neither set the editor is %q, want vi", got)
	}
}
