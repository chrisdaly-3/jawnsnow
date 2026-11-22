package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The post screen's event row: Approve, Comment and Request changes side
// by side. Like the findings checklist it has a cursor and a choice: h and
// l move the cursor, space checks the event under it, and the checked
// event is what posts, wherever the cursor is left.

// eventKey handles a key while the event row has the keys. The checklist's
// j and k do nothing here; tab and shift+tab, enter, esc, e, m and the
// scroll keys work as they do on the checklist.
func (m Model) eventKey(p postFlow, k string) (Model, tea.Cmd) {
	p.holdOn = false
	switch k {
	case "h", "left":
		p.eventCursor = max(p.eventCursor-1, 0)
	case "l", "right":
		p.eventCursor = min(p.eventCursor+1, len(postEvents)-1)
	case "space":
		if e := postEvents[p.eventCursor].event; e != p.sel.Event {
			p.sel.Event = e
			p.box.Placeholder = boxPlaceholder(e)
			p.previewAt = m.now()
			p.rebuild()
		}
	case "j", "k", "down", "up":
	default:
		return m.previewKey(p, k)
	}
	m.post = &p
	return m, nil
}

// eventRow draws the event row: each event as " ✓ Label ", the check (in
// the approval colour) on the chosen one, and the cursor's cell marked ▸
// and filled while the row has the keys.
func (m Model) eventRow(p postFlow) string {
	th := m.theme
	cells := make([]string, len(postEvents))
	for i, e := range postEvents {
		mark, check := " ", " "
		if p.focus == focusEvent && i == p.eventCursor {
			mark = th.Accent().Render("▸")
		}
		if e.event == p.sel.Event {
			check = th.StatusFg("APPROVED").Render("✓")
		}
		cell := mark + check + " " + e.label + " "
		if p.focus == focusEvent && i == p.eventCursor {
			cell = th.Selected(cell, ansi.StringWidth(cell))
		}
		cells[i] = cell
	}
	return strings.Join(cells, " ")
}
