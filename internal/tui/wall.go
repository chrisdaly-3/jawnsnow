package tui

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/harmonica"
	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/status"
	"github.com/chrisdaly-3/jawnsnow/internal/theme"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
)

const (
	laneCount = 5 // CI, Ready for you, New changes, Waiting on author, Approved

	// wideMin is the narrowest terminal that fits the lanes side by side.
	wideMin = 120
	laneGap = 1

	cardLines  = 4
	cardStride = cardLines + 1 // a card and the blank line after it

	// scrollTick drives the narrow-mode scroll spring at about 60 frames
	// a second.
	scrollTick = 16 * time.Millisecond
	// settleEps is how close to its target, and how slow, the spring must be
	// to count as settled.
	settleEps = 0.01
)

var laneTitles = [laneCount]string{"CI", "Ready for you", "New changes", "Waiting on author", "Approved"}

// wallTickMsg advances the narrow-mode scroll spring one frame.
type wallTickMsg struct{}

// wallModel is the Wall view: open PRs as cards in status lanes. It holds
// only the cursor, the filters and the scroll; the PRs come from the root
// model on every call.
type wallModel struct {
	// The cursor is a lane and a row in it. selID remembers which PR that
	// is, so the cursor stays on it when a poll reorders the lanes.
	lane, row int
	selID     string

	// scroll is each lane's first visible card, in wide mode.
	scroll [laneCount]int

	// pos eases to target, a line offset, in narrow mode.
	pos, vel, target float64
	animating        bool
	spring           harmonica.Spring

	input   textinput.Model
	typing  bool
	author  string
	picking bool
	pick    []string
	pickIdx int

	w, h  int
	now   time.Time
	feeds []string // feed names, for the empty state
}

func newWall() wallModel {
	in := textinput.New()
	in.Prompt = "/ "
	in.Placeholder = "title, repo, number or author"
	return wallModel{
		spring: harmonica.NewSpring(harmonica.FPS(60), 6.0, 1.0),
		input:  in, w: 160, h: 30,
	}
}

// setSize tells the wall how much room its next view gets, so scrolling
// keeps the selected card on screen.
func (m *wallModel) setSize(w, h int) { m.w, m.h = w, h }

// capturing reports whether the filter box or the author picker is open.
// While it is, every key belongs to the wall.
func (m wallModel) capturing() bool { return m.typing || m.picking }

func (m wallModel) narrow() bool { return m.w < wideMin }

func (m wallModel) filterText() string { return strings.TrimSpace(m.input.Value()) }

func (m wallModel) filtering() bool { return m.typing || m.filterText() != "" || m.author != "" }

// viewportH is the height left for the lanes after the filter line.
func (m wallModel) viewportH() int {
	if m.filtering() {
		return m.h - 1
	}
	return m.h
}

// onWall is the PRs the wall shows before any filter.
func onWall(prs []tracker.Tracked) []tracker.Tracked {
	return slices.DeleteFunc(slices.Clone(prs), func(t tracker.Tracked) bool {
		return t.Place != tracker.PlaceWall || t.Status.Status.Lane() >= laneCount
	})
}

func (m wallModel) matches(t tracker.Tracked) bool {
	if m.author != "" && !strings.EqualFold(t.PR.Author, m.author) {
		return false
	}
	q := strings.ToLower(m.filterText())
	if q == "" {
		return true
	}
	hay := strings.ToLower(fmt.Sprintf("%s %s#%d %d %s", t.PR.Title, t.PR.Repo, t.PR.Number, t.PR.Number, t.PR.Author))
	return strings.Contains(hay, q)
}

// lanes groups the visible PRs by lane, newest head first.
func (m wallModel) lanes(prs []tracker.Tracked) [laneCount][]tracker.Tracked {
	var out [laneCount][]tracker.Tracked
	for _, t := range onWall(prs) {
		if m.matches(t) {
			l := t.Status.Status.Lane()
			out[l] = append(out[l], t)
		}
	}
	for l := range out {
		slices.SortStableFunc(out[l], func(a, b tracker.Tracked) int {
			if c := b.HeadSeenAt.Compare(a.HeadSeenAt); c != 0 {
				return c
			}
			return strings.Compare(a.PR.ID, b.PR.ID)
		})
	}
	return out
}

// resolve finds the cursor in lanes: on selID if that PR is still shown,
// else at the old position pulled back inside the lanes.
func (m wallModel) resolve(lanes [laneCount][]tracker.Tracked) (lane, row int) {
	if m.selID != "" {
		for l := range lanes {
			for r, t := range lanes[l] {
				if t.PR.ID == m.selID {
					return l, r
				}
			}
		}
	}
	lane = nearestFilled(lanes, min(max(m.lane, 0), laneCount-1))
	return lane, max(min(m.row, len(lanes[lane])-1), 0)
}

// nearestFilled is lane if it has cards, else the closest lane after it
// that does, else the closest before it. With no cards it is lane.
func nearestFilled(lanes [laneCount][]tracker.Tracked, lane int) int {
	for l := lane; l < laneCount; l++ {
		if len(lanes[l]) > 0 {
			return l
		}
	}
	for l := lane - 1; l >= 0; l-- {
		if len(lanes[l]) > 0 {
			return l
		}
	}
	return lane
}

// position is the cursor's lane and row among prs.
func (m wallModel) position(prs []tracker.Tracked) (lane, row int) {
	return m.resolve(m.lanes(prs))
}

func (m wallModel) selected(lanes [laneCount][]tracker.Tracked) (tracker.Tracked, bool) {
	l, r := m.resolve(lanes)
	if r < len(lanes[l]) {
		return lanes[l][r], true
	}
	return tracker.Tracked{}, false
}

// cell is a card's place in the narrow column.
type cell struct{ lane, row int }

// flat lists every card in narrow order.
func flat(lanes [laneCount][]tracker.Tracked) []cell {
	var out []cell
	for l := range lanes {
		for r := range lanes[l] {
			out = append(out, cell{l, r})
		}
	}
	return out
}

func (m wallModel) update(msg tea.Msg, prs []tracker.Tracked) (wallModel, tea.Cmd, action) {
	switch msg := msg.(type) {
	case wallTickMsg:
		m = m.stepSpring()
		return m, m.springCmd(), actionNone{}
	case tea.KeyPressMsg:
		return m.key(msg, prs)
	case tea.PasteMsg:
		if m.typing {
			m.input, _ = m.input.Update(msg)
		}
	}
	return m, nil, actionNone{}
}

func (m wallModel) key(msg tea.KeyPressMsg, prs []tracker.Tracked) (wallModel, tea.Cmd, action) {
	switch {
	case m.typing:
		return m.typingKey(msg, prs)
	case m.picking:
		return m.pickerKey(msg, prs)
	}
	lanes := m.lanes(prs)
	lane, row := m.resolve(lanes)
	k := msg.String()
	switch k {
	case "h", "left", "l", "right", "j", "down", "k", "up":
		if m.narrow() {
			lane, row = narrowMove(lanes, lane, row, k)
		} else {
			lane, row = wideMove(lanes, lane, row, k)
		}
		m, cmd := m.moveTo(lanes, lane, row)
		return m, cmd, actionNone{}
	case "x":
		if t, ok := m.selected(lanes); ok {
			return m, nil, actionHide{ID: t.PR.ID}
		}
	case "o":
		if t, ok := m.selected(lanes); ok {
			return m, nil, actionOpen{URL: t.PR.URL}
		}
	case "/":
		m.typing = true
		m.input.Focus() // the blink command is dropped; the cursor stays solid
	case "a":
		if authors := authorsOn(prs); len(authors) > 0 {
			m.picking, m.pick = true, authors
			m.pickIdx = max(slices.IndexFunc(authors, func(a string) bool { return strings.EqualFold(a, m.author) }), 0)
		}
	case "esc":
		m.input.Reset()
		m.author = ""
		m, cmd := m.moveTo(m.lanes(prs), lane, row)
		return m, cmd, actionNone{}
	}
	return m, nil, actionNone{}
}

func (m wallModel) typingKey(msg tea.KeyPressMsg, prs []tracker.Tracked) (wallModel, tea.Cmd, action) {
	switch msg.String() {
	case "esc":
		m.input.Reset()
		m.input.Blur()
		m.typing = false
	case "enter":
		m.input.Blur()
		m.typing = false
	default:
		m.input, _ = m.input.Update(msg)
	}
	m, cmd := m.keepInView(prs)
	return m, cmd, actionNone{}
}

func (m wallModel) pickerKey(msg tea.KeyPressMsg, prs []tracker.Tracked) (wallModel, tea.Cmd, action) {
	switch msg.String() {
	case "j", "down":
		m.pickIdx = min(m.pickIdx+1, len(m.pick)-1)
	case "k", "up":
		m.pickIdx = max(m.pickIdx-1, 0)
	case "enter":
		m.author = m.pick[m.pickIdx]
		m.picking = false
	case "esc":
		m.author = ""
		m.picking = false
	}
	m, cmd := m.keepInView(prs)
	return m, cmd, actionNone{}
}

// keepInView moves the cursor onto a card that is still shown after the
// filters changed.
func (m wallModel) keepInView(prs []tracker.Tracked) (wallModel, tea.Cmd) {
	lanes := m.lanes(prs)
	lane, row := m.resolve(lanes)
	return m.moveTo(lanes, lane, row)
}

// authorsOn lists the authors with a PR on the wall, sorted.
func authorsOn(prs []tracker.Tracked) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range onWall(prs) {
		if a := t.PR.Author; a != "" && !seen[strings.ToLower(a)] {
			seen[strings.ToLower(a)] = true
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return out
}

// wideMove moves between lanes with h and l, and between a lane's cards
// with j and k. It stops at the edges, and h and l skip empty lanes so the
// cursor always rests on a card.
func wideMove(lanes [laneCount][]tracker.Tracked, lane, row int, k string) (int, int) {
	switch k {
	case "h", "left":
		for l := lane - 1; l >= 0; l-- {
			if len(lanes[l]) > 0 {
				lane = l
				break
			}
		}
	case "l", "right":
		for l := lane + 1; l < laneCount; l++ {
			if len(lanes[l]) > 0 {
				lane = l
				break
			}
		}
	case "j", "down":
		row++
	case "k", "up":
		row--
	}
	return lane, max(min(row, len(lanes[lane])-1), 0)
}

// narrowMove walks every card in order with j and k, and jumps between lane
// headings with h and l. Empty lanes have no heading, so it skips them.
func narrowMove(lanes [laneCount][]tracker.Tracked, lane, row int, k string) (int, int) {
	cells := flat(lanes)
	if len(cells) == 0 {
		return lane, row
	}
	i := slices.IndexFunc(cells, func(c cell) bool { return c.lane == lane && c.row == row })
	if i < 0 { // the cursor is in an empty lane: start from the next card
		i = slices.IndexFunc(cells, func(c cell) bool { return c.lane > lane })
		if i < 0 {
			i = len(cells) - 1
		}
		return cells[i].lane, cells[i].row
	}
	switch k {
	case "j", "down":
		i = min(i+1, len(cells)-1)
	case "k", "up":
		i = max(i-1, 0)
	case "h", "left":
		if row > 0 {
			i -= row // back to this lane's heading
		} else if p := headingBefore(cells, lane); p >= 0 {
			i = p
		}
	case "l", "right":
		if n := slices.IndexFunc(cells, func(c cell) bool { return c.lane > lane && c.row == 0 }); n >= 0 {
			i = n
		}
	}
	return cells[i].lane, cells[i].row
}

// headingBefore is the index of the first card of the nearest non-empty
// lane above lane, or -1.
func headingBefore(cells []cell, lane int) int {
	best := -1
	for i, c := range cells {
		if c.lane < lane && c.row == 0 {
			best = i
		}
	}
	return best
}

// moveTo puts the cursor on (lane, row), scrolls to keep it visible and
// starts the spring if the narrow column has to move.
func (m wallModel) moveTo(lanes [laneCount][]tracker.Tracked, lane, row int) (wallModel, tea.Cmd) {
	m.lane, m.row, m.selID = lane, row, ""
	if row < len(lanes[lane]) {
		m.selID = lanes[lane][row].PR.ID
	}
	if !m.narrow() {
		m.scroll[lane] = wideScroll(m.scroll[lane], row, len(lanes[lane]), m.laneCapacity())
		return m, nil
	}
	items, total := narrowLayout(lanes)
	target := scrollTarget(items, total, lane, row, m.target, m.viewportH())
	if target == m.target {
		return m, nil
	}
	m.target = target
	if m.animating {
		return m, nil // the running spring chases the new target
	}
	m.animating = true
	return m, m.springCmd()
}

func (m wallModel) springCmd() tea.Cmd {
	if !m.animating {
		return nil
	}
	return tea.Tick(scrollTick, func(time.Time) tea.Msg { return wallTickMsg{} })
}

func (m wallModel) stepSpring() wallModel {
	if !m.animating {
		return m
	}
	m.pos, m.vel = m.spring.Update(m.pos, m.vel, m.target)
	if math.Abs(m.pos-m.target) < settleEps && math.Abs(m.vel) < settleEps {
		m.pos, m.vel, m.animating = m.target, 0, false
	}
	return m
}

// laneCapacity is how many whole cards fit in a wide lane's box.
func (m wallModel) laneCapacity() int {
	return max((m.viewportH()-2+1)/cardStride, 1)
}

// wideScroll gives the first visible card that keeps row on screen,
// moving as little as it can from first.
func wideScroll(first, row, count, capacity int) int {
	if row < first {
		first = row
	}
	if row >= first+capacity {
		first = row - capacity + 1
	}
	return max(min(first, count-capacity), 0)
}

// item is one heading or card of the narrow column. row is -1 for a heading.
type item struct{ lane, row, top int }

// narrowLayout places each non-empty lane's heading and cards in one
// column. total is the line after the last card.
func narrowLayout(lanes [laneCount][]tracker.Tracked) (items []item, total int) {
	y := 0
	for l := range lanes {
		if len(lanes[l]) == 0 {
			continue
		}
		items = append(items, item{l, -1, y})
		y++
		for r := range lanes[l] {
			items = append(items, item{l, r, y})
			y += cardStride
		}
	}
	return items, max(y-1, 0)
}

// scrollTarget is the offset nearest cur that shows the card at (lane, row),
// and its heading when it is the lane's first card.
func scrollTarget(items []item, total, lane, row int, cur float64, vh int) float64 {
	if vh <= 0 {
		return 0
	}
	top, bottom := -1, -1
	for _, it := range items {
		switch {
		case it.lane == lane && it.row == row:
			top, bottom = it.top, it.top+cardLines
		case it.lane == lane && it.row == -1 && row == 0:
			top = it.top
		}
	}
	if top < 0 {
		return min(max(cur, 0), float64(max(total-vh, 0)))
	}
	switch {
	case float64(top) < cur:
		cur = float64(top)
	case float64(bottom) > cur+float64(vh):
		cur = float64(bottom - vh)
	}
	return min(max(cur, 0), float64(max(total-vh, 0)))
}

// ---- rendering ----

func (m wallModel) view(width, height int, prs []tracker.Tracked, th theme.Theme, frame int) string {
	m.w, m.h = width, height
	if m.picking {
		return strings.Join(fit(m.pickerView(width, height, th), width, height), "\n")
	}
	lanes := m.lanes(prs)
	footer := m.footerLine(th)
	bh := m.viewportH()

	var body []string
	switch {
	case len(onWall(prs)) == 0:
		body = fit(centered(th, width, bh, "Nothing on the wall. Feeds: "+feedNames(m.feeds)+" · press R to poll"), width, bh)
	case len(flat(lanes)) == 0:
		body = fit(centered(th, width, bh, "No PRs match the filter."), width, bh)
	case m.narrow():
		body = m.narrowView(lanes, width, bh, th, frame)
	default:
		body = m.wideView(lanes, width, bh, th, frame)
	}
	if footer != "" {
		body = append(body, ansi.Truncate(footer, max(width, 0), "…"))
	}
	return strings.Join(fit(strings.Join(body, "\n"), width, height), "\n")
}

func centered(th theme.Theme, width, height int, text string) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, th.Muted().Render(ansi.Truncate(text, width, "…")))
}

func feedNames(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

func (m wallModel) footerLine(th theme.Theme) string {
	if !m.filtering() {
		return ""
	}
	var parts []string
	switch {
	case m.typing:
		parts = append(parts, m.input.View())
	case m.filterText() != "":
		parts = append(parts, th.Accent().Render("filter: "+clean(m.filterText())))
	}
	if m.author != "" {
		parts = append(parts, th.Accent().Render("author: "+clean(m.author)))
	}
	if !m.typing {
		parts = append(parts, th.Muted().Render("esc clears"))
	}
	return " " + strings.Join(parts, th.Muted().Render(" · "))
}

func (m wallModel) pickerView(width, height int, th theme.Theme) string {
	var b strings.Builder
	b.WriteString(th.Accent().Render("Filter by author"))
	for i, a := range m.pick {
		mark := "  "
		if i == m.pickIdx {
			mark = th.Accent().Render("▸ ")
		}
		b.WriteString("\n" + mark + clean(a))
	}
	b.WriteString("\n" + th.Muted().Render("j/k move · enter pick · esc clear"))
	box := th.Border(true).Padding(0, 2).Render(b.String())
	return lipgloss.Place(max(width, 0), max(height, 0), lipgloss.Center, lipgloss.Center, box)
}

func (m wallModel) wideView(lanes [laneCount][]tracker.Tracked, width, height int, th theme.Theme, frame int) []string {
	lane, row := m.resolve(lanes)
	inner := width - laneGap*(laneCount-1)
	cols := make([][]string, laneCount)
	for l := range lanes {
		w := inner / laneCount
		if l < inner%laneCount {
			w++
		}
		first := m.scroll[l]
		if l == lane {
			first = wideScroll(first, row, len(lanes[l]), m.laneCapacity())
		}
		first = max(min(first, len(lanes[l])-m.laneCapacity()), 0)
		cols[l] = m.laneBox(lanes[l], l, w, max(height, 2), l == lane, row, first, th, frame)
	}
	out := make([]string, max(height, 2))
	for i := range out {
		parts := make([]string, laneCount)
		for l := range cols {
			parts[l] = cols[l][i]
		}
		out[i] = strings.Join(parts, strings.Repeat(" ", laneGap))
	}
	return out
}

// laneBox draws one lane as a bordered box exactly w wide and h tall.
func (m wallModel) laneBox(cards []tracker.Tracked, lane, w, h int, focused bool, row, first int, th theme.Theme, frame int) []string {
	border := lipgloss.NewStyle().Foreground(th.Border(focused).GetBorderTopForeground())
	innerW := max(w-2, 0)
	label := laneLabel(lane, cards, th, frame, focused)
	fill := max(w-1-(2+lipgloss.Width(label)+1), 0)
	top := ansi.Truncate(border.Render("╭ ")+label+border.Render(" "+strings.Repeat("─", fill)), max(w-1, 0), "…")
	top += strings.Repeat(" ", max(w-1-ansi.StringWidth(top), 0)) + border.Render("╮")

	var content []string
	for r := first; r < len(cards); r++ {
		content = append(content, renderCard(cards[r], innerW, focused && r == row, th, m.now, frame)...)
		content = append(content, "")
	}
	out := []string{top}
	for i := range h - 2 {
		line := ""
		if i < len(content) {
			line = content[i]
		}
		out = append(out, border.Render("│")+padTo(line, innerW)+border.Render("│"))
	}
	return append(out, border.Render("╰"+strings.Repeat("─", max(w-2, 0))+"╯"))
}

// laneLabel is a lane's title, a spinner when any card in it is spinning,
// and its count.
func laneLabel(lane int, cards []tracker.Tracked, th theme.Theme, frame int, focused bool) string {
	title := th.Muted()
	if focused {
		title = th.Accent()
	}
	s := title.Render(laneTitles[lane])
	if i := slices.IndexFunc(cards, func(t tracker.Tracked) bool { return t.Status.Spinning }); i >= 0 {
		if g := spinGlyph(th, cards[i], frame); g != "" {
			s += " " + th.Accent().Render(g)
		}
	}
	return s + " " + th.Muted().Render(strconv.Itoa(len(cards)))
}

func (m wallModel) narrowView(lanes [laneCount][]tracker.Tracked, width, height int, th theme.Theme, frame int) []string {
	lane, row := m.resolve(lanes)
	items, total := narrowLayout(lanes)
	var lines []string
	for _, it := range items {
		if it.row < 0 {
			lines = append(lines, narrowHeading(it.lane, lanes[it.lane], width, th, frame))
			continue
		}
		lines = append(lines, renderCard(lanes[it.lane][it.row], width, it.lane == lane && it.row == row, th, m.now, frame)...)
		lines = append(lines, "")
	}
	off := m.pos
	if !m.animating {
		off = scrollTarget(items, total, lane, row, m.pos, height)
	}
	start := min(max(int(math.Round(off)), 0), max(len(lines)-height, 0))
	return lines[start:min(start+max(height, 0), len(lines))]
}

func narrowHeading(lane int, cards []tracker.Tracked, width int, th theme.Theme, frame int) string {
	label := laneLabel(lane, cards, th, frame, true)
	line := th.Muted().Render("── ") + label + " "
	fill := max(width-lipgloss.Width(line), 0)
	return ansi.Truncate(line+th.Muted().Render(strings.Repeat("─", fill)), max(width, 0), "")
}

// renderCard draws one PR as cardLines lines, each exactly width cells
// wide: the repo and number, the title, who and when, and the status chip.
func renderCard(t tracker.Tracked, width int, selected bool, th theme.Theme, now time.Time, frame int) []string {
	st := string(t.Status.Status)
	marker := " "
	if selected {
		marker = th.Accent().Render("▌")
	}
	glyph := " "
	switch {
	case t.Status.Spinning && spinGlyph(th, t, frame) != "":
		glyph = th.StatusFg(st).Render(spinGlyph(th, t, frame))
	case t.Status.Status == status.CIFailed:
		glyph = th.StatusFg(st).Render("✗")
	}
	textW := max(width-3, 0)
	pr := t.PR

	ref := clean(repoShort(pr.Repo)) + "#" + strconv.Itoa(pr.Number)
	meta := th.Muted().Render(clean(pr.Author)+" · "+cardAge(t, now)+" · ") +
		th.StatusFg(string(status.Approved)).Render("+"+strconv.Itoa(pr.Additions)) + " " +
		th.StatusFg(string(status.CIFailed)).Render("−"+strconv.Itoa(pr.Deletions))

	lines := []string{
		marker + glyph + " " + th.Accent().Render(ansi.Truncate(ref, textW, "…")),
		marker + "  " + ansi.Truncate(clean(pr.Title), textW, "…"),
		marker + "  " + ansi.Truncate(meta, textW, "…"),
		marker + "  " + ansi.Truncate(chipLine(th, st), textW, "…"),
	}
	for i := range lines {
		lines[i] = padTo(lines[i], width)
	}
	return lines
}

// spinGlyph is the frame of the card's own status spinner, or "" when the
// card isn't spinning or its status has the "none" spinner.
func spinGlyph(th theme.Theme, t tracker.Tracked, frame int) string {
	if !t.Status.Spinning {
		return ""
	}
	sp, ok := th.Spinner(string(t.Status.Status))
	if !ok || len(sp.Frames) == 0 {
		return ""
	}
	return strings.TrimSpace(sp.Frames[frame%len(sp.Frames)])
}

// chipLine is the status chip on one line. An outlined chip is three lines
// tall; its middle line holds the label.
func chipLine(th theme.Theme, st string) string {
	lines := strings.Split(th.Chip(st), "\n")
	return lines[len(lines)/2]
}

// padTo cuts or pads s to exactly w cells.
func padTo(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "")
	return s + strings.Repeat(" ", w-ansi.StringWidth(s))
}

func repoShort(repo string) string {
	_, name, ok := strings.Cut(repo, "/")
	if !ok {
		return repo
	}
	return name
}

// cardAge is how long ago the PR's current head was first seen.
func cardAge(t tracker.Tracked, now time.Time) string {
	since := t.HeadSeenAt
	if since.IsZero() {
		since = t.FirstSeen
	}
	if since.IsZero() {
		return "–"
	}
	d := max(now.Sub(since), 0)
	switch {
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	}
	return strconv.Itoa(int(d/(24*time.Hour))) + "d"
}

// clean makes GitHub-supplied text safe to print: escape sequences are
// dropped and other control characters become spaces, so a title can't
// move the cursor or retitle the terminal.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, ansi.Strip(s))
}
