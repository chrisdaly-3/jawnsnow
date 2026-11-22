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

// painter colours strings with the chart palette. It builds each colour's
// style once per Render rather than once per braille cell.
type painter struct {
	noColor bool
	styles  map[int]lipgloss.Style
}

func newPainter(noColor bool) *painter {
	return &painter{noColor: noColor, styles: map[int]lipgloss.Style{}}
}

func (p *painter) paint(s string, color int) string {
	if p.noColor || s == "" {
		return s
	}
	st, ok := p.styles[color]
	if !ok {
		st = lipgloss.NewStyle().Foreground(lipgloss.Color(strconv.Itoa(paletteColor(color))))
		p.styles[color] = st
	}
	return st.Render(s)
}

// Render draws the lines as exactly Height rows, each at most Width cells
// wide. The bottom two rows are the x labels and the legend.
func Render(lines []Line, o Options) []string {
	if o.Height <= 0 {
		return nil
	}
	rows := make([]string, o.Height)
	paint := newPainter(o.NoColor)
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

	grid := newGrid(plotRows, plotW)
	if plotW > 0 {
		drawLines(grid, lines, top)
	}

	cursorCol := -1
	if plotW > 0 && o.Cursor >= 0 && o.Cursor < longest {
		cursorCol = pointX(o.Cursor, longest, plotW*2) / 2
	}

	for y := range plotRows {
		lab := axisLabel(y, plotRows, topL, midL, botL)
		rows[y] = ansi.Truncate(plotRow(grid[y], lab, lw, cursorCol, paint), o.Width, "")
	}

	if o.Height >= 2 {
		rows[o.Height-2] = ansi.Truncate(strings.Repeat(" ", lw)+xLabelRow(o.XLabels, max(o.Width-lw, 0)), o.Width, "")
	}
	rows[o.Height-1] = legend(lines, o, paint)
	return rows
}

// newGrid returns a rows by cols grid of empty cells, none yet colored.
func newGrid(rows, cols int) [][]cell {
	grid := make([][]cell, rows)
	for y := range grid {
		grid[y] = make([]cell, cols)
		for x := range grid[y] {
			grid[y][x].color = -1
		}
	}
	return grid
}

// drawLines plots each line's dots into a non-empty grid, scaled so top is
// the top dot row and joining each point to the one before. A cell crossed
// by several lines takes the color of the line drawn last (the later entry
// in lines).
func drawLines(grid [][]cell, lines []Line, top float64) {
	dw, dh := len(grid[0])*2, len(grid)*4
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

// plotRow draws one row of the plot: its label right-aligned in an axis
// column lw wide, the "┤" glyph, then each cell's braille dots, the cursor
// or a blank.
func plotRow(cells []cell, lab string, lw, cursorCol int, paint *painter) string {
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", max(lw-1-lipgloss.Width(lab), 0)))
	b.WriteString(lab)
	b.WriteString("┤")
	for x, c := range cells {
		switch {
		case c.bits != 0:
			b.WriteString(paint.paint(string(rune(0x2800+int(c.bits))), c.color))
		case x == cursorCol:
			b.WriteString(paint.paint("│", cursorColor))
		default:
			b.WriteString(" ")
		}
	}
	return b.String()
}

// axisLabel is the y-axis label for plot row y of rows: top on the first,
// bottom on the last, and mid on the middle row when there are at least 3.
func axisLabel(y, rows int, topL, midL, botL string) string {
	switch {
	case y == 0:
		return topL
	case y == rows-1:
		return botL
	case y == (rows-1)/2 && rows >= 3:
		return midL
	}
	return ""
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

func legend(lines []Line, o Options, paint *painter) string {
	parts := make([]string, 0, len(lines))
	for _, l := range lines {
		parts = append(parts, paint.paint("■ "+l.Name, l.Color))
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
