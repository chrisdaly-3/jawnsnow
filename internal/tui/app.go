// Package tui is snow's Bubble Tea interface: the root model that runs the
// poll loop, the status bar, and the views.
package tui

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/cli/go-gh/v2/pkg/browser"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/poll"
	"github.com/chrisdaly-3/jawnsnow/internal/status"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/theme"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
)

// View is one of the five top-level screens.
type View int

const (
	ViewWall View = iota
	ViewDone
	ViewReviews
	ViewCost
	ViewAgent
	viewCount
)

var viewNames = [viewCount]string{"Wall", "Done", "Reviews", "Cost", "Agent"}

func (v View) String() string { return viewNames[v] }

const (
	modeWatcher = "watcher"
	modeAuto    = "auto"

	// pollTimeout bounds one poll, so a hung request can't block polling
	// for good: only one poll runs at a time.
	pollTimeout   = 2 * time.Minute
	clockInterval = time.Second
)

// Deps is everything the root model needs from startup.
type Deps struct {
	Config  config.Config
	Theme   theme.Theme
	Client  github.Client // nil when gh isn't logged in; then snow never polls
	Store   *store.Store  // required
	Viewer  string
	Banners []string
	Reloads <-chan config.Reload
	Now     func() time.Time
}

type (
	pollResultMsg poll.Result
	reloadMsg     config.Reload
	clockMsg      time.Time
	spinTickMsg   struct{}

	// pollTickMsg asks for the scheduled poll. Only the tick carrying the
	// current pollGen polls; rescheduling makes older ticks stale.
	pollTickMsg struct{ gen int }
	loadedMsg   struct {
		prs map[string]tracker.Tracked
		err error
	}
	savedMsg  struct{ err error }
	openedMsg struct{ err error }
)

// An action is what a view asks the root model to do to a PR.
type action interface{ isAction() }

type (
	actionNone   struct{}
	actionHide   struct{ ID string }
	actionOpen   struct{ URL string }
	actionUnhide struct{ ID string }
)

func (actionNone) isAction()   {}
func (actionHide) isAction()   {}
func (actionOpen) isAction()   {}
func (actionUnhide) isAction() {}

// Model is the root Bubble Tea model.
type Model struct {
	cfg     config.Config
	theme   theme.Theme
	client  github.Client
	store   *store.Store
	viewer  string
	banners []string
	reloads <-chan config.Reload
	now     func() time.Time
	tick    func(time.Duration, func(time.Time) tea.Msg) tea.Cmd
	open    func(url string) error

	prs map[string]tracker.Tracked

	polling      bool
	pollStarted  time.Time
	pollIdle     bool // the in-flight poll fetches waiting PRs too
	pollGen      int
	lastPoll     time.Time
	lastIdlePoll time.Time
	lastErr      error
	quota        poll.Quota
	rateNote     string
	feedNote     string // the searches the last poll skipped, and why

	// One save runs at a time, so an older write can't land after a newer
	// one. Changes made meanwhile wait in dirty and deleted.
	saving  bool
	dirty   map[string]bool
	deleted map[string]bool

	configBanner string
	warnings     []string
	notice       string

	wall wallModel
	done doneModel
	// spinning is true while the spinner's tick loop runs; spinFrame is
	// the frame counter every spinning card indexes its own spinner with.
	spinning  bool
	spinFrame int

	view          View
	focus         int
	help          bool
	mode          string
	width, height int
}

// New builds the root model. Nothing runs until Init.
func New(d Deps) Model {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	mode := modeWatcher
	if d.Config.Mode.Default == modeAuto {
		mode = modeAuto
	}
	return Model{
		cfg: d.Config, theme: d.Theme, client: d.Client, store: d.Store, viewer: d.Viewer,
		banners: d.Banners, reloads: d.Reloads, now: now, tick: tea.Tick,
		open: func(url string) error { return browser.New("", io.Discard, io.Discard).Browse(url) },
		prs:  map[string]tracker.Tracked{}, wall: newWall(), done: newDone(), dirty: map[string]bool{}, deleted: map[string]bool{},
		mode: mode, width: 80, height: 24,
	}
}

// Init loads the stored PRs, whose arrival starts the first poll, and starts
// the clock and the config listener.
func (m Model) Init() tea.Cmd {
	st := m.store
	load := func() tea.Msg {
		prs, err := st.LoadTracked(context.Background())
		return loadedMsg{prs: prs, err: err}
	}
	return tea.Batch(load, m.clockTick(), m.listenReloads())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	next, spin := next.ensureSpinner()
	if spin != nil {
		cmd = tea.Batch(cmd, spin)
	}
	return next, cmd
}

// ensureSpinner starts the spinner's tick loop when something on the wall
// is spinning and it isn't running.
func (m Model) ensureSpinner() (Model, tea.Cmd) {
	if m.spinning || m.spinInterval() == 0 {
		return m, nil
	}
	m.spinning = true
	return m, m.spinTick()
}

func (m Model) spinTick() tea.Cmd {
	return m.tick(m.spinInterval(), func(time.Time) tea.Msg { return spinTickMsg{} })
}

// spinInterval is the tick period: the fastest spinner among the wall PRs
// that are spinning. It is 0 when none has a spinner to animate.
func (m Model) spinInterval() time.Duration {
	var fastest time.Duration
	for _, t := range m.prs {
		if t.Place != tracker.PlaceWall || !t.Status.Spinning {
			continue
		}
		if sp, ok := m.theme.Spinner(string(t.Status.Status)); ok && sp.FPS > 0 && (fastest == 0 || sp.FPS < fastest) {
			fastest = sp.FPS
		}
	}
	return fastest
}

func (m Model) update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case loadedMsg:
		if msg.err != nil {
			m.notice = "could not load saved PRs: " + msg.err.Error()
		} else {
			m.prs = msg.prs
		}
		return m.startPoll()
	case pollTickMsg:
		if msg.gen == m.pollGen {
			return m.startPoll()
		}
	case pollResultMsg:
		return m.finishPoll(poll.Result(msg))
	case savedMsg:
		m.saving = false
		if msg.err != nil {
			m.notice = "could not save PRs: " + msg.err.Error()
		}
		return m.flush()
	case openedMsg:
		if msg.err != nil {
			m.notice = "could not open the browser: " + msg.err.Error()
		}
	case reloadMsg:
		return m.reload(config.Reload(msg))
	case clockMsg:
		return m, m.clockTick()
	case spinTickMsg:
		if m.spinInterval() == 0 {
			m.spinning = false
			return m, nil
		}
		m.spinFrame++
		return m, m.spinTick()
	case wallTickMsg:
		return m.wallUpdate(msg)
	case tea.PasteMsg:
		if m.view == ViewWall && m.wall.capturing() {
			return m.wallUpdate(msg)
		}
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	// An open filter box or author picker takes every key, or typing "q"
	// would quit. ctrl+c still quits.
	if m.view == ViewWall && !m.help && m.wall.capturing() && msg.String() != "ctrl+c" {
		return m.updateView(msg)
	}
	switch k := msg.String(); k {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.help = !m.help
	case "esc":
		if !m.help {
			return m.updateView(msg)
		}
		m.help = false
	case "]":
		m = m.switchView((m.view + 1) % viewCount)
	case "[":
		m = m.switchView((m.view + viewCount - 1) % viewCount)
	case "1", "2", "3", "4", "5":
		m = m.switchView(View(k[0] - '1'))
	case "tab":
		m.focus = (m.focus + 1) % paneCount(m.view)
	case "shift+tab":
		n := paneCount(m.view)
		m.focus = (m.focus + n - 1) % n
	case "R":
		return m.startPoll()
	case "m":
		if m.mode == modeWatcher {
			m.mode = modeAuto
		} else {
			m.mode = modeWatcher
		}
	default:
		return m.updateView(msg)
	}
	return m, nil
}

// updateView passes a key the root model doesn't use to the current view.
func (m Model) updateView(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch m.view {
	case ViewWall:
		return m.wallUpdate(msg)
	case ViewDone:
		return m.doneUpdate(msg)
	}
	return m, nil
}

// doneUpdate gives msg to the Done view and applies the action it returns.
func (m Model) doneUpdate(msg tea.Msg) (Model, tea.Cmd) {
	w, h := m.bodySize()
	m.done.setSize(w, h)
	m.done.now, m.done.focus = m.now(), m.focus
	done, cmd, act := m.done.update(msg, m.wallPRs())
	m.done = done
	m, actCmd := m.apply(act)
	return m, tea.Batch(cmd, actCmd)
}

// wallUpdate gives msg to the Wall and applies the action it returns.
func (m Model) wallUpdate(msg tea.Msg) (Model, tea.Cmd) {
	w, h := m.bodySize()
	m.wall.setSize(w, h)
	m.wall.now, m.wall.feeds = m.now(), m.feedNames()
	wall, cmd, act := m.wall.update(msg, m.wallPRs())
	m.wall = wall
	m, actCmd := m.apply(act)
	return m, tea.Batch(cmd, actCmd)
}

// wallPRs is every tracked PR; the Wall picks out the ones on it.
func (m Model) wallPRs() []tracker.Tracked {
	return slices.Collect(maps.Values(m.prs))
}

func (m Model) feedNames() []string {
	names := make([]string, len(m.cfg.Feeds))
	for i, f := range m.cfg.Feeds {
		names[i] = f.Name
	}
	return names
}

// paneCount is the number of focusable panes in v: the Done view's two
// tables, and one for every other view until plan 2 adds the drawer beside
// the Wall.
func paneCount(v View) int {
	if v == ViewDone {
		return donePanes
	}
	return 1
}

func (m Model) switchView(v View) Model {
	m.view, m.focus = v, 0
	return m
}

// apply carries out a view's action against the current PRs.
func (m Model) apply(a action) (Model, tea.Cmd) {
	switch a := a.(type) {
	case actionHide:
		return m.updatePR(a.ID, tracker.Hide)
	case actionUnhide:
		return m.updatePR(a.ID, tracker.Unhide)
	case actionOpen:
		if a.URL == "" {
			return m, nil
		}
		open, url := m.open, a.URL
		return m, func() tea.Msg { return openedMsg{err: open(url)} }
	}
	return m, nil
}

func (m Model) updatePR(id string, change func(tracker.Tracked, time.Time) tracker.Tracked) (Model, tea.Cmd) {
	t, ok := m.prs[id]
	if !ok {
		return m, nil
	}
	m.prs = maps.Clone(m.prs)
	m.prs[id] = change(t, m.now())
	m = m.markSave(id)
	return m.flush()
}

// startPoll starts a poll unless one is in flight or there is no client.
// The command gets copies, so later changes to the model don't race it.
func (m Model) startPoll() (Model, tea.Cmd) {
	if m.client == nil || m.polling {
		return m, nil
	}
	now := m.now()
	idleDue := m.lastIdlePoll.IsZero() || now.Sub(m.lastIdlePoll) >= m.cfg.GitHub.IdlePollInterval.Duration
	known := make(map[string]github.Fingerprint, len(m.prs))
	var watch, force []string
	for id, t := range m.prs {
		known[id] = t.PR.Fingerprint()
		if !tracker.NeedsDetail(t, idleDue) {
			continue
		}
		watch = append(watch, id)
		// The rollup stays PENDING while an optional check runs, so it can't
		// tell when the required checks finish. Fetch these every poll.
		if t.Status.CI == status.CIPending {
			force = append(force, id)
		}
	}
	slices.Sort(watch)
	slices.Sort(force)
	req := poll.Request{Viewer: m.viewer, Feeds: slices.Clone(m.cfg.Feeds), Known: known, Watch: watch, DetailIDs: force, Now: now}
	client := m.client
	m.polling, m.pollStarted, m.pollIdle = true, now, idleDue
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
		defer cancel()
		return pollResultMsg(poll.Fetch(ctx, client, req))
	}
}

// finishPoll applies a poll's result to the current PRs, not the ones the
// poll started from, so a hide made during the poll survives it. A failed
// poll leaves the PRs alone; one GitHub refused for rate limiting backs off.
// Either way the next poll is scheduled.
func (m Model) finishPoll(res poll.Result) (Model, tea.Cmd) {
	m.polling = false
	m.quota.Observe(res.Rate)
	var save tea.Cmd
	switch {
	case poll.IsRateLimited(res.Err):
		// The paced note in the banner explains this; the raw error doesn't.
		m.quota.ObserveRateLimited(poll.RateLimitReset(res.Err, m.now()))
		m.lastErr = nil
	case res.Err != nil:
		m.lastErr = res.Err
	default:
		now := m.now()
		m.quota.ObserveSuccess()
		m.lastErr, m.lastPoll = nil, now
		m.feedNote = feedNote(res.FeedErrs)
		if m.pollIdle {
			m.lastIdlePoll = m.pollStarted
		}
		next, removed := tracker.Apply(m.prs, tracker.PollInput{
			Found: res.Found, Snapshots: res.Snapshots, Viewer: m.viewer, Now: now,
		}, m.cfg)
		m.prs = next
		m = m.markSave(slices.Collect(maps.Keys(next))...)
		m = m.markDelete(removed...)
		m, save = m.flush()
	}
	wait, reason := m.quota.Next(m.cfg.GitHub.PollInterval.Duration, m.now())
	m.rateNote = reason
	m.pollGen++
	gen := m.pollGen
	next := m.tick(wait, func(time.Time) tea.Msg { return pollTickMsg{gen: gen} })
	return m, tea.Batch(save, next)
}

// feedNote is the banner for searches a poll skipped: each error names its
// feed.
func feedNote(errs []error) string {
	notes := make([]string, len(errs))
	for i, err := range errs {
		notes[i] = strings.Join(strings.Fields(err.Error()), " ")
	}
	return strings.Join(notes, "; ")
}

func (m Model) markSave(ids ...string) Model {
	m.dirty, m.deleted = maps.Clone(m.dirty), maps.Clone(m.deleted)
	for _, id := range ids {
		m.dirty[id] = true
		delete(m.deleted, id)
	}
	return m
}

func (m Model) markDelete(ids ...string) Model {
	m.dirty, m.deleted = maps.Clone(m.dirty), maps.Clone(m.deleted)
	for _, id := range ids {
		m.deleted[id] = true
		delete(m.dirty, id)
	}
	return m
}

// flush starts a save of the pending changes unless one is running; the
// running save's savedMsg flushes again.
func (m Model) flush() (Model, tea.Cmd) {
	if m.saving || (len(m.dirty) == 0 && len(m.deleted) == 0) {
		return m, nil
	}
	var save []tracker.Tracked
	for _, id := range slices.Sorted(maps.Keys(m.dirty)) {
		if t, ok := m.prs[id]; ok {
			save = append(save, t)
		}
	}
	del := slices.Sorted(maps.Keys(m.deleted))
	m.dirty, m.deleted, m.saving = map[string]bool{}, map[string]bool{}, true
	st := m.store
	return m, func() tea.Msg {
		ctx := context.Background()
		if len(save) > 0 {
			if err := st.SaveTracked(ctx, save); err != nil {
				return savedMsg{err: err}
			}
		}
		if len(del) > 0 {
			return savedMsg{err: st.DeleteTracked(ctx, del)}
		}
		return savedMsg{}
	}
}

// reload swaps in a good config and its theme. A bad one, or one whose theme
// won't build, keeps the last good config and says so in a banner.
func (m Model) reload(r config.Reload) (Model, tea.Cmd) {
	err := r.Err
	if err == nil {
		var th theme.Theme
		if th, err = theme.New(r.Config.Theme); err == nil {
			m.cfg, m.theme, m.warnings, m.configBanner = r.Config, th, r.Warnings, ""
		}
	}
	if err != nil {
		m.configBanner = fmt.Sprintf("config error: %v — using last good config", err)
	}
	return m, m.listenReloads()
}

// listenReloads waits for the next config reload. It stops when the
// channel closes, and is nil without one.
func (m Model) listenReloads() tea.Cmd {
	ch := m.reloads
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		r, ok := <-ch
		if !ok {
			return nil
		}
		return reloadMsg(r)
	}
}

// clockTick redraws once a second so "polled Ns ago" stays current.
func (m Model) clockTick() tea.Cmd {
	return m.tick(clockInterval, func(t time.Time) tea.Msg { return clockMsg(t) })
}

func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

// render lays out the top line, banners, body and footer to fill the window.
func (m Model) render() string {
	w, h := m.width, m.height
	lines := []string{m.topLine(w)}
	lines = append(lines, m.bannerLines(w)...)
	if bodyH := h - len(lines) - 1; bodyH > 0 {
		lines = append(lines, fit(m.body(w, bodyH), w, bodyH)...)
	}
	lines = append(lines, m.footer(w))
	if len(lines) > h {
		lines = lines[:max(h, 0)]
	}
	return strings.Join(lines, "\n")
}

// bodySize is the room the view gets between the top line, banners and
// footer.
func (m Model) bodySize() (width, height int) {
	return m.width, max(m.height-1-len(m.bannerLines(m.width))-1, 0)
}

// body renders the current view into width×height. Each view supplies its
// own.
func (m Model) body(width, height int) string {
	if m.help {
		return m.helpBody(width, height)
	}
	switch m.view {
	case ViewWall:
		w := m.wall
		w.now, w.feeds = m.now(), m.feedNames()
		w.setSize(width, height)
		return w.view(width, height, m.wallPRs(), m.theme, m.spinFrame)
	case ViewDone:
		d := m.done
		d.now, d.focus = m.now(), m.focus
		d.setSize(width, height)
		return d.view(width, height, m.wallPRs(), m.theme)
	case ViewAgent:
		return placeholder(m.theme, width, height, "Coming in plan 2")
	default:
		return placeholder(m.theme, width, height, "Coming in plan 3")
	}
}

// fit cuts or pads s to exactly height lines of at most width cells.
func fit(s string, width, height int) []string {
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, width, "")
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}
