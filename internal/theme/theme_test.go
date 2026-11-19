package theme

import (
	"image/color"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
)

var allStatuses = []string{
	"CI_WAITING", "CI_FAILED", "READY_FOR_REVIEW", "NEW_CHANGES",
	"CHANGES_REQUESTED", "APPROVED", "APPROVED_BY_YOU", "MERGED", "CLOSED",
}

func mustNew(t *testing.T, cfg config.ThemeConfig) Theme {
	t.Helper()
	th, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return th
}

func assertIndexed(t *testing.T, name string, c color.Color) ansi.IndexedColor {
	t.Helper()
	idx, ok := c.(ansi.IndexedColor)
	if !ok {
		t.Fatalf("%s: color is %T, want an ANSI256 index", name, c)
	}
	if idx < 16 || idx > 255 {
		t.Fatalf("%s: index %d outside 16-255", name, idx)
	}
	return idx
}

func TestDefaultPaletteNeverUsesBasicANSI(t *testing.T) {
	th := mustNew(t, config.ThemeConfig{})
	for _, s := range allStatuses {
		assertIndexed(t, s, th.Color(s))
	}
	assertIndexed(t, "accent", th.accentColor)
	assertIndexed(t, "muted", th.mutedColor)
}

func TestXtermRGB(t *testing.T) {
	tests := []struct {
		index   int
		r, g, b uint8
	}{
		{16, 0, 0, 0},
		{231, 255, 255, 255},
		{196, 255, 0, 0},
		{232, 8, 8, 8},
		{255, 238, 238, 238},
	}
	for _, tt := range tests {
		r, g, b := XtermRGB(tt.index)
		if r != tt.r || g != tt.g || b != tt.b {
			t.Errorf("XtermRGB(%d) = (%d,%d,%d), want (%d,%d,%d)", tt.index, r, g, b, tt.r, tt.g, tt.b)
		}
	}
}

func TestContrastFG(t *testing.T) {
	for index, want := range map[int]int{214: 16, 18: 231, 244: 16} {
		if got := ContrastFG(index); got != want {
			t.Errorf("ContrastFG(%d) = %d, want %d", index, got, want)
		}
	}
}

func TestOverrideLabelAndColor(t *testing.T) {
	th := mustNew(t, config.ThemeConfig{Status: map[string]config.StatusStyle{
		"CI_WAITING": {Label: "CI…", Color: "220"},
	}})
	if got := th.Label("CI_WAITING"); got != "CI…" {
		t.Errorf("Label = %q, want %q", got, "CI…")
	}
	if got := assertIndexed(t, "CI_WAITING", th.Color("CI_WAITING")); got != 220 {
		t.Errorf("color index = %d, want 220", got)
	}
	// An override that omits fields keeps the defaults for them.
	if _, ok := th.Spinner("CI_WAITING"); !ok {
		t.Error("CI_WAITING lost its default spinner")
	}
	if got := th.Label("CI_FAILED"); got != "CI failed" {
		t.Errorf("untouched status label = %q", got)
	}
}

func TestHexColorOverride(t *testing.T) {
	th := mustNew(t, config.ThemeConfig{Status: map[string]config.StatusStyle{
		"CI_WAITING": {Color: "#E5A50A"},
	}})
	got, ok := th.Color("CI_WAITING").(color.RGBA)
	if !ok {
		t.Fatalf("color is %T, want color.RGBA", th.Color("CI_WAITING"))
	}
	if want := (color.RGBA{0xE5, 0xA5, 0x0A, 0xFF}); got != want {
		t.Errorf("color = %v, want %v", got, want)
	}
}

func TestNoColorEnv(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	th := mustNew(t, config.ThemeConfig{})
	for _, s := range allStatuses {
		chip := th.Chip(s)
		if strings.Contains(chip, "\x1b[") {
			t.Errorf("Chip(%s) contains an escape sequence: %q", s, chip)
		}
		if !strings.Contains(chip, th.Label(s)) {
			t.Errorf("Chip(%s) = %q lacks its label", s, chip)
		}
	}
}

func TestChipSetsForegroundAndBackground(t *testing.T) {
	th := mustNew(t, config.ThemeConfig{})
	chip := th.Chip("CI_WAITING")
	// 214 is light, so black (16) text on a 214 background.
	if !strings.Contains(chip, "38;5;16") || !strings.Contains(chip, "48;5;214") {
		t.Errorf("Chip = %q, want fg 16 and bg 214", chip)
	}
	if !strings.Contains(chip, " Waiting on CI ") {
		t.Errorf("Chip = %q lacks padded label", chip)
	}
}

func TestApprovedByYouIsOutlined(t *testing.T) {
	th := mustNew(t, config.ThemeConfig{})
	chip := th.Chip("APPROVED_BY_YOU")
	if strings.Contains(chip, "48;5;") {
		t.Errorf("outlined chip has a background: %q", chip)
	}
	if !strings.Contains(chip, "38;5;41") || !strings.Contains(chip, "╭") {
		t.Errorf("outlined chip = %q, want a rounded border in color 41", chip)
	}
}

func TestChipLineIsOneLine(t *testing.T) {
	th := mustNew(t, config.ThemeConfig{})
	if got, want := th.ChipLine("CI_WAITING"), th.Chip("CI_WAITING"); got != want {
		t.Errorf("filled ChipLine = %q, want the chip %q", got, want)
	}
	chip := th.ChipLine("APPROVED_BY_YOU")
	if strings.Contains(chip, "\n") || strings.ContainsAny(chip, "│╭╰") {
		t.Errorf("outlined ChipLine = %q, want one line without the box", chip)
	}
	if strings.Contains(chip, "48;5;") || !strings.Contains(chip, "38;5;41") {
		t.Errorf("outlined ChipLine = %q, want color 41 text and no background", chip)
	}
	if got := ansi.Strip(chip); got != "[Approved by you · needs more]" {
		t.Errorf("outlined ChipLine text = %q, want the label in brackets", got)
	}

	t.Setenv("NO_COLOR", "1")
	plain := mustNew(t, config.ThemeConfig{})
	if got := plain.ChipLine("APPROVED_BY_YOU"); got != "[Approved by you · needs more]" {
		t.Errorf("NO_COLOR outlined ChipLine = %q", got)
	}
	if got := plain.ChipLine("nope"); got != "" {
		t.Errorf("ChipLine of an unknown status = %q, want empty", got)
	}
}

func TestSpinner(t *testing.T) {
	th := mustNew(t, config.ThemeConfig{})
	if _, ok := th.Spinner("CI_WAITING"); !ok {
		t.Error("CI_WAITING should spin")
	}
	if _, ok := th.Spinner("CI_FAILED"); ok {
		t.Error("CI_FAILED should not spin")
	}
}

func TestNewRejectsBadColor(t *testing.T) {
	cfgs := []config.ThemeConfig{
		{Status: map[string]config.StatusStyle{"CI_WAITING": {Color: "banana"}}},
		{Chrome: config.Chrome{Accent: "5"}},
	}
	for _, cfg := range cfgs {
		if _, err := New(cfg); err == nil {
			t.Errorf("New(%+v) succeeded, want an error", cfg)
		}
	}
}

func TestBannerFillsWithTheIndexAndContrastingText(t *testing.T) {
	th := mustNew(t, config.ThemeConfig{})
	got := th.Banner(203, "REQUEST CHANGES")
	if !strings.Contains(got, "REQUEST CHANGES") || !strings.Contains(got, "48;5;203") {
		t.Errorf("Banner = %q, want text on background 203", got)
	}
	t.Setenv("NO_COLOR", "1")
	if got := mustNew(t, config.ThemeConfig{}).Banner(203, "X"); got != " X " {
		t.Errorf("NO_COLOR Banner = %q, want plain text", got)
	}
}

func TestThemeMarkdownDefaultsDark(t *testing.T) {
	if got := mustNew(t, config.ThemeConfig{}).Markdown(); got != "dark" {
		t.Errorf("empty Markdown() = %q, want dark", got)
	}
	if got := mustNew(t, config.ThemeConfig{Markdown: "light"}).Markdown(); got != "light" {
		t.Errorf("light Markdown() = %q, want light", got)
	}
}
