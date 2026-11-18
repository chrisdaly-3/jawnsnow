package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/lipgloss/v2"

	"github.com/chrisdaly-3/jawnsnow/internal/review"
	"github.com/chrisdaly-3/jawnsnow/internal/status"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/theme"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
)

// reviewSpinner animates the running-review badge whatever the PR's status
// spinner is.
var reviewSpinner = spinner.Dot

// displayPRs is the PRs as the wall draws them: a PR whose review is
// running spins. It is decided here, at render, because every poll derives
// the stored statuses again.
func (m Model) displayPRs() []tracker.Tracked {
	prs := m.wallPRs()
	for i, t := range prs {
		if m.reviews.state[t.PR.ID].running {
			prs[i].Status.Spinning = true
		}
	}
	return prs
}

// reviewSpinInterval is the running-review badge's frame period, or 0 when
// no review runs.
func (m Model) reviewSpinInterval() time.Duration {
	if m.runningCount() == 0 {
		return 0
	}
	return reviewSpinner.FPS
}

// reviewBadges is each wall PR's Claude badge.
func (m Model) reviewBadges(frame int) map[string]string {
	out := map[string]string{}
	for id, t := range m.prs {
		if t.Place != tracker.PlaceWall {
			continue
		}
		if b := m.badge(t, frame); b != "" {
			out[id] = b
		}
	}
	return out
}

func (m Model) badge(t tracker.Tracked, frame int) string {
	th := m.theme
	s := m.reviews.state[t.PR.ID]
	switch {
	case s.running:
		f := reviewSpinner.Frames[frame%len(reviewSpinner.Frames)]
		return th.Accent().Render(strings.TrimSpace(f) + " Claude reviewing…")
	case s.skip == "teammate" && !reviewedAt(s.latest, t.PR.HeadOID):
		return th.Muted().Render("· teammate reviewed")
	}
	return latestBadge(th, s)
}

// latestBadge is the badge for the PR's latest finished review.
func latestBadge(th theme.Theme, s reviewState) string {
	if s.cancelled && s.latest != nil {
		return th.Muted().Render("· review cancelled")
	}
	return verdictBadge(th, s.latest)
}

// reviewedAt reports whether r is a review at head that didn't fail.
func reviewedAt(r *store.Review, head string) bool {
	return r != nil && r.HeadOID == head && r.Outcome != "failed"
}

// verdictBadge is the badge for a finished review, or "" for none.
func verdictBadge(th theme.Theme, r *store.Review) string {
	switch {
	case r == nil:
		return ""
	case r.Outcome == "failed":
		return th.StatusFg(string(status.CIFailed)).Render("! review failed")
	}
	if n := blockingCount(r.Findings); n > 0 {
		return th.StatusFg(string(status.CIFailed)).Render("✦ Claude: " + strconv.Itoa(n) + " blocking")
	}
	return th.StatusFg(string(status.Approved)).Render("✦ Claude: clean")
}

// blockingCount counts the blocking findings still open.
func blockingCount(fs []store.Finding) int {
	n := 0
	for _, f := range fs {
		if f.Blocking && f.Ledger != "addressed" && f.Ledger != "answered" {
			n++
		}
	}
	return n
}

// reviewVerdicts is the latest verdict of each waiting PR, for the Done view.
func (m Model) reviewVerdicts() map[string]string {
	out := map[string]string{}
	for id, s := range m.reviews.state {
		if t, ok := m.prs[id]; ok && t.Place == tracker.PlaceWaiting {
			if b := latestBadge(m.theme, s); b != "" {
				out[id] = b
			}
		}
	}
	return out
}

// tickerText summarizes a tool call: "reading pkg/foo.go".
func tickerText(ev review.Event) string {
	verb := map[string]string{"Read": "reading", "Grep": "searching", "Glob": "listing"}[ev.Tool]
	if verb == "" {
		verb = strings.ToLower(ev.Tool)
	}
	return clean(strings.TrimSpace(verb + " " + ev.Path))
}

// spendLine is the status bar's spend, and a note when auto mode paused
// for the budget.
func (m Model) spendLine() string {
	th := m.theme
	s := th.Muted().Render(fmt.Sprintf("$%.2f / $%s today", m.reviews.spent, money(m.cfg.Review.Budget.DailyUSD)))
	if m.mode == modeAuto && m.reviews.budgetPaused {
		s += th.Muted().Render(" · ") + th.Accent().Render("auto-review paused: daily budget")
	}
	return s
}

// reviewPrompt is the open question, which the footer shows in place of
// the key hints.
func (m Model) reviewPrompt() string {
	if m.reviews.confirm == nil {
		return ""
	}
	return m.theme.Accent().Render(m.reviews.confirm.prompt)
}

// pickerBody draws the c picker over the view.
func (m Model) pickerBody(width, height int) string {
	p, th := m.reviews.picker, m.theme
	items, idx, title := pickModels, p.modelIdx, "Review with model"
	if p.choosingEffort {
		items, idx, title = pickEfforts, p.effortIdx, "Effort for "+pickModels[p.modelIdx]
	}
	var b strings.Builder
	b.WriteString(th.Accent().Render(title))
	for i, it := range items {
		mark := "  "
		if i == idx {
			mark = th.Accent().Render("▸ ")
		}
		b.WriteString("\n" + mark + it)
	}
	b.WriteString("\n" + th.Muted().Render("j/k move · enter pick · esc cancel"))
	box := th.Border(true).Padding(0, 2).Render(b.String())
	return lipgloss.Place(max(width, 0), max(height, 0), lipgloss.Center, lipgloss.Center, box)
}
