package theme

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"
)

// veilAmount is how far Dim moves each colour of the pane without focus
// toward the terminal's background: enough that the focused pane is
// plainly the one to read, while the other stays legible.
const veilAmount = 0.55

// basicRGB are xterm's default values for the 16 basic colours. Terminal
// themes remap these, so a veiled basic colour is only approximate; snow's
// own palette never uses them, but glamour or a PR's text may.
var basicRGB = [16][3]uint8{
	{0, 0, 0}, {205, 0, 0}, {0, 205, 0}, {205, 205, 0}, {0, 0, 238}, {205, 0, 205}, {0, 205, 205}, {229, 229, 229},
	{127, 127, 127}, {255, 0, 0}, {0, 255, 0}, {255, 255, 0}, {92, 92, 255}, {255, 0, 255}, {0, 255, 255}, {255, 255, 255},
}

// sgrColours is the foreground and background an SGR stream has set so
// far; nil is the terminal's default.
type sgrColours struct{ fg, bg color.Color }

// indexed is an xterm-256 index as RGB.
func indexed(n int) color.Color {
	if n >= 0 && n < 16 {
		c := basicRGB[n]
		return color.RGBA{c[0], c[1], c[2], 0xff}
	}
	r, g, b := XtermRGB(n)
	return color.RGBA{r, g, b, 0xff}
}

// apply folds one SGR sequence's parameters into s. Only colours are
// tracked; other attributes pass through untouched in the line itself.
func (s sgrColours) apply(params string) sgrColours {
	ps := strings.FieldsFunc(params, func(r rune) bool { return r == ';' || r == ':' })
	if len(ps) == 0 {
		return sgrColours{}
	}
	num := func(i int) int {
		if i >= len(ps) {
			return -1
		}
		n, err := strconv.Atoi(ps[i])
		if err != nil {
			return -1
		}
		return n
	}
	for i := 0; i < len(ps); i++ {
		switch n := num(i); {
		case n == 0:
			s = sgrColours{}
		case n == 39:
			s.fg = nil
		case n == 49:
			s.bg = nil
		case n >= 30 && n <= 37:
			s.fg = indexed(n - 30)
		case n >= 90 && n <= 97:
			s.fg = indexed(n - 90 + 8)
		case n >= 40 && n <= 47:
			s.bg = indexed(n - 40)
		case n >= 100 && n <= 107:
			s.bg = indexed(n - 100 + 8)
		case n == 38 || n == 48:
			var c color.Color
			switch num(i + 1) {
			case 5:
				c, i = indexed(num(i+2)), i+2
			case 2:
				c = color.RGBA{uint8(num(i + 2)), uint8(num(i + 3)), uint8(num(i + 4)), 0xff}
				i += 4
			default:
				continue
			}
			if n == 38 {
				s.fg = c
			} else {
				s.bg = c
			}
		}
	}
	return s
}

// mix moves c toward to by amount (0 keeps c, 1 gives to).
func mix(c, to color.Color, amount float64) (r, g, b uint8) {
	cr, cg, cb, _ := c.RGBA()
	tr, tg, tb, _ := to.RGBA()
	ch := func(a, b uint32) uint8 {
		return uint8((float64(a>>8)*(1-amount) + float64(b>>8)*amount) + 0.5)
	}
	return ch(cr, tr), ch(cg, tg), ch(cb, tb)
}

// isDark reports whether c is a dark colour, by its perceived lightness.
func isDark(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return 0.299*float64(r>>8)+0.587*float64(g>>8)+0.114*float64(b>>8) < 128
}

// veil rewrites a styled line so every colour in it sits veilAmount of the
// way toward bg: after each SGR sequence (and at the start) it sets the
// blended foreground, and the blended background where the line set one.
// Text in the terminal's default colour is blended from fg. Attributes such
// as bold pass through.
func veil(line string, bg, fg color.Color) string {
	var b strings.Builder
	var s sgrColours
	set := func() {
		f := s.fg
		if f == nil {
			f = fg
		}
		r, g, bl := mix(f, bg, veilAmount)
		fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm", r, g, bl)
		if s.bg != nil {
			r, g, bl := mix(s.bg, bg, veilAmount)
			fmt.Fprintf(&b, "\x1b[48;2;%d;%d;%dm", r, g, bl)
		}
	}
	set()
	last := 0
	for _, loc := range sgr.FindAllStringIndex(line, -1) {
		b.WriteString(line[last:loc[1]])
		s = s.apply(line[loc[0]+2 : loc[1]-1])
		set()
		last = loc[1]
	}
	b.WriteString(line[last:])
	b.WriteString("\x1b[0m")
	return b.String()
}

// Dim draws a styled line for the pane that doesn't have focus. With the
// terminal's background known (bg; fg is its default text colour, guessed
// from bg when unknown) every colour is blended most of the way toward the
// background, as if behind a grey veil. Without it, the line is faint, the
// terminal's own dim attribute, which some terminals draw weakly or not at
// all. With NO_COLOR the line comes back as it was.
func (t Theme) Dim(line string, bg, fg color.Color) string {
	switch {
	case t.noColor:
		return line
	case bg == nil:
		return keep(line, "\x1b[2m")
	}
	if fg == nil {
		fg = color.RGBA{0x30, 0x30, 0x30, 0xff}
		if isDark(bg) {
			fg = color.RGBA{0xd0, 0xd0, 0xd0, 0xff}
		}
	}
	return veil(line, bg, fg)
}
