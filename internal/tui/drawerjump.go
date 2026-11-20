package tui

import (
	"math"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/harmonica"
)

// drawerJumpMsg advances a J or K jump one frame.
type drawerJumpMsg struct{}

// jumpSpring eases J and K (A10): much snappier than the drawer's slide
// (6), and critically damped, so it never overshoots the end. A screen or
// two lands in about a fifth of a second.
var jumpSpring = harmonica.NewSpring(harmonica.FPS(60), 24.0, 1.0)

// jump is a J or K scroll in flight on the open tab. pos is the eased
// scroll; the tab's scroll is pos rounded.
type jump struct {
	pos, vel float64
	target   int
	on       bool
}

// tabScroll is the open tab's scroll.
func (m drawerModel) tabScroll() int {
	switch m.tab {
	case tabChecks:
		return m.checkScroll
	case tabActivity:
		return m.actScroll
	}
	return m.scroll
}

func (m drawerModel) withTabScroll(v int) drawerModel {
	switch m.tab {
	case tabChecks:
		m.checkScroll = v
	case tabActivity:
		m.actScroll = v
	default:
		m.scroll = v
	}
	return m
}

// jumpKey handles J (to the bottom) and K (to the top) on either tab. On the
// Checks tab it selects the last or first check too, so O and y act on one
// that is on screen.
func (m drawerModel) jumpKey(k string, c drawerCtx) (drawerModel, tea.Cmd, action) {
	target := 0
	if k == "J" {
		target = m.maxScroll(c)
	}
	switch m.tab {
	case tabChecks:
		m.checkSel = 0
		if n := len(orderedChecks(checkRows(c), m.showPassed)); k == "J" && n > 0 {
			m.checkSel = n - 1
		}
	case tabActivity:
		m.actSel = 0
		if n := len(shownActivity(activityView(c), m.showBots)); k == "J" && n > 0 && c.Activity.Loaded {
			m.actSel = n - 1
		}
	}
	m, cmd := m.startJump(target)
	return m, cmd, actionNone{}
}

// startJump eases the open tab's scroll to target. The first frame comes at
// once; a jump already running turns toward the new target.
func (m drawerModel) startJump(target int) (drawerModel, tea.Cmd) {
	if m.jump.on {
		m.jump.target = target
		return m, nil
	}
	if m.tabScroll() == target {
		return m, nil
	}
	m.jump = jump{pos: float64(m.tabScroll()), target: target, on: true}
	return m, func() tea.Msg { return drawerJumpMsg{} }
}

func (m drawerModel) stepJump() drawerModel {
	j := m.jump
	j.pos, j.vel = jumpSpring.Update(j.pos, j.vel, float64(j.target))
	// Done once the shown line is the target: the spring can't overshoot,
	// so the rest would only creep inside one line.
	if math.Abs(j.pos-float64(j.target)) < 0.5 {
		j.pos, j.vel, j.on = float64(j.target), 0, false
	}
	m.jump = j
	return m.withTabScroll(int(math.Round(j.pos)))
}

// jumpFrame takes one frame and asks for the next while the jump runs.
func (m drawerModel) jumpFrame(c drawerCtx) (drawerModel, tea.Cmd) {
	if !m.jump.on {
		return m, nil
	}
	m = m.stepJump()
	if !m.jump.on || c.Tick == nil {
		return m, nil
	}
	return m, c.Tick(scrollTick, func(time.Time) tea.Msg { return drawerJumpMsg{} })
}

// stopJump ends a jump where it is, for a key that scrolls on its own.
func (m drawerModel) stopJump() drawerModel {
	m.jump = jump{}
	return m
}

// finishJump puts a jump at its end, for a key that leaves the tab.
func (m drawerModel) finishJump() drawerModel {
	if m.jump.on {
		m = m.withTabScroll(m.jump.target)
	}
	return m.stopJump()
}
