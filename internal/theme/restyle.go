package theme

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// selFill is the fill behind the drawer's selected finding, check or
// entry: between glamour's inline-code fill (236) and the tab fill (238),
// so it reads as neither code nor a tab. Like tabFill, it isn't configurable.
const selFill = 237

// sgr matches one SGR escape sequence, the kind lipgloss and glamour style
// text with.
var sgr = regexp.MustCompile("\x1b\\[[0-9;:]*m")

// keep applies codes to an already styled line and keeps them in force:
// they go at its start and again after each SGR sequence the line has, so
// the line's own resets can't end them. The line's own colours stay.
func keep(line, codes string) string {
	return codes + sgr.ReplaceAllStringFunc(line, func(s string) string { return s + codes }) + "\x1b[0m"
}

// Selected fills a styled line, padded to width cells, with the selection
// fill, keeping the colours of its text. With NO_COLOR the line comes back
// as it was: the ▸ mark beside it still shows the selection.
func (t Theme) Selected(line string, width int) string {
	if t.noColor {
		return line
	}
	line += strings.Repeat(" ", max(width-ansi.StringWidth(line), 0))
	return keep(line, "\x1b[48;5;"+strconv.Itoa(selFill)+"m")
}

// Dim makes a styled line faint, keeping its own colours, for the pane
// that doesn't have focus. Faint is the terminal's own attribute, so it
// works on light and dark themes alike; some terminals draw it weakly or
// not at all. With NO_COLOR the line comes back as it was.
func (t Theme) Dim(line string) string {
	if t.noColor {
		return line
	}
	return keep(line, "\x1b[2m")
}
