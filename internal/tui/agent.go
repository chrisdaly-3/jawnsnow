package tui

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/review"
	"github.com/chrisdaly-3/jawnsnow/internal/status"
)

// agentTiers are the rows the Agent view's picks apply to, in display order.
var agentTiers = []review.Tier{review.TierFirstPass, review.TierFollowUp}

// agentModel is the Agent view's cursor over agentTiers.
type agentModel struct{ sel int }

// editedMsg ends the editor that e opened on the config file.
type editedMsg struct{ err error }

// editorCommand is $VISUAL, else $EDITOR, else vi, run on the config file.
// There is no shell: a value like "code -w" is split on spaces.
func editorCommand() *exec.Cmd {
	fields := strings.Fields(os.Getenv("VISUAL"))
	if len(fields) == 0 {
		fields = strings.Fields(os.Getenv("EDITOR"))
	}
	if len(fields) == 0 {
		fields = []string{"vi"}
	}
	return exec.Command(fields[0], append(fields[1:], config.Path())...)
}

func (m Model) sessionPick(t review.Tier) *config.Tier {
	if t == review.TierFollowUp {
		return m.session.FollowUp
	}
	return m.session.FirstPass
}

// setSessionPick stores pick (nil clears it) as t's choice for this session.
func (m Model) setSessionPick(t review.Tier, pick *config.Tier) Model {
	if t == review.TierFollowUp {
		m.session.FollowUp = pick
	} else {
		m.session.FirstPass = pick
	}
	return m
}

// agentKey handles a key in the Agent view.
func (m Model) agentKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	tier := agentTiers[m.agent.sel]
	switch msg.String() {
	case "j", "down":
		m.agent.sel = min(m.agent.sel+1, len(agentTiers)-1)
	case "k", "up":
		m.agent.sel = max(m.agent.sel-1, 0)
	case "enter":
		m.reviews.picker = &modelPicker{session: true, tier: tier}
	case "x":
		m = m.setSessionPick(tier, nil)
	case "e":
		return m, tea.ExecProcess(editorCommand(), func(err error) tea.Msg { return editedMsg{err: err} })
	}
	return m, nil
}

// agentBody draws the Agent view.
func (m Model) agentBody(width, height int) string {
	th := m.theme
	head := func(s string) string { return th.Accent().Render(s) }
	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	line("%s", head("claude"))
	line("  %s", m.claudeLine())
	line("")

	line("%s  %s", head("Tiers"), th.Muted().Render("j/k choose · enter pick a model for this session · x clear"))
	cfg := m.cfg.Review
	for i, t := range agentTiers {
		base := cfg.FirstPass
		if t == review.TierFollowUp {
			base = cfg.FollowUp
		}
		mark, row := "  ", fmt.Sprintf("%-10s %s · %s", t, base.Model, base.Effort)
		if i == m.agent.sel {
			mark = th.Accent().Render("▸ ")
		}
		if p := m.sessionPick(t); p != nil {
			row = fmt.Sprintf("%-10s %s · %s  %s", t, cmpOr(p.Model, base.Model), cmpOr(p.Effort, base.Effort),
				th.Accent().Render("(session pick)"))
		}
		line("%s%s", mark, row)
	}
	line("")

	line("%s", head("Selected PR"))
	line("  %s", m.overrideLine())
	line("")

	line("%s", head("Spend"))
	line("  today $%s of $%s", money(m.reviews.spent), money(cfg.Budget.DailyUSD))
	line("")

	running, queued := m.agentQueues()
	line("%s", head(fmt.Sprintf("Running (%d)", len(running))))
	for _, r := range running {
		line("  %s", r)
	}
	line("%s", head(fmt.Sprintf("Queued (%d)", len(queued))))
	for _, q := range queued {
		line("  %s", q)
	}
	line("")
	line("%s", th.Muted().Render("e edit config.toml in "+editorCommand().Args[0]))

	return strings.Join(fit(strings.TrimRight(b.String(), "\n"), width, height), "\n")
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (m Model) claudeLine() string {
	c := m.claude
	if c == (review.ClaudeInfo{}) {
		return "not checked"
	}
	if !c.Found {
		return clean(c.Unusable())
	}
	login := "not logged in"
	if c.LoggedIn {
		login = "logged in"
		if c.AuthMethod != "" {
			login += " · " + clean(c.AuthMethod)
		}
	}
	s := fmt.Sprintf("%s · %s · %s", clean(c.Path), clean(c.Version), login)
	if c.Err != "" {
		s += " · " + clean(c.Err)
	}
	return s
}

// overrideLine says which override rule matches the PR under the Wall's
// cursor, and what model it gets. Path rules can't be checked here, as the
// PR's file list is fetched when a review starts.
func (m Model) overrideLine() string {
	t, ok := m.wall.selected(m.wall.lanes(m.wallPRs()))
	if !ok {
		return "no PR selected on the Wall"
	}
	pr := t.PR
	c := review.Resolve(m.cfg.Review, review.ResolveInput{
		Repo: pr.Repo, Author: pr.Author, Labels: pr.Labels, Lines: pr.Additions + pr.Deletions,
		HasYourReview: status.YourLatestReview(pr.Reviews, m.viewer) != nil,
		Session:       m.session,
	})
	who := clean(fmt.Sprintf("%s#%d", pr.Repo, pr.Number))
	rule := "no override matches"
	if c.Override >= 0 {
		rule = fmt.Sprintf("override %d of %d matches", c.Override+1, len(m.cfg.Review.Overrides))
	}
	s := fmt.Sprintf("%s (%s): %s → %s · %s", who, c.Tier, rule, c.Model, c.Effort)
	if slices.ContainsFunc(m.cfg.Review.Overrides, func(o config.Override) bool { return len(o.Match.Paths) > 0 }) {
		s += " (path rules not checked)"
	}
	return s
}

// agentQueues lists the running and the queued reviews, one line each.
func (m Model) agentQueues() (running, queued []string) {
	name := func(id string) string {
		if t, ok := m.prs[id]; ok {
			return clean(fmt.Sprintf("%s#%d", t.PR.Repo, t.PR.Number))
		}
		return clean(id)
	}
	for _, id := range slices.Sorted(maps.Keys(m.reviews.state)) {
		if s := m.reviews.state[id]; s.running {
			l := name(id)
			if s.ticker != "" {
				l += " · " + clean(s.ticker)
			}
			running = append(running, l)
		}
	}
	for _, q := range m.reviews.queue {
		queued = append(queued, name(q.id))
	}
	return running, queued
}
