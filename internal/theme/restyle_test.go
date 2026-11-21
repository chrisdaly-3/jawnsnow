package theme

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
)

func TestSelectedKeepsItsFillAcrossInnerResets(t *testing.T) {
	th := mustNew(t, config.ThemeConfig{})
	// Two styled pieces: each ends with a reset that would end a fill
	// wrapped around the whole line.
	line := th.Accent().Render("pkg/a.go:12") + "  " + lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Render("blocking")
	got := th.Selected(line, 30)
	if w := ansi.StringWidth(got); w != 30 {
		t.Errorf("width %d, want 30", w)
	}
	if ansi.Strip(got) != ansi.Strip(line)+strings.Repeat(" ", 30-ansi.StringWidth(line)) {
		t.Errorf("text changed: %q", ansi.Strip(got))
	}
	fill := "\x1b[48;5;237m"
	if !strings.HasPrefix(got, fill) {
		t.Errorf("the fill doesn't start the line: %q", got)
	}
	// Every reset in the line is followed by the fill again; the two
	// pieces have one each.
	parts := strings.Split(got, "\x1b[m")
	if len(parts) < 3 {
		t.Fatalf("found %d resets, want the two pieces' own: %q", len(parts)-1, got)
	}
	for i, part := range parts[1:] {
		if !strings.HasPrefix(part, fill) {
			t.Errorf("reset %d isn't followed by the fill: %q", i+1, got)
		}
	}
	if !strings.Contains(got, "38;5;203") {
		t.Errorf("the line lost its own colour: %q", got)
	}
}

func TestSelectedWithNoColorIsUnchanged(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	th := mustNew(t, config.ThemeConfig{})
	if got := th.Selected("▸ pkg/a.go:12", 30); got != "▸ pkg/a.go:12" {
		t.Errorf("Selected = %q, want the line unchanged", got)
	}
}
