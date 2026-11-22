package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/theme"
)

// The post flow's modal, drawn over the view, and its footer.

// eventLabel is how an event reads on screen; the enum stays in what is
// sent and stored.
func eventLabel(e string) string {
	for _, pe := range postEvents {
		if string(pe.event) == e {
			return pe.label
		}
	}
	return cleanLine(e)
}

// postedAgo is "just now" for the first minute, then "3m ago".
func postedAgo(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	return ago(d) + " ago"
}

func (p postFlow) hints() []keyHint {
	switch p.stage {
	case postLoading:
		return postLoadingKeys
	case postRepost:
		return postRepostKeys
	case postResult:
		return postResultKeys
	}
	if p.inFlight {
		return postPostingKeys
	}
	keys := postPreviewKeys(p.raw)
	switch p.focus {
	case focusSummary:
		keys = postBoxKeys(p.enterPosts)
	case focusEvent:
		keys = postEventKeys
	}
	if p.holdOn {
		return append([]keyHint{postHoldOnKey}, keys...)
	}
	return keys
}

// postBody draws the modal over the view.
func (m Model) postBody(width, height int) string {
	top, body, _, rows := m.postFrame(width, height)
	inner := postInner(width)
	bodyH := max(rows-len(top), 0)
	scroll := min(max(m.post.scroll, 0), max(len(body)-bodyH, 0))
	lines := append(slices.Clone(top), body[min(scroll, len(body)):]...)
	lines = lines[:min(len(lines), rows)]
	for i, l := range lines {
		l = ansi.Truncate(l, inner, "")
		lines[i] = l + strings.Repeat(" ", max(inner-ansi.StringWidth(l), 0))
	}
	box := m.theme.Border(true).Padding(0, 2).Render(strings.Join(lines, "\n"))
	return lipgloss.Place(max(width, 0), max(height, 0), lipgloss.Center, lipgloss.Center, box)
}

// postInner is the content width inside the box's border and padding.
func postInner(width int) int { return max(min(width, postMaxWidth)-6, 10) }

// Each heading says how to reach or use its part from where the focus is:
// tab goes down the screen and shift+tab up.
var (
	postEventHints = map[postFocus]string{
		focusEvent:    " ● h/l to move · space to choose",
		focusSummary:  " ● shift+tab to change",
		focusFindings: " ● tab to change",
	}
	postFindingsHints = map[postFocus]string{
		focusEvent:    " ● shift+tab to choose which to post",
		focusSummary:  " ● tab to choose which to post",
		focusFindings: " ● j/k to move · space to include or leave out",
	}
)

// checklistRow is f's line in the findings checklist: the cursor mark,
// whether it is included, where it is, and its ledger and posted tags. The
// cursor row is filled like the drawer's selection while the checklist
// has the keys.
func checklistRow(th theme.Theme, p *postFlow, f store.Finding, atCursor bool, inner int) string {
	mark, box := "  ", "[ ] "
	if atCursor {
		mark = th.Accent().Render("▸ ")
	}
	if included(p.sel, f.ID) {
		box = "[x] "
	}
	var row strings.Builder
	row.WriteString(mark)
	row.WriteString(box)
	row.WriteString(findingLocation(f))
	for _, l := range ledgerLabels[:3] {
		if f.Ledger == l.ledger {
			row.WriteString(th.Muted().Render("  [" + l.label + "]"))
		}
	}
	if f.ThreadID != "" {
		row.WriteString(th.Muted().Render("  [posted]"))
	}
	line := ansi.Truncate(row.String(), inner, "…")
	if atCursor && p.focus == focusFindings {
		line = th.Selected(line, inner)
	}
	return line
}

// postPreview is the "What will be posted" section. Each part is drawn
// from the payload's own string, so what shows is what is sent. built
// says whether the payload was built; one that wasn't has no parts.
func (m Model) postPreview(p *postFlow, built bool, inner int) []string {
	th, pv := m.theme, p.built.pv
	raw := "m for the raw markdown"
	if p.raw {
		raw = "m for the rendered view"
	}
	out := []string{ansi.Truncate(th.Accent().Render("What will be posted")+th.Muted().Render(" ● "+raw), inner, "…")}
	if built && len(pv.Parts) == 0 {
		out = append(out, th.Muted().Render("No summary or comments: only the event is posted."))
	}
	for _, part := range pv.Parts {
		out = append(out, th.Accent().Render(ansi.Truncate(cleanLine(part.Label), inner, "…")))
		var ls []string
		if p.raw {
			ls = strings.Split(ansi.Wrap(cleanBlock(part.Markdown), inner-2, ""), "\n")
		} else {
			ls = m.drawer.md.render(part.Markdown, inner-2, th)
		}
		for _, l := range ls {
			out = append(out, "  "+l)
		}
		out = append(out, "")
	}
	return out
}

// postFrame lays the modal out: the lines that stay put, the lines that
// scroll, the scrolling line the cursor is on, and the rows inside the box.
func (m Model) postFrame(width, height int) (top, body []string, cursorRow, rows int) {
	p, th := m.post, m.theme
	inner := postInner(width)
	rows = max(height-2, 1)
	wrap := func(s string, style lipgloss.Style) []string {
		var out []string
		for l := range strings.SplitSeq(ansi.Wrap(s, inner, ""), "\n") {
			out = append(out, style.Render(l))
		}
		return out
	}
	title := "Post review · " + cleanLine(p.review.Repo) + "#" + strconv.Itoa(p.review.Number)

	switch p.stage {
	case postLoading:
		return []string{th.Accent().Render(title), "", th.Muted().Render("Checking earlier posts…")}, nil, 0, rows
	case postRepost:
		a := p.ask
		q := fmt.Sprintf("Already posted as %s %s — post again? y/n", eventLabel(a.event), postedAgo(m.now().Sub(a.at)))
		if a.uncertain {
			q = fmt.Sprintf("A post may already have reached GitHub %s — post again? y/n", postedAgo(m.now().Sub(a.at)))
		}
		return append([]string{th.Accent().Render(title), ""}, wrap(q, th.Accent())...), nil, 0, rows
	case postResult:
		return append([]string{th.Accent().Render(title), ""}, wrap(cleanLine(p.result), th.StatusFg("CI_FAILED"))...), nil, 0, rows
	}

	pv, err := p.built.pv, p.built.err
	commit := shortOID(p.review.HeadOID)
	if err == nil {
		commit = pv.Commit
	}
	top = []string{th.Accent().Render(title + " · " + cleanLine(commit))}
	if t, ok := m.prs[p.review.PRID]; ok && t.PR.HeadOID != "" && t.PR.HeadOID != p.review.HeadOID {
		top = append(top, wrap(headMovedWarning, th.StatusFg("CI_WAITING"))...)
	}
	// Build's refusal (Request changes with no summary, a Comment with
	// nothing in it) shows once enter has tried to post, not while you are
	// still writing.
	if err != nil && p.showBuildErr {
		top = append(top, wrap(cleanLine(err.Error()), th.StatusFg("CI_FAILED"))...)
	}
	if p.err != "" {
		top = append(top, wrap(cleanLine(p.err), th.StatusFg("CI_FAILED"))...)
	}
	if p.inFlight {
		top = append(top, th.Accent().Render("Posting to GitHub…"))
	}
	if len(pv.Hidden) > 0 {
		items := make([]string, len(pv.Hidden))
		for i, h := range pv.Hidden {
			items[i] = cleanLine(h)
		}
		top = append(top, wrap("Rendered view hides: "+strings.Join(items, "; ")+" — press m to see the raw text", th.StatusFg("CI_WAITING"))...)
	}
	eventHint := postEventHints[p.focus]
	top = append(top, "")
	// The event row and the box scroll with the rest, so a short window
	// still shows the checklist; the scroll follows whichever part has the
	// keys (eventRow, summaryRow, or the checklist's cursor).
	eventRow := len(body)
	body = append(body, ansi.Truncate(th.Accent().Render("Post as")+th.Muted().Render(eventHint), inner, "…"), m.eventRow(*p), "")
	summaryRow := len(body)
	body = append(body, ansi.Truncate(th.Accent().Render("Summary")+th.Muted().Render(" ● "+p.boxHelp()), inner, "…"))
	box := p.box
	box.SetWidth(max(inner-4, 1))
	body = append(body, strings.Split(th.Border(p.inBox()).Padding(0, 1).Render(box.View()), "\n")...)
	body = append(body, "")

	fs := orderedFindings(p.review.Findings)
	if len(fs) > 0 {
		body = append(body, ansi.Truncate(th.Accent().Render("Findings")+th.Muted().Render(postFindingsHints[p.focus]), inner, "…"))
	}
	for i, f := range fs {
		if i == p.cursor {
			cursorRow = len(body)
		}
		body = append(body, checklistRow(th, p, f, i == p.cursor, inner))
	}
	if len(fs) > 0 {
		body = append(body, "")
	}
	body = append(body, m.postPreview(p, err == nil, inner)...)
	switch p.focus {
	case focusEvent:
		cursorRow = eventRow
	case focusSummary:
		cursorRow = summaryRow
	}
	return top, body, cursorRow, rows
}
