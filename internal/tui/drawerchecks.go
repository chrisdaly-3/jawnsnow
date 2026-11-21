package tui

import (
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/status"
	"github.com/chrisdaly-3/jawnsnow/internal/theme"
)

// The drawer's tabs, in the order the tab row shows them.
const (
	tabReview = iota
	tabChecks
	tabActivity
)

var tabNames = []string{"Review", "Checks", "Activity"}

// checkGroups says how the lane line and the Checks tab show each group, in
// the order both list them: most urgent first, as EvalCI weighs them.
var checkGroups = []struct {
	group          status.CheckGroup
	glyph, heading string
	summary        string // after the count on the lane line
	status         string // the status whose colour the glyph takes; "" is muted
}{
	{status.GroupFailing, "✗", "Failing", "failed", "CI_FAILED"},
	{status.GroupHeld, "⏸", "Waiting on you", "waiting on you", "READY_FOR_REVIEW"},
	{status.GroupRunning, "●", "Running", "running", "CI_WAITING"},
	{status.GroupIgnored, "◌", "Ignored", "ignored", ""},
	{status.GroupPassed, "✓", "Passed", "passed", "APPROVED"},
}

func groupGlyph(th theme.Theme, g status.CheckGroup) string {
	for _, cg := range checkGroups {
		if cg.group == g {
			if cg.status == "" {
				return th.Muted().Render(cg.glyph)
			}
			return th.StatusFg(cg.status).Render(cg.glyph)
		}
	}
	return " "
}

// checkRows classifies the PR's checks with the current config's ignore
// globs, so an ignore_checks reload shows at once.
func checkRows(c drawerCtx) []status.CheckRow {
	return status.Classify(c.PR.PR.Checks, c.IgnoreChecks)
}

// laneLines rolls the checks up by group, the way they counted towards the
// PR's lane, with the passed checks always on a line of their own:
//
//	✗ 1 failed · ◌ 1 ignored
//	✓ 44 passed
//
// When the other groups don't fit on one line, they go two to a line in
// aligned columns, so no line breaks at an odd place; when two don't fit,
// one to a line.
func laneLines(rows []status.CheckRow, th theme.Theme, inner int) []string {
	if len(rows) == 0 {
		return []string{th.Muted().Render("No checks")}
	}
	var parts []string
	passed := ""
	for _, cg := range checkGroups {
		n := 0
		for _, r := range rows {
			if r.Group == cg.group {
				n++
			}
		}
		if n == 0 {
			continue
		}
		part := groupGlyph(th, cg.group) + " " + strconv.Itoa(n) + " " + cg.summary
		if cg.group == status.GroupPassed {
			passed = ansi.Truncate(part, inner, "…")
		} else {
			parts = append(parts, part)
		}
	}
	lines := groupLines(parts, th, inner)
	if passed != "" {
		lines = append(lines, passed)
	}
	return lines
}

// groupLines lays out the lane line's groups: on one line when they fit,
// else two to a line in aligned columns, else one to a line.
func groupLines(parts []string, th theme.Theme, inner int) []string {
	if len(parts) == 0 {
		return nil
	}
	if one := strings.Join(parts, th.Muted().Render(" · ")); ansi.StringWidth(one) <= inner {
		return []string{one}
	}
	const gap = "   "
	left := 0
	for i := 0; i < len(parts); i += 2 {
		left = max(left, ansi.StringWidth(parts[i]))
	}
	right := 0
	for i := 1; i < len(parts); i += 2 {
		right = max(right, ansi.StringWidth(parts[i]))
	}
	var lines []string
	if left+len(gap)+right <= inner {
		for i := 0; i < len(parts); i += 2 {
			l := parts[i]
			if i+1 < len(parts) {
				l += strings.Repeat(" ", left-ansi.StringWidth(parts[i])) + gap + parts[i+1]
			}
			lines = append(lines, l)
		}
		return lines
	}
	for _, p := range parts {
		lines = append(lines, ansi.Truncate(p, inner, "…"))
	}
	return lines
}

// tabRow names the drawer's tabs, the current one filled in charcoal.
func (m drawerModel) tabRow(th theme.Theme) string {
	parts := make([]string, len(tabNames))
	for i, n := range tabNames {
		parts[i] = th.Tab(n, i == m.tab)
	}
	return strings.Join(parts, " ")
}

// orderedChecks is the Checks tab's rows in the order it lists them: by
// group, then as GitHub listed them. Passed checks are left out unless
// shown.
func orderedChecks(rows []status.CheckRow, showPassed bool) []status.CheckRow {
	out := slices.Clone(rows)
	slices.SortStableFunc(out, func(a, b status.CheckRow) int { return int(a.Group) - int(b.Group) })
	if !showPassed {
		out = slices.DeleteFunc(out, func(r status.CheckRow) bool { return r.Group == status.GroupPassed })
	}
	return out
}

// checksLayout is the Checks tab under the header: each group that has
// checks under its heading, with the passed ones folded until s. spans
// holds where each selectable check sits, in orderedChecks order.
func (m drawerModel) checksLayout(c drawerCtx, lines []string, inner int) ([]string, []itemSpan) {
	th := c.Theme
	rows := checkRows(c)
	if len(rows) == 0 {
		return append(lines, "", th.Muted().Render("No checks.")), nil
	}
	shown := orderedChecks(rows, m.showPassed)
	sel := min(m.checkSel, max(len(shown)-1, 0))
	var spans []itemSpan
	i := 0
	for _, cg := range checkGroups {
		var group []status.CheckRow
		for _, r := range rows {
			if r.Group == cg.group {
				group = append(group, r)
			}
		}
		if len(group) == 0 {
			continue
		}
		lines = append(lines, "")
		if cg.group == status.GroupPassed {
			mark := map[bool]string{true: "▾ ", false: "▸ "}[m.showPassed]
			lines = append(lines, th.Accent().Render(mark+cg.heading+" ("+strconv.Itoa(len(group))+")")+th.Muted().Render("  s"))
			if !m.showPassed {
				continue
			}
		} else {
			lines = append(lines, th.Accent().Render(cg.heading))
		}
		for _, r := range group {
			spans = append(spans, itemSpan{start: len(lines), head: len(lines) + 1, end: len(lines) + 1})
			lines = append(lines, checkLine(th, r, i == sel, inner))
			i++
		}
	}
	return lines, spans
}

// checkLine is one check: the selection mark, its glyph, its name, and
// muted tags for a required check and the glob that ignored it.
func checkLine(th theme.Theme, r status.CheckRow, selected bool, inner int) string {
	mark := "  "
	if selected {
		mark = th.Accent().Render("▸ ")
	}
	var tags []string
	if r.Check.Required {
		tags = append(tags, "required")
	}
	if r.Pattern != "" {
		tags = append(tags, `matches "`+cleanLine(r.Pattern)+`"`)
	}
	line := mark + groupGlyph(th, r.Group) + " " + cleanLine(r.Check.Name)
	if len(tags) > 0 {
		line += th.Muted().Render("  " + strings.Join(tags, " · "))
	}
	return ansi.Truncate(line, inner, "…")
}

// checksKey handles a key on the Checks tab.
func (m drawerModel) checksKey(msg tea.KeyPressMsg, c drawerCtx) (drawerModel, tea.Cmd, action) {
	_, spans := m.layout(c)
	shown := orderedChecks(checkRows(c), m.showPassed)
	m.checkSel = min(m.checkSel, max(len(shown)-1, 0))
	switch k := msg.String(); k {
	case "j", "down":
		m.checkScroll++
	case "k", "up":
		m.checkScroll--
	case "n", "N":
		if len(shown) > 0 {
			m.checkSel = min(max(m.checkSel+map[string]int{"n": 1, "N": -1}[k], 0), len(shown)-1)
			m.checkScroll = scrollToItem(m.checkScroll, spans[m.checkSel], m.bodyHeight(c.Height))
		}
	case "s":
		m.showPassed = !m.showPassed
		m.checkSel = min(m.checkSel, max(len(orderedChecks(checkRows(c), m.showPassed))-1, 0))
	case "O":
		if len(shown) > 0 {
			return m, nil, actionOpen{URL: checkURL(c.PR.PR.URL, shown[m.checkSel])}
		}
	case "y":
		if len(shown) > 0 {
			m.flash = "copied the check's name"
			return m, nil, actionCopy{Text: cleanLine(shown[m.checkSel].Check.Name)}
		}
	default:
		return m.sharedKey(k, c)
	}
	m.checkScroll = min(max(m.checkScroll, 0), m.maxScroll(c))
	return m, nil, actionNone{}
}

// checkURL is the check's own page, or the PR's checks page for a check
// stored without one.
func checkURL(prURL string, r status.CheckRow) string {
	if u := r.Check.URL; strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
		return u
	}
	return strings.TrimSuffix(prURL, "/") + "/checks"
}
