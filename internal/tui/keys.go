package tui

import (
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
)

type keyHint struct{ keys, help string }

type keySection struct {
	title string
	keys  []keyHint
}

// keyMap is every key snow uses; the help overlay lists all of it.
var keyMap = []keySection{
	{"Everywhere", []keyHint{
		{"[ ]", "previous or next view"},
		{"1-5", "jump to a view"},
		{"tab shift+tab", "move focus"},
		{"R", "poll now"},
		{"m", "toggle watcher or auto mode"},
		{"?", "show or hide help"},
		{"esc", "close help"},
		{"q ctrl+c", "quit"},
	}},
	{"Wall", []keyHint{
		{"h l", "previous or next lane"},
		{"j k", "previous or next card"},
		{"x", "hide"},
		{"enter", "open the review drawer"},
		{"o", "open in the browser"},
		{"space", "review with Claude now"},
		{"c", "review with a model and effort you pick"},
		{"/", "filter"},
		{"a", "filter by author"},
		{"esc", "clear the filters"},
	}},
	{"Drawer", []keyHint{
		{"esc", "close the drawer"},
		{"j k", "scroll"},
		{"n p", "next or previous finding"},
		{"< >", "older or newer pass"},
		{"u a", "show or hide understanding, assumptions"},
		{"o", "open the PR"},
		{"O", "open the finding's line"},
		{"y", "copy the finding"},
		{"space c", "review again, or pick a model"},
	}},
	{"Agent", []keyHint{
		{"j k", "choose a tier"},
		{"enter", "pick a model and effort for this session"},
		{"x", "clear the tier's session pick"},
		{"e", "edit config.toml in $VISUAL or $EDITOR"},
	}},
	{"Done", []keyHint{
		{"j k", "previous or next row"},
		{"x", "unhide"},
		{"enter", "open the review drawer"},
		{"o", "open in the browser"},
	}},
}

func allKeys() []keyHint {
	var out []keyHint
	for _, s := range keyMap {
		out = append(out, s.keys...)
	}
	return out
}

// footerKeys are the short hints for each view, before the global ones.
var footerKeys = map[View][]keyHint{
	ViewWall:  {{"h/l", "lane"}, {"j/k", "card"}, {"space", "review"}, {"x", "hide"}, {"o", "open"}, {"/", "filter"}, {"a", "author"}},
	ViewAgent: {{"j/k", "tier"}, {"enter", "pick"}, {"x", "clear"}, {"e", "edit config"}},
	ViewDone:  {{"j/k", "row"}, {"x", "unhide"}, {"o", "open"}},
}

var footerGlobal = []keyHint{{"[ ]", "views"}, {"R", "poll"}, {"m", "mode"}, {"?", "help"}, {"q", "quit"}}

func footerHints(v View) []keyHint { return slices.Concat(footerKeys[v], footerGlobal) }

func (m Model) helpBody(width, height int) string {
	keyW := 0
	for _, k := range allKeys() {
		keyW = max(keyW, lipgloss.Width(k.keys))
	}
	var b strings.Builder
	for i, s := range keyMap {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(m.theme.Accent().Render(s.title))
		for _, k := range s.keys {
			b.WriteString("\n  " + m.theme.Accent().Render(k.keys) + strings.Repeat(" ", keyW-lipgloss.Width(k.keys)+2) + k.help)
		}
	}
	box := m.theme.Border(true).Padding(0, 2).Render(b.String())
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}
