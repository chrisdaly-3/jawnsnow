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

const (
	boxMinHeight = 3
	boxMaxHeight = 8
)

// summaryBox is a box holding sel's summary, sized for the current window.
// Its cursor doesn't blink, so it needs no ticks of its own.
func (m Model) summaryBox(sel post.Selection) textarea.Model {
	b := textarea.New()
	b.Prompt = ""
	b.ShowLineNumbers = false
	b.CharLimit = 0
	b.DynamicHeight = true
	b.MinHeight, b.MaxHeight = boxMinHeight, boxMaxHeight
	b.Placeholder = "Write a summary (optional)"
	if sel.Event == post.RequestChanges {
		b.Placeholder = "Write a summary (Request changes needs one)"
	}
	b.SetStyles(boxStyles(m.theme))
	b.SetWidth(postInner(m.width))
	b.SetValue(sel.Body)
	return b
}

// boxStyles draws the box in snow's own palette. textarea's defaults use
// the basic ANSI colours, which terminal themes remap, so every style is
// set here: a rounded border in the accent colour while the box has focus
// and muted when not, muted placeholder text, and an accent cursor.
func boxStyles(th theme.Theme) textarea.Styles {
	plain := lipgloss.NewStyle()
	frame := plain.Border(lipgloss.RoundedBorder()).Padding(0, 1)
	state := func(border lipgloss.Style) textarea.StyleState {
		return textarea.StyleState{
			Base: border, Text: plain, LineNumber: plain, CursorLineNumber: plain,
			CursorLine: plain, EndOfBuffer: plain, Placeholder: th.Muted(), Prompt: plain,
			Selection: plain.Reverse(true),
		}
	}
	st := textarea.Styles{
		Focused: state(frame.BorderForeground(th.AccentColor())),
		Blurred: state(frame.BorderForeground(th.MutedColor())),
		Cursor:  textarea.CursorStyle{Color: th.AccentColor(), Shape: tea.CursorBar},
	}
	if th.NoColor() {
		st.Focused, st.Blurred = state(frame), state(frame)
		st.Focused.Placeholder, st.Blurred.Placeholder = plain, plain
		st.Cursor.Color = nil
	}
	return st
}

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
	p.box.SetWidth(postInner(m.width))
	var cmd tea.Cmd
	switch msg.String() {
	case "tab", "esc":
		p.inBox = false
		p.box.Blur()
		m.post = &p
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
	return m.syncSummary(p), cmd
}

// postPaste puts pasted text into the summary box.
func (m Model) postPaste(msg tea.PasteMsg) (Model, tea.Cmd) {
	p := *m.post
	p.box.SetWidth(postInner(m.width))
	var cmd tea.Cmd
	p.box, cmd = p.box.Update(msg)
	return m.syncSummary(p), cmd
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
		return "tab to write in the box · e to open in $EDITOR"
	case p.enterPosts:
		return "tab to leave the box · shift+enter for a new line · enter to post"
	}
	return "tab to leave the box, then enter to post · enter for a new line"
}
