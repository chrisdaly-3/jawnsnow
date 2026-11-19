package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/post"
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
	case postPick:
		return postPickKeys
	case postResult:
		return postResultKeys
	}
	if p.inFlight {
		return postPostingKeys
	}
	if p.holdOn {
		return append([]keyHint{postHoldOnKey}, postPreviewKeys(p.raw)...)
	}
	return postPreviewKeys(p.raw)
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

// postFrame lays the modal out: the lines that stay put, the lines that
// scroll, the scrolling line the cursor is on, and the rows inside the box.
func (m Model) postFrame(width, height int) (top, body []string, cursorRow, rows int) {
	p, th := m.post, m.theme
	inner := postInner(width)
	rows = max(height-2, 1)
	wrap := func(s string, style lipgloss.Style) []string {
		var out []string
		for _, l := range strings.Split(ansi.Wrap(s, inner, ""), "\n") {
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
	case postPick:
		top = []string{th.Accent().Render(title), ""}
		for i, e := range postEvents {
			mark := "  "
			if i == p.pick {
				mark = th.Accent().Render("▸ ")
			}
			top = append(top, mark+e.label)
		}
		return top, nil, 0, rows
	}

	_, pv, err := post.Build(p.review, p.review.PRID, p.sel)
	event, commit := string(p.sel.Event), shortCommit(p.review.HeadOID)
	if err == nil {
		event, commit = string(pv.Event), pv.Commit
	}
	top = []string{th.Accent().Render(title + " · " + eventLabel(event) + " · " + cleanLine(commit))}
	if t, ok := m.prs[p.review.PRID]; ok && t.PR.HeadOID != "" && t.PR.HeadOID != p.review.HeadOID {
		top = append(top, wrap(headMovedWarning, th.StatusFg("CI_WAITING"))...)
	}
	if err != nil {
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
	top = append(top, "")

	fs := orderedFindings(p.review.Findings)
	if len(fs) > 0 {
		body = append(body, th.Accent().Render("Findings"))
	}
	for i, f := range fs {
		mark, box := "  ", "[ ] "
		if i == p.cursor {
			mark, cursorRow = th.Accent().Render("▸ "), len(body)
		}
		if included(p.sel, f.ID) {
			box = "[x] "
		}
		row := mark + box + findingLocation(f)
		for _, l := range ledgerLabels[:3] {
			if f.Ledger == l.ledger {
				row += th.Muted().Render("  [" + l.label + "]")
			}
		}
		if f.ThreadID != "" {
			row += th.Muted().Render("  [posted]")
		}
		body = append(body, ansi.Truncate(row, inner, "…"))
	}
	if len(fs) > 0 {
		body = append(body, "")
	}
	if err == nil && len(pv.Parts) == 0 {
		body = append(body, th.Muted().Render("No summary or comments: only the event is posted."))
	}
	// Each part is drawn from the payload's own string, so what shows is
	// what is sent.
	for _, part := range pv.Parts {
		body = append(body, th.Accent().Render(ansi.Truncate(cleanLine(part.Label), inner, "…")))
		var ls []string
		if p.raw {
			ls = strings.Split(ansi.Wrap(cleanBlock(part.Markdown), inner-2, ""), "\n")
		} else {
			ls = m.drawer.md.render(part.Markdown, inner-2, th)
		}
		for _, l := range ls {
			body = append(body, "  "+l)
		}
		body = append(body, "")
	}
	return top, body, cursorRow, rows
}

func shortCommit(oid string) string { return oid[:min(len(oid), 7)] }
