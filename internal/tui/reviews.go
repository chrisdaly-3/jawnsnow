package tui

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/review"
	"github.com/chrisdaly-3/jawnsnow/internal/schedule"
	"github.com/chrisdaly-3/jawnsnow/internal/status"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/theme"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
)

// Reviewer runs Claude reviews; *review.Engine is the real one. Run and
// ReleasePR can block for a whole review, so the model only calls them from
// commands.
type Reviewer interface {
	Run(ctx context.Context, req review.Request, onEvent func(review.Event)) (store.Review, error)
	ReleasePR(ctx context.Context, repo string, number int) error
	PruneIdle(ctx context.Context) error
}

const (
	reviewTickInterval = 30 * time.Second
	pruneInterval      = 10 * time.Minute
	// quitWait bounds how long q waits for cancelled reviews to save.
	quitWait = 5 * time.Second
	// inputsTimeout bounds the store reads behind one evaluation.
	inputsTimeout = 30 * time.Second
	// eventBuffer holds progress events the UI hasn't taken yet; more are
	// dropped, since the ticker shows only the latest.
	eventBuffer = 16
)

// The choices the c picker offers.
var (
	pickModels  = []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5-2"}
	pickEfforts = []string{"low", "medium", "high", "xhigh", "max"}
)

// reviewSpinner animates the running-review badge whatever the PR's status
// spinner is.
var reviewSpinner = spinner.Dot

// reviewState is what the model knows about one PR's reviews.
type reviewState struct {
	running bool
	cancel  context.CancelFunc
	ticker  string // the running review's latest tool call
	latest  *store.Review
	// failures counts the failed reviews in a row at failHead; a new head
	// or a review that succeeds resets it.
	failures    int
	lastFailure time.Time
	failHead    string
	head        string // the head the running review started at
	skip        string // why the last auto-mode evaluation didn't review it
}

type (
	reviewDoneMsg struct {
		PRID   string
		Review store.Review
		Err    error
	}
	// reviewEventMsg is a progress event. next is the running review's
	// channel, which the model waits on again for its next message.
	reviewEventMsg struct {
		PRID  string
		Event review.Event
		next  <-chan tea.Msg
	}
	reviewInputsMsg struct {
		spent float64
		prs   map[string]reviewInputs
		err   error
	}
	reviewTickMsg        struct{}
	pruneTickMsg         struct{}
	reviewQuitTimeoutMsg struct{}
	releasedMsg          struct{ err error }
	prunedMsg            struct{ err error }
)

// reviewInputs is what one evaluation read from the store about a PR.
type reviewInputs struct {
	latest    *store.Review // newest review of any outcome
	autoToday int
}

type confirmKind int

const (
	confirmBudget confirmKind = iota
	confirmQuit
)

// reviewConfirm is a y/n question that takes every key until answered.
type reviewConfirm struct {
	kind   confirmKind
	prompt string
	id     string // the PR a budget question is about
	oneRun *config.Tier
}

// modelPicker is the c picker: a model, then an effort.
type modelPicker struct {
	id                  string
	choosingEffort      bool
	modelIdx, effortIdx int
}

type queuedReview struct {
	id     string
	oneRun *config.Tier
}

// reviewsModel is the review queue and what the model caches about reviews.
// Its maps and slices are replaced, never written in place, like the root
// model's.
type reviewsModel struct {
	reviewer Reviewer // nil when reviews are unavailable, for the reason in off
	off      string

	state map[string]reviewState
	queue []queuedReview // forced reviews waiting for a free slot
	// released are PRs that left the wall: no review starts for them until
	// they come back.
	released map[string]bool

	// spent and autoToday come from the last evaluation's store reads.
	spent     float64
	autoToday map[string]int
	// One read runs at a time. stale is set when a review finished while
	// it read, so its spend and counts are out of date.
	loading, stale bool
	budgetPaused   bool

	confirm  *reviewConfirm
	picker   *modelPicker
	quitting bool // a quit was confirmed: waiting for cancelled reviews
}

func newReviews(r Reviewer, off string) reviewsModel {
	return reviewsModel{reviewer: r, off: off, state: map[string]reviewState{}, released: map[string]bool{}, autoToday: map[string]int{}}
}

// capturing reports whether a question or the picker takes every key.
func (r reviewsModel) capturing() bool { return r.confirm != nil || r.picker != nil }

func (m Model) unavailable() Model {
	m.notice = "reviews unavailable: " + m.reviews.off
	return m
}

func (m Model) withState(id string, change func(*reviewState)) Model {
	m.reviews.state = maps.Clone(m.reviews.state)
	s := m.reviews.state[id]
	change(&s)
	m.reviews.state[id] = s
	return m
}

func (m Model) runningCount() int {
	n := 0
	for _, s := range m.reviews.state {
		if s.running {
			n++
		}
	}
	return n
}

func (m Model) maxInFlight() int { return max(m.cfg.Review.MaxInFlight, 1) }

func (m Model) queued(id string) bool {
	return slices.ContainsFunc(m.reviews.queue, func(q queuedReview) bool { return q.id == id })
}

// reviewable returns id's PR when a review of it may start now: it is on
// the wall, not released, and not already being reviewed.
func (m Model) reviewable(id string) (tracker.Tracked, bool) {
	t, ok := m.prs[id]
	if !ok || t.Place != tracker.PlaceWall || m.reviews.released[id] || m.reviews.state[id].running || m.reviews.quitting {
		return tracker.Tracked{}, false
	}
	return t, true
}

// updateReviews handles the review messages; ok is false for any other.
func (m Model) updateReviews(msg tea.Msg) (_ Model, _ tea.Cmd, ok bool) {
	switch msg := msg.(type) {
	case reviewEventMsg:
		if m.reviews.state[msg.PRID].running {
			m = m.withState(msg.PRID, func(s *reviewState) { s.ticker = tickerText(msg.Event) })
		}
		if msg.next == nil {
			return m, nil, true
		}
		return m, waitReview(msg.next), true
	case reviewDoneMsg:
		m, cmd := m.finishReview(msg)
		return m, cmd, true
	case reviewInputsMsg:
		m, cmd := m.applyInputs(msg)
		return m, cmd, true
	case reviewTickMsg:
		m, load := m.loadReviewInputs()
		return m, tea.Batch(load, m.tick(reviewTickInterval, func(time.Time) tea.Msg { return reviewTickMsg{} })), true
	case pruneTickMsg:
		next := m.tick(pruneInterval, func(time.Time) tea.Msg { return pruneTickMsg{} })
		rv := m.reviews.reviewer
		if rv == nil {
			return m, next, true
		}
		return m, tea.Batch(next, func() tea.Msg { return prunedMsg{err: rv.PruneIdle(context.Background())} }), true
	case releasedMsg:
		if msg.err != nil {
			m.notice = "could not clean up after a review: " + msg.err.Error()
		}
		return m, nil, true
	case prunedMsg:
		if msg.err != nil {
			m.notice = "could not prune idle worktrees: " + msg.err.Error()
		}
		return m, nil, true
	case reviewQuitTimeoutMsg:
		if !m.reviews.quitting {
			return m, nil, true
		}
		m, cmd := m.quit()
		return m, cmd, true
	}
	return m, nil, false
}

// reviewTicks starts the evaluation and prune timers.
func (m Model) reviewTicks() tea.Cmd {
	return tea.Batch(
		m.tick(reviewTickInterval, func(time.Time) tea.Msg { return reviewTickMsg{} }),
		m.tick(pruneInterval, func(time.Time) tea.Msg { return pruneTickMsg{} }),
	)
}

// ---- starting and finishing ----

// startReview runs a review of t in a command. The command starts Run in a
// goroutine and returns its first message; each progress event waits again
// on the same channel, until the reviewDoneMsg.
func (m Model) startReview(t tracker.Tracked, mode review.Mode, oneRun *config.Tier) (Model, tea.Cmd) {
	id := t.PR.ID
	ctx, cancel := context.WithCancel(context.Background())
	m = m.withState(id, func(s *reviewState) {
		s.running, s.cancel, s.ticker, s.head = true, cancel, "", t.PR.HeadOID
	})
	if mode == review.ModeAuto {
		m.reviews.autoToday = maps.Clone(m.reviews.autoToday)
		m.reviews.autoToday[id]++
	}
	req := review.Request{PR: t, Mode: mode, Force: mode == review.ModeManual, Session: m.session, OneRun: oneRun}
	rv := m.reviews.reviewer
	ch := make(chan tea.Msg, eventBuffer)
	run := func() tea.Msg {
		go func() {
			defer cancel()
			r, err := rv.Run(ctx, req, func(ev review.Event) {
				select {
				case ch <- reviewEventMsg{PRID: id, Event: ev}:
				default:
				}
			})
			ch <- reviewDoneMsg{PRID: id, Review: r, Err: err}
			close(ch)
		}()
		return waitReview(ch)()
	}
	return m, m.launch(run)
}

// waitReview takes a running review's next message off its channel.
func waitReview(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		if ev, isEvent := msg.(reviewEventMsg); isEvent {
			ev.next = ch
			return ev
		}
		return msg
	}
}

func (m Model) finishReview(msg reviewDoneMsg) (Model, tea.Cmd) {
	failed := msg.Err != nil || msg.Review.Outcome == "failed"
	now := m.now()
	m = m.withState(msg.PRID, func(s *reviewState) {
		head := cmp.Or(msg.Review.HeadOID, s.head)
		s.running, s.cancel, s.ticker = false, nil, ""
		r := msg.Review
		if r.ID == "" { // the run failed before it had a review to return
			r = store.Review{PRID: msg.PRID, HeadOID: head, StartedAt: now}
		}
		if failed {
			r.Outcome = "failed"
			if r.Error == "" && msg.Err != nil {
				r.Error = msg.Err.Error()
			}
		}
		s.latest = &r
		switch {
		case !failed:
			s.failures, s.failHead = 0, ""
		case s.failHead != head:
			s.failures, s.failHead, s.lastFailure = 1, head, now
		default:
			s.failures, s.lastFailure = s.failures+1, now
		}
	})
	if m.reviews.quitting {
		if m.runningCount() == 0 {
			return m.quit()
		}
		return m, nil
	}
	m, start := m.startQueued()
	m, load := m.loadReviewInputs()
	return m, tea.Batch(start, load)
}

// startQueued starts queued forced reviews while slots are free. One whose
// PR left the wall is dropped.
func (m Model) startQueued() (Model, tea.Cmd) {
	var cmds []tea.Cmd
	for len(m.reviews.queue) > 0 && m.runningCount() < m.maxInFlight() {
		q := m.reviews.queue[0]
		m.reviews.queue = slices.Clone(m.reviews.queue[1:])
		t, ok := m.reviewable(q.id)
		if !ok {
			continue
		}
		var cmd tea.Cmd
		m, cmd = m.startReview(t, review.ModeManual, q.oneRun)
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// ---- evaluation ----

// loadReviewInputs reads, in one command, what an evaluation needs from
// the store: today's spend, and each wall or waiting PR's latest review and
// auto reviews today. If a read is running it asks for another after it.
func (m Model) loadReviewInputs() (Model, tea.Cmd) {
	if m.reviews.loading {
		m.reviews.stale = true
		return m, nil
	}
	var ids []string
	for id, t := range m.prs {
		if t.Place == tracker.PlaceWall || t.Place == tracker.PlaceWaiting {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	st, day := m.store, schedule.DayStart(m.now())
	m.reviews.loading, m.reviews.stale = true, false
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), inputsTimeout)
		defer cancel()
		spent, err := st.SpentSince(ctx, day)
		if err != nil {
			return reviewInputsMsg{err: err}
		}
		prs := make(map[string]reviewInputs, len(ids))
		for _, id := range ids {
			n, err := st.AutoReviewsSince(ctx, id, day)
			if err != nil {
				return reviewInputsMsg{err: err}
			}
			rs, err := st.Reviews(ctx, id)
			if err != nil {
				return reviewInputsMsg{err: err}
			}
			in := reviewInputs{autoToday: n}
			if len(rs) > 0 {
				in.latest = &rs[0]
			}
			prs[id] = in
		}
		return reviewInputsMsg{spent: spent, prs: prs}
	}
}

// applyInputs caches a read's results, then lets auto mode start reviews.
// A read that a finished review made stale gives only its latest reviews,
// and another read starts.
func (m Model) applyInputs(msg reviewInputsMsg) (Model, tea.Cmd) {
	m.reviews.loading = false
	if msg.err != nil {
		m.notice = "could not read reviews: " + msg.err.Error()
		return m, nil
	}
	for id, in := range msg.prs {
		if in.latest == nil {
			continue
		}
		m = m.withState(id, func(s *reviewState) {
			// A review that finished during the read is newer than its result.
			if s.latest == nil || !in.latest.StartedAt.Before(s.latest.StartedAt) {
				s.latest = in.latest
			}
		})
	}
	if m.reviews.stale {
		return m.loadReviewInputs()
	}
	m.reviews.spent = msg.spent
	m.reviews.autoToday = map[string]int{}
	for id, in := range msg.prs {
		m.reviews.autoToday[id] = in.autoToday
	}
	return m.autoStart()
}

// autoStart, in auto mode, decides each wall PR and starts the allowed
// ones, oldest head first, until max_in_flight reviews run.
func (m Model) autoStart() (Model, tea.Cmd) {
	if m.mode != modeAuto || m.reviews.reviewer == nil || m.viewer == "" || m.reviews.quitting {
		return m, nil
	}
	var wall []tracker.Tracked
	for _, t := range m.prs {
		if t.Place == tracker.PlaceWall {
			wall = append(wall, t)
		}
	}
	slices.SortFunc(wall, func(a, b tracker.Tracked) int {
		return cmp.Or(a.HeadSeenAt.Compare(b.HeadSeenAt), strings.Compare(a.PR.ID, b.PR.ID))
	})
	now := m.now()
	paused := false
	var cmds []tea.Cmd
	for _, t := range wall {
		id := t.PR.ID
		s := m.reviews.state[id]
		c := schedule.Candidate{
			T: t, Latest: s.latest, AutoToday: m.reviews.autoToday[id], TeamFeed: m.teamFeed(t),
			Running: s.running || m.queued(id),
		}
		if s.failHead == t.PR.HeadOID {
			c.Failures, c.LastFailure = s.failures, s.lastFailure
		}
		d := schedule.Decide(c, m.cfg, m.reviews.spent, m.viewer, now)
		m = m.withState(id, func(s *reviewState) { s.skip = d.Reason })
		paused = paused || d.Reason == "budget"
		if !d.Review || m.runningCount() >= m.maxInFlight() {
			continue
		}
		if _, ok := m.reviewable(id); !ok {
			continue
		}
		var cmd tea.Cmd
		m, cmd = m.startReview(t, review.ModeAuto, nil)
		cmds = append(cmds, cmd)
	}
	m.reviews.budgetPaused = paused
	return m, tea.Batch(cmds...)
}

// teamFeed reports whether any of t's feeds is a team feed in the config.
func (m Model) teamFeed(t tracker.Tracked) bool {
	for _, f := range m.cfg.Feeds {
		if f.Team && slices.Contains(t.Feeds, f.Name) {
			return true
		}
	}
	return false
}

// toggleMode switches between watcher and auto. Auto needs a reviewer.
func (m Model) toggleMode() (Model, tea.Cmd) {
	if m.mode == modeAuto {
		// Nothing is skipped in watcher mode, so the reasons go too.
		m.mode, m.reviews.budgetPaused = modeWatcher, false
		for id := range m.reviews.state {
			m = m.withState(id, func(s *reviewState) { s.skip = "" })
		}
		return m, nil
	}
	if m.reviews.reviewer == nil {
		return m.unavailable(), nil
	}
	m.mode = modeAuto
	return m.loadReviewInputs()
}

// ---- forced reviews ----

// forceReview starts a manual review of id. It asks first when today's
// budget is spent, unless confirmed, and queues the review when
// max_in_flight reviews are running.
func (m Model) forceReview(id string, oneRun *config.Tier, confirmed bool) (Model, tea.Cmd) {
	if m.reviews.reviewer == nil {
		return m.unavailable(), nil
	}
	t, ok := m.reviewable(id)
	if !ok || m.queued(id) {
		return m, nil
	}
	if daily := m.cfg.Review.Budget.DailyUSD; !confirmed && m.reviews.spent >= daily {
		m.reviews.confirm = &reviewConfirm{
			kind: confirmBudget, id: id, oneRun: oneRun,
			prompt: fmt.Sprintf("Over today's $%s budget — review anyway? y/n", money(daily)),
		}
		return m, nil
	}
	if m.runningCount() >= m.maxInFlight() {
		m.reviews.queue = append(slices.Clone(m.reviews.queue), queuedReview{id: id, oneRun: oneRun})
		m.notice = fmt.Sprintf("review queued: %d already running", m.runningCount())
		return m, nil
	}
	return m.startReview(t, review.ModeManual, oneRun)
}

func (m Model) openPicker(id string) Model {
	if m.reviews.reviewer == nil {
		return m.unavailable()
	}
	if _, ok := m.reviewable(id); ok {
		m.reviews.picker = &modelPicker{id: id}
	}
	return m
}

// reviewKey handles a key while a question or the picker is open.
func (m Model) reviewKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if c := m.reviews.confirm; c != nil {
		switch msg.String() {
		case "y", "Y":
			m.reviews.confirm = nil
			if c.kind == confirmQuit {
				return m.cancelAndQuit()
			}
			return m.forceReview(c.id, c.oneRun, true)
		case "n", "N", "esc":
			m.reviews.confirm = nil
		}
		return m, nil
	}
	p := *m.reviews.picker
	n, idx := len(pickModels), &p.modelIdx
	if p.choosingEffort {
		n, idx = len(pickEfforts), &p.effortIdx
	}
	switch msg.String() {
	case "j", "down":
		*idx = min(*idx+1, n-1)
	case "k", "up":
		*idx = max(*idx-1, 0)
	case "esc":
		m.reviews.picker = nil
		return m, nil
	case "enter":
		if p.choosingEffort {
			m.reviews.picker = nil
			return m.forceReview(p.id, &config.Tier{Model: pickModels[p.modelIdx], Effort: pickEfforts[p.effortIdx]}, false)
		}
		p.choosingEffort = true
	}
	m.reviews.picker = &p
	return m, nil
}

// ---- leaving the wall ----

// placesChanged releases each PR that was on the wall in prev and isn't
// now, and clears the mark on released PRs that came back.
func (m Model) placesChanged(prev map[string]tracker.Tracked) (Model, tea.Cmd) {
	var cmds []tea.Cmd
	for _, id := range slices.Sorted(maps.Keys(prev)) {
		p := prev[id]
		if t, ok := m.prs[id]; p.Place != tracker.PlaceWall || (ok && t.Place == tracker.PlaceWall) {
			continue
		}
		var cmd tea.Cmd
		m, cmd = m.release(id, p.PR.Repo, p.PR.Number)
		cmds = append(cmds, cmd)
	}
	for id := range m.reviews.released {
		if t, ok := m.prs[id]; ok && t.Place == tracker.PlaceWall {
			m.reviews.released = maps.Clone(m.reviews.released)
			delete(m.reviews.released, id)
		}
	}
	return m, tea.Batch(cmds...)
}

// release cancels the PR's running review and marks it released so no new
// one starts. Only then does a command free its worktree and files: the
// release waits on the PR's lock, which the cancelled run holds until it
// has saved.
func (m Model) release(id, repo string, number int) (Model, tea.Cmd) {
	rv := m.reviews.reviewer
	if rv == nil {
		return m, nil
	}
	if s := m.reviews.state[id]; s.running && s.cancel != nil {
		s.cancel()
	}
	m.reviews.released = maps.Clone(m.reviews.released)
	m.reviews.released[id] = true
	m.reviews.queue = slices.DeleteFunc(slices.Clone(m.reviews.queue), func(q queuedReview) bool { return q.id == id })
	return m, func() tea.Msg { return releasedMsg{err: rv.ReleasePR(context.Background(), repo, number)} }
}

// ---- quitting ----

// quitKey quits, asking first when q is pressed with reviews running.
// ctrl+c cancels them without asking, and a q while cancelled reviews
// finish quits at once.
func (m Model) quitKey(k string) (Model, tea.Cmd) {
	n := m.runningCount()
	switch {
	case n == 0 || m.reviews.quitting:
		return m.quit()
	case k == "ctrl+c":
		return m.cancelAndQuit()
	}
	prompt := fmt.Sprintf("%d reviews running — quit and cancel them? y/n", n)
	if n == 1 {
		prompt = "1 review running — quit and cancel it? y/n"
	}
	m.help = false
	m.reviews.confirm = &reviewConfirm{kind: confirmQuit, prompt: prompt}
	return m, nil
}

// cancelAndQuit cancels every running review and quits when the last one
// has finished saving, or after quitWait.
func (m Model) cancelAndQuit() (Model, tea.Cmd) {
	n := m.runningCount()
	if n == 0 {
		return m.quit()
	}
	for _, s := range m.reviews.state {
		if s.running && s.cancel != nil {
			s.cancel()
		}
	}
	m.reviews.quitting, m.reviews.queue, m.reviews.confirm, m.reviews.picker = true, nil, nil, nil
	m.notice = fmt.Sprintf("cancelling %d running reviews…", n)
	return m, m.tick(quitWait, func(time.Time) tea.Msg { return reviewQuitTimeoutMsg{} })
}

// ---- rendering ----

// displayPRs is the PRs as the wall draws them: a PR whose review is
// running spins. It is decided here, at render, because every poll derives
// the stored statuses again.
func (m Model) displayPRs() []tracker.Tracked {
	prs := m.wallPRs()
	for i, t := range prs {
		if m.reviews.state[t.PR.ID].running {
			prs[i].Status.Spinning = true
		}
	}
	return prs
}

// reviewSpinInterval is the running-review badge's frame period, or 0 when
// no review runs.
func (m Model) reviewSpinInterval() time.Duration {
	if m.runningCount() == 0 {
		return 0
	}
	return reviewSpinner.FPS
}

// reviewBadges is each wall PR's Claude badge.
func (m Model) reviewBadges(frame int) map[string]string {
	out := map[string]string{}
	for id, t := range m.prs {
		if t.Place != tracker.PlaceWall {
			continue
		}
		if b := m.badge(t, frame); b != "" {
			out[id] = b
		}
	}
	return out
}

func (m Model) badge(t tracker.Tracked, frame int) string {
	th := m.theme
	s := m.reviews.state[t.PR.ID]
	switch {
	case s.running:
		f := reviewSpinner.Frames[frame%len(reviewSpinner.Frames)]
		return th.Accent().Render(strings.TrimSpace(f) + " Claude reviewing…")
	case s.skip == "teammate" && !reviewedAt(s.latest, t.PR.HeadOID):
		return th.Muted().Render("· teammate reviewed")
	}
	return verdictBadge(th, s.latest)
}

// reviewedAt reports whether r is a review at head that didn't fail.
func reviewedAt(r *store.Review, head string) bool {
	return r != nil && r.HeadOID == head && r.Outcome != "failed"
}

// verdictBadge is the badge for a finished review, or "" for none.
func verdictBadge(th theme.Theme, r *store.Review) string {
	switch {
	case r == nil:
		return ""
	case r.Outcome == "failed":
		return th.StatusFg(string(status.CIFailed)).Render("! review failed")
	}
	if n := blockingCount(r.Findings); n > 0 {
		return th.StatusFg(string(status.CIFailed)).Render("✦ Claude: " + strconv.Itoa(n) + " blocking")
	}
	return th.StatusFg(string(status.Approved)).Render("✦ Claude: clean")
}

// blockingCount counts the blocking findings still open.
func blockingCount(fs []store.Finding) int {
	n := 0
	for _, f := range fs {
		if f.Blocking && f.Ledger != "addressed" && f.Ledger != "answered" {
			n++
		}
	}
	return n
}

// reviewVerdicts is the latest verdict of each waiting PR, for the Done view.
func (m Model) reviewVerdicts() map[string]string {
	out := map[string]string{}
	for id, s := range m.reviews.state {
		if t, ok := m.prs[id]; ok && t.Place == tracker.PlaceWaiting {
			if b := verdictBadge(m.theme, s.latest); b != "" {
				out[id] = b
			}
		}
	}
	return out
}

// tickerText summarizes a tool call: "reading pkg/foo.go".
func tickerText(ev review.Event) string {
	verb := map[string]string{"Read": "reading", "Grep": "searching", "Glob": "listing"}[ev.Tool]
	if verb == "" {
		verb = strings.ToLower(ev.Tool)
	}
	return clean(strings.TrimSpace(verb + " " + ev.Path))
}

// spendLine is the status bar's spend, and a note when auto mode paused
// for the budget.
func (m Model) spendLine() string {
	th := m.theme
	s := th.Muted().Render(fmt.Sprintf("$%.2f / $%s today", m.reviews.spent, money(m.cfg.Review.Budget.DailyUSD)))
	if m.mode == modeAuto && m.reviews.budgetPaused {
		s += th.Muted().Render(" · ") + th.Accent().Render("auto-review paused: daily budget")
	}
	return s
}

// reviewPrompt is the open question, which the footer shows in place of
// the key hints.
func (m Model) reviewPrompt() string {
	if m.reviews.confirm == nil {
		return ""
	}
	return m.theme.Accent().Render(m.reviews.confirm.prompt)
}

// pickerBody draws the c picker over the view.
func (m Model) pickerBody(width, height int) string {
	p, th := m.reviews.picker, m.theme
	items, idx, title := pickModels, p.modelIdx, "Review with model"
	if p.choosingEffort {
		items, idx, title = pickEfforts, p.effortIdx, "Effort for "+pickModels[p.modelIdx]
	}
	var b strings.Builder
	b.WriteString(th.Accent().Render(title))
	for i, it := range items {
		mark := "  "
		if i == idx {
			mark = th.Accent().Render("▸ ")
		}
		b.WriteString("\n" + mark + it)
	}
	b.WriteString("\n" + th.Muted().Render("j/k move · enter pick · esc cancel"))
	box := th.Border(true).Padding(0, 2).Render(b.String())
	return lipgloss.Place(max(width, 0), max(height, 0), lipgloss.Center, lipgloss.Center, box)
}
