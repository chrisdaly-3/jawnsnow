package tui

import (
	"testing"
)

// frames runs a jump to its end and returns the scroll after each frame.
func frames(t *testing.T, d drawerModel, c drawerCtx) (drawerModel, []int) {
	t.Helper()
	var seen []int
	for i := 0; d.jump.on; i++ {
		if i > 200 {
			t.Fatal("the jump never settled")
		}
		d, _, _ = d.update(drawerJumpMsg{}, c)
		seen = append(seen, d.tabScroll())
	}
	return d, seen
}

func TestDrawerJumpEasesToTheEnds(t *testing.T) {
	d, c := openDrawer(t, 160, 12, drawerReview()) // short: it has to scroll
	end := d.maxScroll(c)
	if end < 5 {
		t.Fatalf("setup: max scroll %d, want room to ease", end)
	}
	d, cmd, _ := d.update(key("J"), c)
	if cmd == nil || !d.jump.on {
		t.Fatal("J should start a jump")
	}
	d, seen := frames(t, d, c)
	if len(seen) < 3 {
		t.Errorf("J took %d frames, want it eased over several: %v", len(seen), seen)
	}
	if len(seen) > 20 {
		t.Errorf("J took %d frames, want it snappier than the slide: %v", len(seen), seen)
	}
	for i, s := range seen {
		if s > end || (i > 0 && s < seen[i-1]) {
			t.Fatalf("J went %v, want it to rise to %d without overshooting", seen, end)
		}
	}
	if d.scroll != end {
		t.Errorf("J ended at %d, want %d", d.scroll, end)
	}
	d, _, _ = d.update(key("K"), c)
	if d, _ = frames(t, d, c); d.scroll != 0 {
		t.Errorf("K ended at %d, want 0", d.scroll)
	}
	if _, cmd, _ := d.update(key("K"), c); cmd != nil {
		t.Error("K at the top should do nothing")
	}
}

func TestDrawerJumpInterrupted(t *testing.T) {
	d, c := openDrawer(t, 160, 12, drawerReview())
	end := d.maxScroll(c)
	d, _, _ = d.update(key("J"), c)
	d, _, _ = d.update(drawerJumpMsg{}, c)
	d, _, _ = d.update(drawerJumpMsg{}, c)
	mid := d.scroll
	if mid == 0 || mid == end {
		t.Fatalf("setup: after two frames at %d, want partway to %d", mid, end)
	}
	// K turns the running jump around.
	d, cmd, _ := d.update(key("K"), c)
	if cmd != nil || !d.jump.on || d.jump.target != 0 {
		t.Errorf("K mid-jump: cmd %v jump %+v, want the running jump aimed at 0", cmd != nil, d.jump)
	}
	// j stops it where it is and scrolls from there.
	d, _, _ = d.update(key("j"), c)
	if d.jump.on || d.scroll != mid+1 {
		t.Errorf("j mid-jump: scroll %d jumping %v, want %d and stopped", d.scroll, d.jump.on, mid+1)
	}
	if d, _, _ = d.update(drawerJumpMsg{}, c); d.scroll != mid+1 {
		t.Errorf("a frame after the stop moved the scroll to %d", d.scroll)
	}
	// Leaving the tab puts the jump at its end.
	d, _, _ = d.update(key("J"), c)
	d, _, _ = d.update(drawerJumpMsg{}, c)
	d, _, _ = d.update(key("l"), c)
	d, _, _ = d.update(key("h"), c)
	if d.jump.on || d.scroll != end {
		t.Errorf("l then h mid-jump: scroll %d jumping %v, want %d and done", d.scroll, d.jump.on, end)
	}
}

func TestChecksTabJumpSelectsTheEnds(t *testing.T) {
	d, c := checksDrawer(t, 8) // short: the checks scroll
	d, _, _ = d.update(key("s"), c)
	d, _, _ = d.update(key("J"), c)
	d, _ = frames(t, d, c)
	if n := len(orderedChecks(checkRows(c), true)); d.checkSel != n-1 {
		t.Errorf("J: sel %d, want the last check, %d", d.checkSel, n-1)
	}
	if d.checkScroll != d.maxScroll(c) || d.scroll != 0 {
		t.Errorf("J: checks scroll %d review scroll %d, want %d and 0", d.checkScroll, d.scroll, d.maxScroll(c))
	}
	d, _, _ = d.update(key("K"), c)
	d, _ = frames(t, d, c)
	if d.checkSel != 0 || d.checkScroll != 0 {
		t.Errorf("K: sel %d scroll %d, want the first check at the top", d.checkSel, d.checkScroll)
	}
}
