// Package tui is snow's Bubble Tea interface: the root model that runs the
// poll loop, the status bar, and the views.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/cli/go-gh/v2/pkg/browser"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/poll"
	"github.com/chrisdaly-3/jawnsnow/internal/review"
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
	viewerTimeout = 15 * time.Second
	// finalSaveTimeout bounds the save that blocks quitting.
	finalSaveTimeout = 5 * time.Second
	clockInterval    = time.Second
)

// Deps is everything the root model needs from startup.
type Deps struct {
	Config  config.Config
	Theme   theme.Theme
	Client  github.Client // nil when gh isn't logged in; then snow never polls
	Store   *store.Store  // required
	Viewer  string        // the gh user; when empty the model looks it up
	Banners []string
	// LoginWarning is shown above every other banner: snow can't do
	// anything useful without a gh login.
	LoginWarning string
	// ConfigBanner is a note about the config file, cleared by the first
	// good reload.
	ConfigBanner string
	// ConfigPath is the config file snow loaded or created; the Agent
	// view's e key edits it. Empty means the default lookup.
	ConfigPath string
	Reloads    <-chan config.Reload
	Now        func() time.Time
	// Reviewer runs Claude reviews. It is nil when reviews are unavailable,
	// for the reason in ReviewsOff.
	Reviewer   Reviewer
	ReviewsOff string
	// Claude is what startup detection found about the claude CLI. The zero
	// value means detection didn't run, and nothing is refused.
	Claude review.ClaudeInfo
	// Poster makes the one GitHub write, a review posted from the post
	// flow. It is nil when posting is unavailable, for the reason in
	// PostOff.
	Poster  github.Poster
	PostOff string
	// DataDir holds the post flow's temporary files. Empty means the
	// directory of the store's default path.
	DataDir string
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
	viewerMsg struct {
		login string
		err   error
	}
)

// An action is what a view asks the root model to do to a PR.
type action interface{ isAction() }

type (
	actionNone   struct{}
	actionHide   struct{ ID string }
	actionOpen   struct{ URL string }
	actionUnhide struct{ ID string }
	// actionReview forces a review; actionPickModel opens the model picker
	// for one.
	actionReview    struct{ ID string }
	actionPickModel struct{ ID string }
	// actionOpenDrawer opens the review drawer on a PR.
	actionOpenDrawer struct{ ID string }
	// actionOpenReview opens the drawer on a PR at one of its reviews.
	actionOpenReview struct{ PRID, ReviewID string }
	// actionCopy puts Text on the clipboard.
	actionCopy struct{ Text string }
	// actionPost opens the post flow on one of the drawer's reviews.
	actionPost struct{ ReviewID string }
)

func (actionNone) isAction()       {}
func (actionHide) isAction()       {}
func (actionOpen) isAction()       {}
func (actionUnhide) isAction()     {}
func (actionReview) isAction()     {}
func (actionPickModel) isAction()  {}
func (actionOpenDrawer) isAction() {}
func (actionOpenReview) isAction() {}
func (actionCopy) isAction()       {}
func (actionPost) isAction()       {}

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
	// launch wraps each review's command; tests hold them back with it.
	launch func(tea.Cmd) tea.Cmd
	// execProcess runs the editor; tests record it instead.
	execProcess func(*exec.Cmd, tea.ExecCallback) tea.Cmd

	poster  github.Poster
	postOff string
	dataDir string
	// configPath is the file e edits.
	configPath string
	// post is the open post flow, or nil.
	post *postFlow
	// pollAgain asks for a poll as soon as the one in flight finishes.
	pollAgain bool
	// unconfirmedPosts are posts the store has no row for, by review ID:
	// ones whose record failed, and ones that may have reached GitHub.
	unconfirmedPosts map[string]unconfirmedPost
	// assumed are reviews this session posted that GitHub's snapshots may
	// not show yet, by PR ID.
	assumed map[string]assumedReview

	reviews reviewsModel
	// session is the Agent view's model picks for this session.
	session review.SessionTiers
	claude  review.ClaudeInfo
	agent   agentModel
	cost    costModel
	// reviewLog is the Reviews view.
	reviewLog reviewLogModel
	// pendingReviewsFilter is the PR filter the Cost view's top-PR list
	// hands to the Reviews view, which applies it.
	pendingReviewsFilter reviewsFilter

	prs map[string]tracker.Tracked
	// loaded is set once the stored PRs arrive; no poll starts before, or
	// its result would be saved over rows not yet read. readOnly is set when
	// that load failed: nothing is saved for the rest of the session.
	loaded, readOnly bool

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
	// unreachable are the tracked PRs a poll couldn't reach and none has
	// reached since. Waiting PRs are looked up only on idle polls, so one
	// poll's skips alone would make the banner come and go.
	unreachable map[string]github.SkippedPR
	// failNote is the whole error of a poll that reached nothing, which the
	// top line cuts short.
	failNote string

	// One save runs at a time, so an older write can't land after a newer
	// one. Changes made meanwhile wait in dirty and deleted.
	saving  bool
	dirty   map[string]bool
	deleted map[string]bool
	// quitting is set when q waits for the running save to land.
	quitting bool

	configBanner string
	// loginWarning is the startup warning, or one set when GitHub rejects
	// the login; a successful poll clears it.
	loginWarning string
	warnings     []string
	notice       string

	wall   wallModel
	done   doneModel
	drawer drawerModel
	// drawerView is the view the drawer was opened in, the only one it
	// shows in.
	drawerView View
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
	dataDir := d.DataDir
	if dataDir == "" {
		dataDir = filepath.Dir(store.DataPath())
	}
	mode := modeWatcher
	if d.Config.Mode.Default == modeAuto && d.Reviewer != nil && claudeRefused(d.Claude) == "" {
		mode = modeAuto
	}
	return Model{
		cfg: d.Config, claude: d.Claude, theme: d.Theme, client: d.Client, store: d.Store, viewer: d.Viewer,
		banners: d.Banners, loginWarning: d.LoginWarning, configBanner: d.ConfigBanner, reloads: d.Reloads, now: now, tick: tea.Tick,
		open:   func(url string) error { return browser.New("", io.Discard, io.Discard).Browse(url) },
		launch: func(c tea.Cmd) tea.Cmd { return c }, reviews: newReviews(d.Reviewer, d.ReviewsOff),
		execProcess: tea.ExecProcess, poster: d.Poster, postOff: d.PostOff, dataDir: dataDir, configPath: d.ConfigPath,
		prs: map[string]tracker.Tracked{}, wall: newWall(), done: newDone(), cost: newCost(), reviewLog: newReviewLog(), drawer: newDrawer(), dirty: map[string]bool{}, deleted: map[string]bool{},
		mode: mode, width: 80, height: 24,
	}
}

// Init loads the stored PRs, whose arrival starts the first poll, and starts
// the clock, the review timers and the config listener.
func (m Model) Init() tea.Cmd {
	st := m.store
	load := func() tea.Msg {
		prs, err := st.LoadTracked(context.Background())
		return loadedMsg{prs: prs, err: err}
	}
	return tea.Batch(load, m.clockTick(), m.reviewTicks(), m.listenReloads())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	next, closing := next.closeGoneDrawer()
	cmd = tea.Batch(cmd, closing)
	next, spin := next.ensureSpinner()
	if spin != nil {
		cmd = tea.Batch(cmd, spin)
	}
	return next, cmd
}

// ensureSpinner starts the spinner's tick loop when something on the wall
// is spinning or a review runs, and it isn't running.
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
// that are spinning and the running reviews' badges. It is 0 when there is
// nothing to animate.
func (m Model) spinInterval() time.Duration {
	fastest := m.reviewSpinInterval()
	for _, t := range m.displayPRs() {
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
	if next, cmd, ok := m.updateReviews(msg); ok {
		return next, cmd
	}
	if next, cmd, ok := m.updatePost(msg); ok {
		return next, cmd
	}
	if next, cmd, ok := m.updateCost(msg); ok {
		return next, cmd
	}
	if next, cmd, ok := m.updateReviewLog(msg); ok {
		return next, cmd
	}
	if next, ok := m.updateDoneStats(msg); ok {
		return next, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		var cmd tea.Cmd
		m.drawer, cmd = m.drawer.resize(msg.Width)
		return m, cmd
	case drawerTickMsg:
		return m.drawerUpdate(msg)
	case drawerLoadedMsg:
		if msg.Err != nil {
			m.notice = "could not read reviews: " + msg.Err.Error()
			return m, nil
		}
		return m.drawerUpdate(m.withUnrecorded(msg))
	case loadedMsg:
		m.loaded = true
		if msg.err != nil {
			m.readOnly = true
			m.banners = append(slices.Clone(m.banners), "could not load saved PRs: "+msg.err.Error()+" — not saving this session")
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
	case viewerMsg:
		if msg.err != nil {
			return m.finishPoll(poll.Result{Err: fmt.Errorf("look up the gh user: %w", msg.err)})
		}
		m.polling, m.viewer = false, msg.login
		return m.startPoll()
	case savedMsg:
		m.saving = false
		if msg.err != nil {
			m.notice = "could not save PRs: " + msg.err.Error()
		}
		if m.quitting {
			return m.quit()
		}
		return m.flush()
	case editedMsg:
		if msg.err != nil {
			m.notice = "could not run the editor: " + msg.err.Error()
		}
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
	// A review question or the model picker takes every key but ctrl+c.
	if m.reviews.capturing() && msg.String() != "ctrl+c" {
		return m.reviewKey(msg)
	}
	// So does the post flow: nothing may reach GitHub but its own keys.
	if m.post != nil {
		if next, cmd, ok := m.postCapture(msg); ok {
			return next, cmd
		}
	}
	// An open filter box or author picker takes every key, or typing "q"
	// would quit. ctrl+c still quits.
	if m.view == ViewWall && !m.help && m.wall.capturing() && msg.String() != "ctrl+c" {
		return m.updateView(msg)
	}
	if m.view == ViewCost && !m.help && m.cost.capturing() && msg.String() != "ctrl+c" {
		return m.updateView(msg)
	}
	if m.view == ViewReviews && !m.help && m.reviewLog.typing && msg.String() != "ctrl+c" {
		return m.updateView(msg)
	}
	// The help overlay hides the view, so only the keys that close it or
	// quit do anything.
	if m.help {
		switch k := msg.String(); k {
		case "q", "ctrl+c":
			return m.quitKey(k)
		case "?", "esc":
			m.help = false
		}
		return m, nil
	}
	switch k := msg.String(); k {
	case "q", "ctrl+c":
		return m.quitKey(k)
	case "?":
		m.help = true
	case "esc":
		if m.drawerHere() {
			var cmd tea.Cmd
			m.drawer, cmd = m.drawer.hide()
			m.focus = 0
			return m, cmd
		}
		return m.updateView(msg)
	case "]":
		return m.showView((m.view + 1) % viewCount)
	case "[":
		return m.showView((m.view + viewCount - 1) % viewCount)
	case "1", "2", "3", "4", "5":
		return m.showView(View(k[0] - '1'))
	case "tab":
		m.focus = (m.focus + 1) % m.paneCount()
	case "shift+tab":
		n := m.paneCount()
		m.focus = (m.focus + n - 1) % n
	case "R":
		return m.startPoll()
	case "m":
		return m.toggleMode()
	default:
		return m.updateView(msg)
	}
	return m, nil
}

// updateView passes a key the root model doesn't use to the current view.
func (m Model) updateView(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch m.view {
	case ViewWall:
		if m.drawerHere() && m.focus == m.drawerPane() {
			return m.drawerUpdate(msg)
		}
		return m.wallUpdate(msg)
	case ViewDone:
		if m.drawerHere() && m.focus == m.drawerPane() {
			return m.drawerUpdate(msg)
		}
		return m.doneUpdate(msg)
	case ViewReviews:
		if m.drawerHere() && m.focus == m.drawerPane() {
			return m.drawerUpdate(msg)
		}
		return m.reviewLogKey(msg)
	case ViewAgent:
		return m.agentKey(msg)
	case ViewCost:
		return m.costKey(msg)
	}
	return m, nil
}

// drawerUpdate gives msg to the drawer and applies the action it returns.
func (m Model) drawerUpdate(msg tea.Msg) (Model, tea.Cmd) {
	d, cmd, act := m.drawer.update(msg, m.drawerCtx())
	m.drawer = d
	m, actCmd := m.apply(act)
	return m, tea.Batch(cmd, actCmd)
}

// drawerCtx is what the drawer needs to know from the rest of the model.
func (m Model) drawerCtx() drawerCtx {
	id := m.drawer.prID
	s := m.reviews.state[id]
	_, h := m.bodySize()
	return drawerCtx{
		PR: m.prs[id], Running: s.running, Ticker: s.ticker, Frame: m.spinFrame,
		Focused: m.view == m.drawerView && m.focus == m.drawerPane(), Now: m.now(), Theme: m.theme, Tick: m.tick, Height: h,
	}
}

// loadDrawer reads a PR's reviews, and the newest post of each, in a
// command.
func (m Model) loadDrawer(id string) (Model, tea.Cmd) {
	st := m.store
	m.drawer.issued++
	seq := m.drawer.issued
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), inputsTimeout)
		defer cancel()
		rs, err := st.Reviews(ctx, id)
		if err != nil {
			return drawerLoadedMsg{PRID: id, Seq: seq, Err: err}
		}
		posted := map[string]store.Posted{}
		for _, r := range rs {
			ps, err := st.PostedFor(ctx, r.ID)
			if err != nil {
				return drawerLoadedMsg{PRID: id, Seq: seq, Err: err}
			}
			if len(ps) > 0 {
				posted[r.ID] = ps[0]
			}
		}
		return drawerLoadedMsg{PRID: id, Seq: seq, Reviews: rs, Posted: posted}
	}
}

// closeGoneDrawer slides the drawer out when its PR is no longer tracked.
func (m Model) closeGoneDrawer() (Model, tea.Cmd) {
	if !m.drawer.isOpen() {
		return m, nil
	}
	if _, ok := m.prs[m.drawer.prID]; ok {
		return m, nil
	}
	var cmd tea.Cmd
	m.drawer, cmd = m.drawer.hide()
	m.focus = 0
	return m, cmd
}

// doneUpdate gives msg to the Done view and applies the action it returns.
func (m Model) doneUpdate(msg tea.Msg) (Model, tea.Cmd) {
	w, h := m.bodySize()
	w -= m.drawerWidthIn(ViewDone)
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
	w -= m.drawerWidthIn(ViewWall)
	m.wall.setSize(w, h)
	m.wall.now, m.wall.feeds, m.wall.drafted = m.now(), m.feedNames(), m.draftedPRs()
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

// drawerPane is the focus index of the review drawer: after the Done
// view's two tables, or after the Wall.
func (m Model) drawerPane() int {
	if m.view == ViewDone {
		return donePanes
	}
	return 1
}

// drawerHere reports whether the drawer is open in the current view.
func (m Model) drawerHere() bool { return m.drawer.isOpen() && m.view == m.drawerView }

// drawerWidthIn is the drawer's width in view v: 0 unless it was opened
// there.
func (m Model) drawerWidthIn(v View) int {
	if m.drawerView != v {
		return 0
	}
	return m.drawer.width()
}

// paneCount is the number of focusable panes in the current view: the Done
// view's two tables, the Cost view's filter bar, chart and top PRs, the
// Wall, the drawer while it is open in the Wall, Done or Reviews, and one for every
// other view.
func (m Model) paneCount() int {
	n := 1
	switch m.view {
	case ViewDone:
		n = donePanes
	case ViewCost:
		n = costPanes
	}
	if m.drawerHere() && (m.view == ViewDone || m.view == ViewWall || m.view == ViewReviews) {
		n++
	}
	return n
}

func (m Model) switchView(v View) Model {
	m.view, m.focus = v, 0
	return m
}

// showView switches to v and starts what it needs: the Cost view reads its
// rows again.
func (m Model) showView(v View) (Model, tea.Cmd) {
	m = m.switchView(v)
	switch v {
	case ViewCost:
		return m.enterCost()
	case ViewReviews:
		return m.enterReviews()
	case ViewDone:
		return m.loadDoneStats()
	}
	return m, nil
}

// apply carries out a view's action against the current PRs.
func (m Model) apply(a action) (Model, tea.Cmd) {
	switch a := a.(type) {
	case actionHide:
		return m.updatePR(a.ID, tracker.Hide)
	case actionUnhide:
		// Moving it would only be undone at the next poll.
		if t, ok := m.prs[a.ID]; ok && t.Place == tracker.PlaceWaiting && t.WaitReason == tracker.WaitNoFeed {
			m.notice = "no current feed finds this PR — add one that does"
			return m, nil
		}
		return m.updatePR(a.ID, tracker.Unhide)
	case actionOpen:
		if a.URL == "" {
			return m, nil
		}
		open, url := m.open, a.URL
		return m, func() tea.Msg { return openedMsg{err: open(url)} }
	case actionReview:
		return m.forceReview(a.ID, nil, false)
	case actionPickModel:
		return m.openPicker(a.ID), nil
	case actionOpenDrawer:
		var cmd tea.Cmd
		m.drawerView = m.view
		m.drawer, cmd = m.drawer.show(a.ID, m.width)
		// Opening the drawer focuses it, so its keys work at once.
		m.focus = m.drawerPane()
		m, load := m.loadDrawer(a.ID)
		return m, tea.Batch(cmd, load)
	case actionOpenReview:
		if _, ok := m.prs[a.PRID]; !ok {
			m.notice = "that PR is no longer tracked"
			return m, nil
		}
		var cmd tea.Cmd
		m.drawerView = m.view
		m.drawer, cmd = m.drawer.showReview(a.PRID, a.ReviewID, m.width)
		m.focus = m.drawerPane()
		m, load := m.loadDrawer(a.PRID)
		return m, tea.Batch(cmd, load)
	case actionCopy:
		return m, tea.SetClipboard(a.Text)
	case actionPost:
		return m.openPost(a.ReviewID)
	}
	return m, nil
}

func (m Model) updatePR(id string, change func(tracker.Tracked, time.Time) tracker.Tracked) (Model, tea.Cmd) {
	t, ok := m.prs[id]
	if !ok {
		return m, nil
	}
	prev := m.prs
	m.prs = maps.Clone(m.prs)
	m.prs[id] = change(t, m.now())
	m = m.markSave(id)
	m, release := m.placesChanged(prev)
	m, save := m.flush()
	return m, tea.Batch(save, release)
}

// startPoll starts a poll unless one is in flight, the stored PRs haven't
// loaded, or there is no client. The command gets copies, so later changes
// to the model don't race it.
func (m Model) startPoll() (Model, tea.Cmd) {
	if m.client == nil || m.polling || !m.loaded {
		return m, nil
	}
	if m.viewer == "" {
		return m.lookUpViewer()
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
		// The rollup can stay PENDING after the checks snow counts have
		// finished, such as while an ignored gate waits. Fetch these every
		// poll.
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

// lookUpViewer asks GitHub who the gh user is. Statuses depend on it, so
// polls wait for it; a failure is retried on the next poll tick.
func (m Model) lookUpViewer() (Model, tea.Cmd) {
	client := m.client
	m.polling = true
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), viewerTimeout)
		defer cancel()
		login, err := client.Viewer(ctx)
		return viewerMsg{login: login, err: err}
	}
}

// finishPoll applies a poll's result to the current PRs, not the ones the
// poll started from, so a hide made during the poll survives it. A failed
// poll leaves the PRs alone; one GitHub refused for rate limiting backs off.
// Either way the next poll is scheduled.
func (m Model) finishPoll(res poll.Result) (Model, tea.Cmd) {
	m.polling = false
	m.quota.Observe(res.Rate)
	var save, release, load tea.Cmd
	m.failNote = ""
	switch {
	case github.IsUnauthorized(res.Err):
		m = m.rejectLogin()
	case github.IsRateLimited(res.Err):
		// The paced note in the banner explains this; the raw error doesn't.
		m.quota.ObserveRateLimited(github.RateLimitReset(res.Err, m.now()))
		m.lastErr = nil
	case res.Err != nil:
		m.lastErr = res.Err
		var nothing *poll.NothingReachedError
		if errors.As(res.Err, &nothing) {
			m.failNote = "poll failed: " + nothing.Error()
		}
	default:
		now := m.now()
		m.quota.ObserveSuccess()
		m.lastErr, m.lastPoll, m.loginWarning = nil, now, ""
		m.feedNote = feedNote(res.FeedErrs)
		if m.pollIdle {
			m.lastIdlePoll = m.pollStarted
		}
		prev := m.prs
		next, removed := tracker.Apply(m.prs, tracker.PollInput{
			Found: res.Found, Snapshots: res.Snapshots, Viewer: m.viewer, Now: now,
		}, m.cfg)
		m.prs = next
		m = m.settleAssumed(res.Snapshots, m.pollStarted)
		m.unreachable = stillUnreachable(m.unreachable, res, next)
		m = m.markSave(slices.Collect(maps.Keys(next))...)
		m = m.markDelete(removed...)
		m, save = m.flush()
		m, release = m.placesChanged(prev)
		m, load = m.loadReviewInputs()
	}
	wait, reason := m.quota.Next(m.cfg.GitHub.PollInterval.Duration, m.now())
	m.rateNote = reason
	m.pollGen++
	gen := m.pollGen
	next := m.tick(wait, func(time.Time) tea.Msg { return pollTickMsg{gen: gen} })
	var again tea.Cmd
	if m.pollAgain {
		m.pollAgain = false
		m, again = m.startPoll()
	}
	return m, tea.Batch(save, release, load, next, again)
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

// stillUnreachable updates the unreachable PRs after a poll: a PR leaves
// the set when the poll reached it or it is no longer tracked, and the
// poll's skips join it.
func stillUnreachable(prev map[string]github.SkippedPR, res poll.Result, tracked map[string]tracker.Tracked) map[string]github.SkippedPR {
	next := maps.Clone(prev)
	if next == nil {
		next = map[string]github.SkippedPR{}
	}
	for _, id := range res.Reached {
		delete(next, id)
	}
	for _, s := range res.Unreachable {
		next[s.ID] = s
	}
	maps.DeleteFunc(next, func(id string, _ github.SkippedPR) bool {
		_, ok := tracked[id]
		return !ok
	})
	return next
}

// unreachableNote is the banner for the PRs GitHub answers with an error;
// they keep their last status.
func unreachableNote(unreachable map[string]github.SkippedPR) string {
	skipped := slices.Collect(maps.Values(unreachable))
	switch len(skipped) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("1 PR unreachable (%s)", github.SkippedTypes(skipped))
	}
	return fmt.Sprintf("%d PRs unreachable (%s)", len(skipped), github.SkippedTypes(skipped))
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
// running save's savedMsg flushes again. In a read-only session it drops
// them.
func (m Model) flush() (Model, tea.Cmd) {
	if m.readOnly {
		m.dirty, m.deleted = map[string]bool{}, map[string]bool{}
		return m, nil
	}
	if m.saving || (len(m.dirty) == 0 && len(m.deleted) == 0) {
		return m, nil
	}
	m, write := m.takePending()
	m.saving = true
	return m, func() tea.Msg { return savedMsg{err: write(context.Background())} }
}

// takePending clears the pending changes and returns the write that saves
// them, built from copies so it can run off the update loop.
func (m Model) takePending() (Model, func(context.Context) error) {
	var save []tracker.Tracked
	for _, id := range slices.Sorted(maps.Keys(m.dirty)) {
		if t, ok := m.prs[id]; ok {
			save = append(save, t)
		}
	}
	del := slices.Sorted(maps.Keys(m.deleted))
	m.dirty, m.deleted = map[string]bool{}, map[string]bool{}
	st := m.store
	return m, func(ctx context.Context) error {
		if len(save) > 0 {
			if err := st.SaveTracked(ctx, save); err != nil {
				return err
			}
		}
		if len(del) > 0 {
			return st.DeleteTracked(ctx, del)
		}
		return nil
	}
}

// quit saves what is pending before quitting, since nothing would deliver a
// later save's result. If a save is running it waits for it, so the older
// write can't land after the final one; a second q quits regardless.
func (m Model) quit() (Model, tea.Cmd) {
	if m.saving && !m.quitting {
		m.quitting = true
		return m, nil
	}
	if !m.readOnly && (len(m.dirty) > 0 || len(m.deleted) > 0) {
		var write func(context.Context) error
		m, write = m.takePending()
		ctx, cancel := context.WithTimeout(context.Background(), finalSaveTimeout)
		_ = write(ctx) // the screen is going away; there is nowhere to show an error
		cancel()
	}
	return m, tea.Quit
}

// reload swaps in a good config and its theme, and derives every PR's
// status again under it, such as after a change to ignore_checks. A bad
// config, or one whose theme won't build, keeps the last good one and says
// so in a banner.
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
		return m, m.listenReloads()
	}
	m, save := m.recomputeStatuses()
	return m, tea.Batch(save, m.listenReloads())
}

// recomputeStatuses derives every PR's status again under the current
// config and saves the ones that changed. Statuses depend on the viewer, so
// until it is known the first poll does this instead.
func (m Model) recomputeStatuses() (Model, tea.Cmd) {
	if m.viewer == "" {
		return m, nil
	}
	next := tracker.Recompute(m.prs, m.cfg, m.viewer)
	var changed []string
	for id, t := range next {
		// YourReview depends only on the viewer and the reviews, which a
		// reload doesn't change.
		was := m.prs[id].Status
		if t.Status.Status != was.Status || t.Status.CI != was.CI || t.Status.Spinning != was.Spinning {
			changed = append(changed, id)
		}
	}
	m.prs = next
	m = m.markSave(changed...)
	return m.flush()
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
	if m.reviews.picker != nil {
		return m.pickerBody(width, height)
	}
	if m.post != nil {
		return m.postBody(width, height)
	}
	switch m.view {
	case ViewWall:
		w := m.wall
		w.now, w.feeds, w.badges, w.drafted = m.now(), m.feedNames(), m.reviewBadges(m.spinFrame), m.draftedPRs()
		dw := min(m.drawerWidthIn(ViewWall), max(width-1, 0))
		w.setSize(width-dw, height)
		wallView := w.view(width-dw, height, m.displayPRs(), m.theme, m.spinFrame)
		if dw == 0 {
			return wallView
		}
		return m.withDrawer(wallView, width-dw, dw, height)
	case ViewDone:
		dw := min(m.drawerWidthIn(ViewDone), max(width-1, 0))
		d := m.done
		d.now, d.focus, d.verdicts = m.now(), m.focus, m.reviewVerdicts()
		d.setSize(width-dw, height)
		doneView := d.view(width-dw, height, m.wallPRs(), m.theme)
		if dw == 0 {
			return doneView
		}
		return m.withDrawer(doneView, width-dw, dw, height)
	case ViewAgent:
		return m.agentBody(width, height)
	case ViewCost:
		return m.costBody(width, height)
	case ViewReviews:
		dw := min(m.drawerWidthIn(ViewReviews), max(width-1, 0))
		logView := m.reviewLogBody(width-dw, height)
		if dw == 0 {
			return logView
		}
		return m.withDrawer(logView, width-dw, dw, height)
	}
	return ""
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

// withDrawer puts the drawer beside the view's lines, which it pads to
// viewW cells so the drawer's edge lines up.
func (m Model) withDrawer(view string, viewW, drawerW, height int) string {
	c := m.drawerCtx()
	left := fit(view, viewW, height)
	right := strings.Split(m.drawer.view(drawerW, height, c), "\n")
	for i := range left {
		left[i] += strings.Repeat(" ", max(viewW-ansi.StringWidth(left[i]), 0))
		if i < len(right) {
			left[i] += right[i]
		}
	}
	return strings.Join(left, "\n")
}
