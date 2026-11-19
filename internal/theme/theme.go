// Package theme turns the theme configuration into lipgloss styles: a fixed
// xterm-256 palette for status chips and the UI frame.
package theme

import (
	"fmt"
	"image/color"
	"os"
	"strconv"

	"charm.land/bubbles/v2/spinner"
	"charm.land/lipgloss/v2"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
)

// statusDef is the resolved look of one status.
type statusDef struct {
	label    string
	color    color.Color
	fg       color.Color // chip text color, chosen to contrast with color
	spinner  spinner.Spinner
	spins    bool
	outlined bool
}

// Theme holds the resolved styles. Build one with New.
type Theme struct {
	statuses    map[string]statusDef
	accentColor color.Color
	mutedColor  color.Color
	noColor     bool
	markdown    string
}

type defaultStyle struct {
	label    string
	color    string
	spinner  string
	outlined bool
}

// statusOrder lists the statuses in a stable order.
var statusOrder = []string{
	"CI_WAITING", "CI_FAILED", "READY_FOR_REVIEW", "NEW_CHANGES",
	"CHANGES_REQUESTED", "APPROVED", "APPROVED_BY_YOU", "MERGED", "CLOSED",
}

var defaults = map[string]defaultStyle{
	"CI_WAITING":        {label: "Waiting on CI", color: "214", spinner: "dot"},
	"CI_FAILED":         {label: "CI failed", color: "203", spinner: "none"},
	"READY_FOR_REVIEW":  {label: "Ready for you", color: "39", spinner: "none"},
	"NEW_CHANGES":       {label: "New changes", color: "141", spinner: "dot"},
	"CHANGES_REQUESTED": {label: "Waiting on author", color: "208", spinner: "none"},
	"APPROVED":          {label: "Approved", color: "41", spinner: "none"},
	"APPROVED_BY_YOU":   {label: "Approved by you · needs more", color: "41", spinner: "none", outlined: true},
	"MERGED":            {label: "Merged", color: "140", spinner: "none"},
	"CLOSED":            {label: "Closed", color: "244", spinner: "none"},
}

const (
	defaultAccent = "153"
	defaultMuted  = "67"
)

var spinners = map[string]spinner.Spinner{
	"dot":     spinner.Dot,
	"line":    spinner.Line,
	"minidot": spinner.MiniDot,
	"pulse":   spinner.Pulse,
	"points":  spinner.Points,
	"meter":   spinner.Meter,
}

// New resolves cfg over the defaults. It returns an error for a color it
// cannot parse or a spinner it does not know, though Validate catches both
// before a config reaches here.
func New(cfg config.ThemeConfig) (Theme, error) {
	t := Theme{
		statuses: make(map[string]statusDef, len(statusOrder)),
		noColor:  os.Getenv("NO_COLOR") != "",
		markdown: "dark",
	}
	if cfg.Markdown == "light" {
		t.markdown = "light"
	}
	for _, name := range statusOrder {
		d := defaults[name]
		if o, ok := cfg.Status[name]; ok {
			if o.Label != "" {
				d.label = o.Label
			}
			if o.Color != "" {
				d.color = o.Color
			}
			if o.Spinner != "" {
				d.spinner = o.Spinner
			}
		}
		c, fg, err := parseColor(d.color)
		if err != nil {
			return Theme{}, fmt.Errorf("theme.status.%s.color: %w", name, err)
		}
		def := statusDef{label: d.label, color: c, fg: fg, outlined: d.outlined}
		if d.spinner != "none" {
			sp, ok := spinners[d.spinner]
			if !ok {
				return Theme{}, fmt.Errorf("theme.status.%s.spinner %q: unknown spinner", name, d.spinner)
			}
			def.spinner, def.spins = sp, true
		}
		t.statuses[name] = def
	}

	accent, muted := defaultAccent, defaultMuted
	if cfg.Chrome.Accent != "" {
		accent = cfg.Chrome.Accent
	}
	if cfg.Chrome.Muted != "" {
		muted = cfg.Chrome.Muted
	}
	var err error
	if t.accentColor, _, err = parseColor(accent); err != nil {
		return Theme{}, fmt.Errorf("theme.chrome.accent: %w", err)
	}
	if t.mutedColor, _, err = parseColor(muted); err != nil {
		return Theme{}, fmt.Errorf("theme.chrome.muted: %w", err)
	}
	return t, nil
}

// parseColor accepts an xterm-256 index in 16–255 or #rrggbb. It returns the
// color and the black or white that reads best on top of it.
func parseColor(s string) (c, fg color.Color, err error) {
	if len(s) == 7 && s[0] == '#' {
		rgba, ok := lipgloss.Color(s).(color.RGBA)
		if !ok {
			return nil, nil, fmt.Errorf("invalid hex color %q", s)
		}
		return rgba, lipgloss.Color(strconv.Itoa(contrastFor(rgba.R, rgba.G, rgba.B))), nil
	}
	i, convErr := strconv.Atoi(s)
	if convErr != nil || i < 16 || i > 255 {
		return nil, nil, fmt.Errorf("invalid color %q: want an index 16–255 or #rrggbb", s)
	}
	return lipgloss.Color(s), lipgloss.Color(strconv.Itoa(ContrastFG(i))), nil
}

// Chip renders the status label as a padded chip with fixed foreground and
// background, so it reads the same on any terminal background. An outlined
// status gets a border in its color and no background. With NO_COLOR set the
// chip is plain text.
func (t Theme) Chip(status string) string {
	d, ok := t.statuses[status]
	if !ok {
		return ""
	}
	text := " " + d.label + " "
	if d.outlined {
		s := lipgloss.NewStyle().Border(lipgloss.RoundedBorder())
		if !t.noColor {
			s = s.Foreground(d.color).BorderForeground(d.color)
		}
		return s.Render(text)
	}
	if t.noColor {
		return text
	}
	return lipgloss.NewStyle().Foreground(d.fg).Background(d.color).Render(text)
}

// ChipLine is Chip on one line, for places with one row for it, like a
// card. An outlined status shows its label in brackets in its color, which
// keeps it apart from the filled chips and matches their width.
func (t Theme) ChipLine(status string) string {
	d, ok := t.statuses[status]
	if !ok || !d.outlined {
		return t.Chip(status)
	}
	text := "[" + d.label + "]"
	if t.noColor {
		return text
	}
	return lipgloss.NewStyle().Foreground(d.color).Render(text)
}

// Label returns the status's display text.
func (t Theme) Label(status string) string { return t.statuses[status].label }

// Color returns the status's color.
func (t Theme) Color(status string) color.Color { return t.statuses[status].color }

// Spinner returns the status's spinner, or false when it has none.
func (t Theme) Spinner(status string) (spinner.Spinner, bool) {
	d := t.statuses[status]
	return d.spinner, d.spins
}

// Accent styles highlighted frame text.
func (t Theme) Accent() lipgloss.Style { return t.fg(t.accentColor) }

// Muted styles de-emphasized frame text.
func (t Theme) Muted() lipgloss.Style { return t.fg(t.mutedColor) }

// Border is a rounded border, accent colored when focused and muted otherwise.
func (t Theme) Border(focused bool) lipgloss.Style {
	s := lipgloss.NewStyle().Border(lipgloss.RoundedBorder())
	if t.noColor {
		return s
	}
	if focused {
		return s.BorderForeground(t.accentColor)
	}
	return s.BorderForeground(t.mutedColor)
}

func (t Theme) fg(c color.Color) lipgloss.Style {
	if t.noColor {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(c)
}

// StatusFg styles text in the status's color, or plain with NO_COLOR.
func (t Theme) StatusFg(status string) lipgloss.Style { return t.fg(t.statuses[status].color) }

// Banner is text on a filled banner in the xterm-256 color index, in the
// black or white that reads best on it. It is plain with NO_COLOR.
func (t Theme) Banner(index int, text string) string {
	text = " " + text + " "
	if t.noColor {
		return text
	}
	return lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color(strconv.Itoa(ContrastFG(index)))).
		Background(lipgloss.Color(strconv.Itoa(index))).Render(text)
}

// NoColor reports whether NO_COLOR asked for plain text.
func (t Theme) NoColor() bool { return t.noColor }

// AccentColor and MutedColor are the chrome colors, for renderers that take
// color strings rather than lipgloss styles.
func (t Theme) AccentColor() color.Color { return t.accentColor }
func (t Theme) MutedColor() color.Color  { return t.mutedColor }

// Markdown is the markdown style for review text: "dark" or "light".
func (t Theme) Markdown() string { return t.markdown }
