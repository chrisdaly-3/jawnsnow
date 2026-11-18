package tui

import (
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/theme"
)

// placeholder centers one muted line in a width×height body.
func placeholder(th theme.Theme, width, height int, text string) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, th.Muted().Render(ansi.Truncate(text, max(width, 0), "…")))
}
