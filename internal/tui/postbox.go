package tui

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/chrisdaly-3/jawnsnow/internal/post"
	"github.com/chrisdaly-3/jawnsnow/internal/theme"
)

// The post preview's summary box: the summary is typed here, and the
// rendered Summary part below it shows what will be posted.

// The box is boxMinHeight lines tall while empty or short, so it reads as
// a place to write, and grows with its text up to boxMaxHeight.
const (
	boxMinHeight = 6
	boxMaxHeight = 12
)

// summaryBox is a box holding sel's summary, sized for the current window.
// Its cursor doesn't blink, so it needs no ticks of its own.
func (m Model) summaryBox(sel post.Selection) textarea.Model {
	b := textarea.New()
	b.Prompt = ""
	b.ShowLineNumbers = false
	b.CharLimit = 0
	b.DynamicHeight = true
	b.MinHeight, b.MaxHeight = boxHeights(m.height)
	b.Placeholder = boxPlaceholder(sel.Event)
	b.SetStyles(boxStyles(m.theme))
	b.SetWidth(boxWidth(m.width))
	b.SetValue(sel.Body)
	return b
}

// boxHeights is the box's least and most height in a window height lines
// tall: boxMinHeight and boxMaxHeight on a usual window, less on a short
// one, so the checklist under the box keeps some room.
func boxHeights(height int) (lo, hi int) {
	rows := max(height-4, 0) // the top line, the footer and the modal's border
	lo = min(boxMinHeight, max(rows/5, 3))
	hi = min(boxMaxHeight, max(rows/3, lo))
	return lo, hi
}

// resizeBox fits an open preview's box to a new window size.
func (m Model) resizeBox() Model {
	if m.post == nil || !m.post.opened {
		return m
	}
	p := *m.post
	p.box.MinHeight, p.box.MaxHeight = boxHeights(m.height)
	p.box.SetWidth(boxWidth(m.width))
	p.box.InsertString("") // recalculates its height; the text and cursor stay
	m.post = &p
	return m
}

// boxPlaceholder is the box's text while it is empty: optional for an
// approval or a comment, needed for Request changes.
func boxPlaceholder(e post.Event) string {
	if e == post.RequestChanges {
		return "Write a summary. Request changes needs one."
	}
	return "Write a summary (optional). " + eventLabel(string(e)) + " posts with or without one."
}

// boxStyles draws the box's text in snow's own palette. textarea's
// defaults use the basic ANSI colours, which terminal themes remap, so
// every style is set here: plain text, muted placeholder text and an
// accent cursor. The frame is drawn around the box by boxFrame, not here:
// textarea gives every line Base's style, so a border on Base is drawn
// again inside the box.
func boxStyles(th theme.Theme) textarea.Styles {
	plain := lipgloss.NewStyle()
	state := textarea.StyleState{
		Base: plain, Text: plain, LineNumber: plain, CursorLineNumber: plain,
		CursorLine: plain, EndOfBuffer: plain, Placeholder: th.Muted(), Prompt: plain,
		Selection: plain.Reverse(true),
	}
	st := textarea.Styles{
		Focused: state, Blurred: state,
		Cursor: textarea.CursorStyle{Color: th.AccentColor(), Shape: tea.CursorBar},
	}
	if th.NoColor() {
		st.Focused.Placeholder, st.Blurred.Placeholder = plain, plain
		st.Cursor.Color = nil
	}
	return st
}

// boxFrame is the rounded frame around the box: in the accent colour while
// it has the keys and muted when not, plain with NO_COLOR.
func boxFrame(th theme.Theme, focused bool) lipgloss.Style {
	f := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	switch {
	case th.NoColor():
		return f
	case focused:
		return f.BorderForeground(th.AccentColor())
	}
	return f.BorderForeground(th.MutedColor())
}

// boxWidth is the box's text width in a window width cells wide: the
// modal's content width less the frame's border and padding.
func boxWidth(width int) int { return max(postInner(width)-4, 1) }

// boxActive reports whether the summary box has the keys: the preview is
// open on it and no post or editor is running.
func (p *postFlow) boxActive() bool {
	return p != nil && p.stage == postPreview && p.inBox && !p.inFlight && !p.editing
}

// boxKey handles a key while the summary box has focus. tab and esc leave
// it for the checklist. enter posts when the terminal reports shift+enter
// apart from enter, and is a new line otherwise; shift+enter, alt+enter
// and ctrl+j are always a new line.
func (m Model) boxKey(p postFlow, msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p.holdOn = false
	p.box.SetWidth(boxWidth(m.width))
	var cmd tea.Cmd
	switch msg.String() {
	case "tab", "esc":
		p.inBox = false
		p.box.Blur()
		m.post = &p
		m.post.scroll = m.clampPostScroll(true) // to the checklist's cursor
		return m, nil
	case "enter":
		if p.enterPosts {
			return m.previewKey(p, "enter")
		}
		p.box.InsertString("\n")
	case "shift+enter", "alt+enter", "ctrl+j":
		p.box.InsertString("\n")
	default:
		p.box, cmd = p.box.Update(msg)
	}
	m = m.syncSummary(p)
	m.post.scroll = m.clampPostScroll(true) // the box stays in view as it grows
	return m, cmd
}

// postPaste puts pasted text into the summary box.
func (m Model) postPaste(msg tea.PasteMsg) (Model, tea.Cmd) {
	p := *m.post
	p.box.SetWidth(boxWidth(m.width))
	var cmd tea.Cmd
	p.box, cmd = p.box.Update(msg)
	m = m.syncSummary(p)
	m.post.scroll = m.clampPostScroll(true)
	return m, cmd
}

// syncSummary copies the box's text into the selection. A change rebuilds
// the payload and restarts the confirm delay, so an enter just after
// typing is held rather than posting text you haven't seen rendered.
func (m Model) syncSummary(p postFlow) Model {
	if body := strings.TrimRight(p.box.Value(), "\n"); body != p.sel.Body {
		p.sel.Body = body
		p.previewAt = m.now()
		p.rebuild()
	}
	m.post = &p
	return m
}

// boxHelp is the muted line beside the box's title: how to get out of the
// box, and what enter does in it.
func (p postFlow) boxHelp() string {
	switch {
	case !p.inBox:
		return "tab to write here · e to open in $EDITOR"
	case p.enterPosts:
		return "shift+enter for a new line · enter to post · tab for the findings"
	}
	return "enter for a new line · tab for the findings, then enter to post"
}
