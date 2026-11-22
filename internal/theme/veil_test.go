package theme

import (
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
)

var (
	black = color.RGBA{0, 0, 0, 0xff}
	white = color.RGBA{0xff, 0xff, 0xff, 0xff}
)

func TestDimVeilsEveryColourTowardTheBackground(t *testing.T) {
	th := mustNew(t, config.ThemeConfig{})
	// Default-coloured text, then 153 (ice blue: 175,215,255) on 214
	// (orange: 255,175,0), then default again after the reset.
	line := "plain " + lipgloss.NewStyle().Foreground(lipgloss.Color("153")).Background(lipgloss.Color("214")).Render("chip") + " tail"
	got := th.Dim(line, black, white)
	if ansi.Strip(got) != ansi.Strip(line) {
		t.Errorf("text changed: %q", ansi.Strip(got))
	}
	// 55% toward black: white 255 → 115; 175,215,255 → 79,97,115;
	// 255,175,0 → 115,79,0.
	for _, want := range []string{
		"\x1b[38;2;115;115;115m",                  // default text, from fg
		"\x1b[38;2;79;97;115m\x1b[48;2;115;79;0m", // the chip
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in %q", want, got)
		}
	}
	if !strings.HasPrefix(got, "\x1b[38;2;115;115;115m") {
		t.Errorf("the default text isn't veiled from the start: %q", got)
	}
	// After the chip's reset, the tail is default text again, with no fill.
	tail := got[strings.LastIndex(got, "chip"):]
	if strings.Contains(tail[strings.Index(tail, "\x1b[m"):], "48;2") {
		t.Errorf("the chip's fill outlived its reset: %q", tail)
	}
}

func TestDimGuessesTheDefaultTextColour(t *testing.T) {
	th := mustNew(t, config.ThemeConfig{})
	// No foreground reported: light text on a dark background (0xd0 → 94
	// at 55% toward black), dark text on a light one (0x30 → 162 toward
	// white).
	if got := th.Dim("x", black, nil); !strings.HasPrefix(got, "\x1b[38;2;94;94;94m") {
		t.Errorf("dark background: %q", got)
	}
	if got := th.Dim("x", white, nil); !strings.HasPrefix(got, "\x1b[38;2;162;162;162m") {
		t.Errorf("light background: %q", got)
	}
}

func TestDimWithoutABackgroundIsFaint(t *testing.T) {
	th := mustNew(t, config.ThemeConfig{})
	if got := th.Dim("x", nil, nil); !strings.HasPrefix(got, "\x1b[2m") {
		t.Errorf("no background known: %q, want faint", got)
	}
	t.Setenv("NO_COLOR", "1")
	th = mustNew(t, config.ThemeConfig{})
	if got := th.Dim("x", black, white); got != "x" {
		t.Errorf("NO_COLOR: %q, want the line unchanged", got)
	}
}

func TestSGRColoursApply(t *testing.T) {
	var s sgrColours
	s = s.apply("1;38;5;153;48;2;10;20;30")
	if s.fg != indexed(153) || s.bg != (color.RGBA{10, 20, 30, 0xff}) {
		t.Errorf("fg %v bg %v", s.fg, s.bg)
	}
	if s = s.apply("39"); s.fg != nil || s.bg == nil {
		t.Errorf("39 should clear only the foreground: %+v", s)
	}
	if s = s.apply(""); s.fg != nil || s.bg != nil {
		t.Errorf("an empty SGR is a reset: %+v", s)
	}
	if s = s.apply("31;102"); s.fg != indexed(1) || s.bg != indexed(10) {
		t.Errorf("basic colours: %+v", s)
	}
}
