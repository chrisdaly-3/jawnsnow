package tui

import (
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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

func TestStepSelection(t *testing.T) {
	// A 20-line view over 60 lines of content (max scroll 40): a short
	// item, a 30-line item, and a short last item followed by 10 lines.
	spans := []itemSpan{{2, 3, 5}, {6, 7, 36}, {37, 38, 50}}
	const bodyH, maxScroll = 20, 40
	cases := []struct {
		name                string
		sel, scroll, dir    int
		wantSel, wantScroll int
	}{
		{"j moves to the next item and brings it in", 0, 0, 1, 1, 3},
		{"j inside a tall item scrolls through it", 1, 3, 1, 1, 4},
		{"j at a tall item's end moves on", 1, 19, 1, 2, 33},
		{"j on the last item scrolls the rest", 2, 33, 1, 2, 34},
		{"k on a short item moves back", 2, 33, -1, 1, 3},
		{"k inside a tall item scrolls back through it", 1, 10, -1, 1, 9},
		{"k on the first item scrolls to the header", 0, 1, -1, 0, 0},
	}
	for _, c := range cases {
		sel, scroll := stepSelection(c.sel, c.scroll, spans, bodyH, maxScroll, c.dir)
		if sel != c.wantSel || scroll != c.wantScroll {
			t.Errorf("%s: got sel %d scroll %d, want %d and %d", c.name, sel, scroll, c.wantSel, c.wantScroll)
		}
	}
	if sel, scroll := stepSelection(0, 4, nil, bodyH, maxScroll, 1); sel != 0 || scroll != 5 {
		t.Errorf("no items: j gave sel %d scroll %d, want a plain scroll to 5", sel, scroll)
	}
}

func TestReviewTabJumpSelectsTheEnds(t *testing.T) {
	d, c := openDrawer(t, 160, 16, drawerReview())
	d, _, _ = d.update(key("J"), c)
	if d.sel != len(drawerReview().Findings)-1 {
		t.Errorf("J: sel %d, want the last finding", d.sel)
	}
	d, _, _ = d.update(key("K"), c)
	if d.sel != 0 {
		t.Errorf("K: sel %d, want the first finding", d.sel)
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
	for i := 0; d.sel < len(spans)-1 && i < 100; i++ {
		prev := d.sel
		d, _, _ = d.update(key("j"), c)
		if d.sel == prev {
			continue // j scrolled through a tall finding
		}
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
		// j keeps room above the finding rather than putting it on the
		// first line, unless the drawer is scrolled to its end.
		if d.scroll > 0 && d.scroll < d.maxScroll(c) && row < scrollMargin {
			t.Errorf("sel %d: header on row %d, want at least %d lines of room", d.sel, row, scrollMargin)
		}
	}
}

func TestWithDrawerDimsThePaneWithoutFocus(t *testing.T) {
	h := drawerHarness(t, opts{})
	h.m, _ = update(t, h.m, tea.WindowSizeMsg{Width: 120, Height: 30})
	h.press(t, "enter")
	settleDrawer(t, h)
	faint := "\x1b[2m"
	line := func() string { return strings.Split(h.m.withDrawer("wall", 60, 50, 3), "\n")[0] }

	// enter focuses the drawer, so the Wall's side is faint and the
	// drawer's isn't.
	if l := line(); !strings.HasPrefix(l, faint) || strings.Count(l, faint) != 1 {
		t.Errorf("drawer focused: want only the Wall faint, got %q", l)
	}
	h.press(t, "tab")
	l := line()
	if strings.HasPrefix(l, faint) {
		t.Errorf("Wall focused: the Wall is still faint: %q", l)
	}
	if !strings.Contains(l, "wall"+strings.Repeat(" ", 56)+faint) {
		t.Errorf("Wall focused: want the drawer faint, got %q", l)
	}
}

func TestDimWithNoColorIsUnchanged(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	h := drawerHarness(t, opts{})
	h.m, _ = update(t, h.m, tea.WindowSizeMsg{Width: 120, Height: 30})
	h.press(t, "enter")
	settleDrawer(t, h)
	if l := h.m.withDrawer("wall", 60, 50, 3); strings.Contains(l, "\x1b[2m") {
		t.Errorf("NO_COLOR: a pane is faint: %q", l)
	}
}

// Once the terminal reports its colours, the side without focus is veiled
// toward the background rather than faint.
func TestWithDrawerVeilsOnceTheTerminalReportsItsBackground(t *testing.T) {
	h := drawerHarness(t, opts{})
	h.m, _ = update(t, h.m, tea.WindowSizeMsg{Width: 120, Height: 30})
	h.m, _ = update(t, h.m, tea.BackgroundColorMsg{Color: color.RGBA{0x10, 0x10, 0x10, 0xff}})
	h.m, _ = update(t, h.m, tea.ForegroundColorMsg{Color: color.RGBA{0xe0, 0xe0, 0xe0, 0xff}})
	h.press(t, "enter")
	settleDrawer(t, h)
	// 0xe0 text 55% toward 0x10: 224×0.45 + 16×0.55 = 110.
	veiled := "\x1b[38;2;110;110;110m"
	line, _, _ := strings.Cut(h.m.withDrawer("wall", 60, 50, 3), "\n")
	wall, drawer := line[:strings.Index(line, "wall")+4], line[strings.Index(line, "wall")+4:]
	if !strings.HasPrefix(wall, veiled) {
		t.Errorf("drawer focused: the Wall isn't veiled: %q", wall)
	}
	if strings.Contains(line, "\x1b[2m") || strings.Contains(drawer, "38;2;110;110;110") {
		t.Errorf("drawer focused: want only the Wall veiled, not faint: %q", line)
	}
	h.press(t, "tab")
	line, _, _ = strings.Cut(h.m.withDrawer("wall", 60, 50, 3), "\n")
	if strings.HasPrefix(line, veiled) || !strings.Contains(line, "38;2;") {
		t.Errorf("Wall focused: want the drawer veiled and the Wall not: %q", line)
	}
}
