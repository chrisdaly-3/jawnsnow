package tui

import (
	"math"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// maxErrWidth keeps a long poll error from pushing the rest of the top line
// off screen; the full text is rarely more useful than its start.
const maxErrWidth = 60

// topLine is the brand, mode, poll state, spend, GitHub budget, and the view
// tabs pushed to the right edge.
func (m Model) topLine(width int) string {
	th := m.theme
	sep := th.Muted().Render(" · ")
	left := " " + th.Accent().Render("❄ jawnSnow") + "   " +
		th.Accent().Render("◉ "+m.mode) + sep +
		m.pollStatus() + sep +
		m.spendLine() + sep +
		th.Muted().Render(m.ghRemaining())
	tabs := m.tabs()
	gap := width - lipgloss.Width(left) - lipgloss.Width(tabs) - 1
	return ansi.Truncate(left+strings.Repeat(" ", max(gap, 3))+tabs, max(width, 0), "…")
}

func (m Model) pollStatus() string {
	switch {
	case m.lastErr != nil:
		msg := strings.Join(strings.Fields(m.lastErr.Error()), " ")
		return m.theme.Accent().Render(ansi.Truncate("poll failed: "+msg, maxErrWidth, "…"))
	case m.quota.Limited():
		return m.theme.Accent().Render("rate limited")
	case m.client == nil:
		return m.theme.Muted().Render("not polling")
	case m.lastPoll.IsZero():
		return m.theme.Muted().Render("polling…")
	}
	return m.theme.Muted().Render("polled " + ago(m.now().Sub(m.lastPoll)) + " ago")
}

func (m Model) ghRemaining() string {
	r, ok := m.quota.Remaining()
	switch {
	case !ok:
		return "GH –"
	case r >= 1000:
		return "GH " + strings.TrimSuffix(strconv.FormatFloat(float64(r)/1000, 'f', 1, 64), ".0") + "k"
	}
	return "GH " + strconv.Itoa(r)
}

func (m Model) tabs() string {
	th := m.theme
	parts := make([]string, viewCount)
	for v := range viewCount {
		if v == m.view {
			parts[v] = th.Accent().Render(v.String())
		} else {
			parts[v] = th.Muted().Render(v.String())
		}
	}
	return th.Muted().Render("‹ ") + strings.Join(parts, "  ") + th.Muted().Render(" ›")
}

// bannerLines are the notes under the top line: startup banners, config
// trouble, rate-limit pacing, a poll that reached nothing, skipped feeds,
// unreachable PRs and failed saves or opens.
func (m Model) bannerLines(width int) []string {
	texts := append([]string(nil), m.banners...)
	if m.configBanner != "" {
		texts = append(texts, m.configBanner)
	}
	for _, w := range m.warnings {
		texts = append(texts, "config warning: "+w)
	}
	for _, s := range []string{m.rateNote, m.failNote, m.feedNote, unreachableNote(m.unreachable), m.notice} {
		if s != "" {
			texts = append(texts, s)
		}
	}
	lines := make([]string, len(texts))
	for i, t := range texts {
		lines[i] = ansi.Truncate(" "+m.theme.Accent().Render(t), max(width, 0), "…")
	}
	return lines
}

func (m Model) footer(width int) string {
	if p := m.reviewPrompt(); p != "" {
		return ansi.Truncate(" "+p, max(width, 0), "…")
	}
	hints := footerHints(m.view)
	parts := make([]string, len(hints))
	for i, k := range hints {
		parts[i] = k.keys + " " + k.help
	}
	return ansi.Truncate(" "+m.theme.Muted().Render(strings.Join(parts, " · ")), max(width, 0), "…")
}

// ago is a short age: 40s, 3m, 2h.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(max(d, 0)/time.Second)) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	}
	return strconv.Itoa(int(d/time.Hour)) + "h"
}

// money drops the cents from whole dollars: 250 → "250", 12.5 → "12.50".
func money(v float64) string {
	if v == math.Trunc(v) {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}
