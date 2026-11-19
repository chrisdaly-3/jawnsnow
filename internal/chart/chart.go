// Package chart draws braille line charts as lines of terminal text. It is a
// pure package: no I/O, no global state.
package chart

import (
	"math"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Line is one series. Color is an xterm-256 index (16–255), chosen by the
// caller.
type Line struct {
	Name   string
	Values []float64
	Color  int
}

// Options sizes and decorates the chart. Cursor is a data index; a value
// outside [0, longest series) draws no cursor.
type Options struct {
	Width, Height, Cursor int
	YLabel                func(float64) string
	XLabels               []string
	NoColor               bool
}

// cursorColor is the muted grey used for the cursor column.
const cursorColor = 244

// cell is one braille character: a 2x4 grid of dots.
type cell struct {
	bits  uint8
	color int // -1 when no line has touched the cell
}

// dotBits[x][y] is the braille bit for dot column x (0..1), row y (0..3).
var dotBits = [2][4]uint8{
	{0x01, 0x02, 0x04, 0x40},
	{0x08, 0x10, 0x20, 0x80},
}

func (c *cell) set(x, y int) { c.bits |= dotBits[x][y] }

// niceMax returns the smallest of 1, 2 or 5 times a power of ten that is at
// least v. Zero, negative and non-finite values give 1, so axis labels never
// show NaN.
func niceMax(v float64) float64 {
	if !(v > 0) || math.IsInf(v, 0) {
		return 1
	}
	exp := math.Floor(math.Log10(v))
	for _, m := range []float64{1, 2, 5, 10} {
		if n := m * math.Pow(10, exp); n >= v*(1-1e-12) {
			return min(n, math.MaxFloat64)
		}
	}
	return math.MaxFloat64
}

func plainLabel(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// clean maps negative and non-finite values to 0.
func clean(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}

// paletteColor keeps colors inside xterm-256 16–255; the basic 16 are
// remapped by terminal themes, so anything else falls back to cursorColor.
func paletteColor(c int) int {
	if c < 16 || c > 255 {
		return cursorColor
	}
	return c
}

func colorize(s string, color int, noColor bool) string {
	if noColor || s == "" {
		return s
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(strconv.Itoa(paletteColor(color)))).Render(s)
}

// Render draws the lines as exactly Height rows, each at most Width cells
// wide. The bottom two rows are the x labels and the legend.
func Render(lines []Line, o Options) []string {
	if o.Height <= 0 {
		return nil
	}
	rows := make([]string, o.Height)
	if o.Width <= 0 {
		return rows
	}
	label := o.YLabel
	if label == nil {
		label = plainLabel
	}

	longest, maxV := 0, 0.0
	for _, l := range lines {
		longest = max(longest, len(l.Values))
		for _, v := range l.Values {
			maxV = max(maxV, clean(v))
		}
	}
	top := niceMax(maxV)

	plotRows := max(o.Height-2, 0)
	topL, midL, botL := label(top), label(top/2), label(0)
	// Axis column: widest label, then the "┤" glyph.
	lw := max(lipgloss.Width(topL), lipgloss.Width(midL), lipgloss.Width(botL)) + 1
	plotW := o.Width - lw
	if plotW < 1 || plotRows == 0 {
		plotW = 0
	}

	grid := make([][]cell, plotRows)
	for y := range grid {
		grid[y] = make([]cell, plotW)
		for x := range grid[y] {
			grid[y][x].color = -1
		}
	}

	if plotW > 0 {
		dw, dh := plotW*2, plotRows*4
		// A cell crossed by several lines takes the color of the line drawn
		// last (the later entry in lines).
		plot := func(l Line, x, y int) {
			c := &grid[y/4][x/2]
			c.set(x%2, y%4)
			c.color = l.Color
		}
		for _, l := range lines {
			var px, py int
			for i, raw := range l.Values {
				x := pointX(i, len(l.Values), dw)
				y := (dh - 1) - int(math.Round(clean(raw)/top*float64(dh-1)))
				y = min(max(y, 0), dh-1)
				if i == 0 {
					plot(l, x, y)
				} else {
					bresenham(px, py, x, y, func(x, y int) { plot(l, x, y) })
				}
				px, py = x, y
			}
		}
	}

	cursorCol := -1
	if plotW > 0 && o.Cursor >= 0 && o.Cursor < longest {
		cursorCol = pointX(o.Cursor, longest, plotW*2) / 2
	}

	midRow := (plotRows - 1) / 2
	for y := 0; y < plotRows; y++ {
		var lab string
		switch {
		case y == 0:
			lab = topL
		case y == plotRows-1:
			lab = botL
		case y == midRow && plotRows >= 3:
			lab = midL
		}
		var b strings.Builder
		b.WriteString(strings.Repeat(" ", max(lw-1-lipgloss.Width(lab), 0)))
		b.WriteString(lab)
		b.WriteString("┤")
		for x := 0; x < plotW; x++ {
			c := grid[y][x]
			switch {
			case c.bits != 0:
				b.WriteString(colorize(string(rune(0x2800+int(c.bits))), c.color, o.NoColor))
			case x == cursorCol:
				b.WriteString(colorize("│", cursorColor, o.NoColor))
			default:
				b.WriteString(" ")
			}
		}
		rows[y] = ansi.Truncate(b.String(), o.Width, "")
	}

	if o.Height >= 2 {
		rows[o.Height-2] = ansi.Truncate(strings.Repeat(" ", lw)+xLabelRow(o.XLabels, max(o.Width-lw, 0)), o.Width, "")
	}
	rows[o.Height-1] = legend(lines, o)
	return rows
}

// pointX is the dot column of point i of n across dw dots; one point sits at
// the left.
func pointX(i, n, dw int) int {
	if n <= 1 {
		return 0
	}
	return int(math.Round(float64(i) * float64(dw-1) / float64(n-1)))
}

// xLabelRow spreads labels evenly across w cells, first at the left edge and
// last flush right, dropping any that would touch the previous one.
func xLabelRow(labels []string, w int) string {
	buf := []rune(strings.Repeat(" ", w))
	end := -1
	for i, s := range labels {
		r := []rune(s)
		if len(r) > w {
			r = r[:w]
		}
		start := 0
		if len(labels) > 1 {
			start = int(math.Round(float64(i) * float64(w-len(r)) / float64(len(labels)-1)))
		}
		if start <= end || len(r) == 0 {
			continue
		}
		copy(buf[start:], r)
		end = start + len(r)
	}
	return strings.TrimRight(string(buf), " ")
}

func legend(lines []Line, o Options) string {
	var parts []string
	for _, l := range lines {
		parts = append(parts, colorize("■ "+l.Name, l.Color, o.NoColor))
	}
	return ansi.Truncate(strings.Join(parts, "  "), o.Width, "")
}

// bresenham calls set for every dot on the line from (x0,y0) to (x1,y1).
func bresenham(x0, y0, x1, y1 int, set func(x, y int)) {
	dx, dy := abs(x1-x0), -abs(y1-y0)
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	err := dx + dy
	for {
		set(x0, y0)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}
