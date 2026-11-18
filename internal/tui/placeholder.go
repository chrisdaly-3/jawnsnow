package tui

import (
	"fmt"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/theme"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
)

// placeholder centers one muted line in a width×height body.
func placeholder(th theme.Theme, width, height int, text string) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, th.Muted().Render(ansi.Truncate(text, max(width, 0), "…")))
}

// wallPlaceholder stands in for the Wall until Task 11.
func wallPlaceholder(m Model, width, height int) string {
	return placeholder(m.theme, width, height, fmt.Sprintf("%d on the wall", m.count(tracker.PlaceWall)))
}

// donePlaceholder stands in for the Done view until Task 12.
func donePlaceholder(m Model, width, height int) string {
	return placeholder(m.theme, width, height,
		fmt.Sprintf("%d merged or closed · %d waiting", m.count(tracker.PlaceDone), m.count(tracker.PlaceWaiting)))
}

func (m Model) count(p tracker.Place) int {
	n := 0
	for _, t := range m.prs {
		if t.Place == p {
			n++
		}
	}
	return n
}
