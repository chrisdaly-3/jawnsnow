package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestScrollToItem(t *testing.T) {
	// A 20-line view keeps 3 lines of room at each edge.
	cases := []struct {
		name   string
		scroll int
		s      itemSpan
		want   int
	}{
		{"already in view", 0, itemSpan{5, 6, 10}, 0},
		{"below: its end comes 3 lines above the bottom", 0, itemSpan{20, 21, 25}, 8},
		{"above: its start goes 3 lines below the top", 30, itemSpan{28, 29, 32}, 25},
		{"inside the top margin", 10, itemSpan{11, 12, 14}, 8},
		{"inside the bottom margin", 0, itemSpan{15, 16, 18}, 1},
		{"too tall: shown from its top", 0, itemSpan{10, 11, 40}, 7},
		{"near the start: never above 0", 5, itemSpan{1, 2, 3}, 0},
	}
	for _, c := range cases {
		if got := scrollToItem(c.scroll, c.s, 20); got != c.want {
			t.Errorf("%s: scrollToItem(%d, %+v, 20) = %d, want %d", c.name, c.scroll, c.s, got, c.want)
		}
	}
	// A short view shrinks the margin rather than fighting it.
	if got := scrollToItem(0, itemSpan{6, 7, 8}, 6); got != 3 {
		t.Errorf("6-line view: got %d, want 3 (margin 1)", got)
	}
}

func TestDrawerFillsTheSelectedFindingAndKeepsRoomAroundIt(t *testing.T) {
	const height = 16
	d, c := openDrawer(t, 160, height, drawerReview())
	_, spans := d.layout(c)
	if len(spans) < 3 {
		t.Fatalf("drawerReview has %d findings, want at least 3", len(spans))
	}
	fill := "48;5;237"
	for range len(spans) - 1 {
		d, _, _ = d.update(key("n"), c)
		out := strings.Split(renderDrawer(d, c, height), "\n")
		_, spans = d.layout(c)
		s := spans[d.sel]
		row := s.start - d.scroll
		if row < 0 || row >= len(out)-1 {
			t.Fatalf("sel %d: header row %d is off screen (scroll %d)", d.sel, row, d.scroll)
		}
		if !strings.Contains(out[row], fill) {
			t.Errorf("sel %d: header row isn't filled: %q", d.sel, ansi.Strip(out[row]))
		}
		for i, l := range out {
			if i != row && strings.Contains(l, fill) {
				t.Errorf("sel %d: row %d is filled too: %q", d.sel, i, ansi.Strip(l))
			}
		}
		// The old n put the finding on the first line; now it keeps room
		// above it unless the drawer is scrolled to its end.
		if d.scroll > 0 && d.scroll < d.maxScroll(c) && row < scrollMargin {
			t.Errorf("sel %d: header on row %d, want at least %d lines of room", d.sel, row, scrollMargin)
		}
	}
}
