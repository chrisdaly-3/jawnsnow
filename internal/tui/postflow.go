package tui

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/post"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
)

// The post flow is the only way snow writes to GitHub. P in the drawer opens
// it on the review shown, and nothing is sent until enter in the preview.

type postStage int

const (
	postLoading postStage = iota // reading the review's earlier posts
	postRepost                   // it was posted before: ask first
	postPick
	postPreview
	postResult // posted, but recording it failed
)

type postEvent struct {
	event post.Event
	label string
}

var postEvents = []postEvent{{post.Approve, "Approve"}, {post.Comment, "Comment"}, {post.RequestChanges, "Request changes"}}

const (
	// postTimeout bounds the mutation and the thread lookup after it.
	postTimeout = 2 * time.Minute
	// recordTimeout bounds the store write, on a context of its own so a
	// slow GitHub can't leave a post unrecorded.
	recordTimeout = 30 * time.Second
	postMaxWidth  = 110
	// postConfirmDelay is how long the preview must be open before enter
	// posts.
	postConfirmDelay = 500 * time.Millisecond
)

const postQuitWarning = "posting to GitHub — ctrl+c again to quit anyway"

const headMovedWarning = "PR has new commits since this review — comments will show as outdated"

const uncertainPost = "the post may have reached GitHub — check the PR before posting again: "

// postDoubt is why a post the store has no row for may still be on GitHub.
type postDoubt int

const (
	postUnrecorded postDoubt = iota + 1 // it posted, but the record failed
	postUncertain                       // the post failed in a way that may have come after GitHub took it
)

// unconfirmedPost is a post only the model knows of. Drawer loads never
// replace these, so the repost question survives them.
type unconfirmedPost struct {
	Event string
	At    time.Time
	Kind  postDoubt
}

// priorPost is what the repost question names.
type priorPost struct {
	event     string
	at        time.Time
	uncertain bool
}

// postKey identifies a stored post, so the flow can tell one recorded
// since it opened.
func postKey(p store.Posted) string {
	return p.GitHubReviewID + "@" + strconv.FormatInt(p.At.UnixNano(), 10)
}

// postFlow is the open post modal. Like the picker it is replaced, never
// written in place, since older copies of the model share it.
type postFlow struct {
	stage  postStage
	review store.Review // a copy, so a drawer reload can't change what posts
	// ask is the earlier post the repost question names; known are the
	// stored posts the flow has asked about, by postKey.
	ask   priorPost
	known map[string]bool
	// mustAsk is set when a post may have reached GitHub: the next enter
	// asks again before sending.
	mustAsk bool
	pick    int // index into postEvents
	sel     post.Selection
	cursor  int // the checklist row, in orderedFindings order
	scroll  int
	raw     bool
	// inFlight is set from the confirming enter until the post's result
	// arrives; every key but ctrl+c is ignored meanwhile, so a second
	// enter sends nothing. editing is the same for the editor. quitArmed
	// is set by a first ctrl+c while posting.
	inFlight, editing, quitArmed bool
	// previewAt is when the preview last changed: it opened, the
	// selection or summary changed, or an enter came too soon. An enter
	// sooner than postConfirmDelay after it is held or bounced, and on
	// most terminals a held key arrives as plain presses, so each one
	// restarts the wait; holdOn says so in the footer.
	previewAt time.Time
	holdOn    bool
	err       string // the last post or editor failure
	result    string // postResult's text
	// built is post.Build over review and sel, made whenever sel changes,
	// so the view doesn't build on every frame.
	built postBuild
}

type postBuild struct {
	in  github.PostInput
	pv  post.Preview
	err error
}

// buildPost is post.Build; tests count its calls.
var buildPost = post.Build

// rebuild builds the payload for the current selection.
func (p *postFlow) rebuild() {
	p.built.in, p.built.pv, p.built.err = buildPost(p.review, p.review.PRID, p.sel)
}

type (
	postPriorMsg struct {
		ReviewID string
		Posted   []store.Posted
		Err      error
	}
	// postEditReadyMsg says the summary's temp file is written.
	postEditReadyMsg struct {
		ReviewID, Path string
		Err            error
	}
	postEditedMsg struct {
		ReviewID, Body string
		Err            error
	}
	// postDoneMsg ends a post. Raced is a post recorded since the flow
	// opened, and then nothing was sent. Err means the post failed:
	// github.IsRefused says whether nothing reached GitHub. Otherwise it
	// posted, and RecordErr says whether the record was saved.
	postDoneMsg struct {
		ReviewID, PRID, Event string
		// Body and NewThreads are the posted review's summary and how many
		// threads it started.
		Body       string
		NewThreads int
		Posted     store.Posted
		Raced      *store.Posted
		Err        error
		ThreadErr  error
		RecordErr  error
	}
)

// openPost opens the flow on one of the drawer's reviews, after reading
// whether it was posted before.
func (m Model) openPost(reviewID string) (Model, tea.Cmd) {
	if m.poster == nil {
		reason := m.postOff
		if reason == "" {
			reason = "gh is not logged in"
		}
		m.notice = "posting unavailable: " + reason
		return m, nil
	}
	i := slices.IndexFunc(m.drawer.reviews, func(r store.Review) bool { return r.ID == reviewID })
	if i < 0 {
		return m, nil
	}
	rv := m.drawer.reviews[i]
	sel := post.DefaultSelection(rv)
	pick := slices.IndexFunc(postEvents, func(e postEvent) bool { return e.event == sel.Event })
	m.post = &postFlow{stage: postLoading, review: rv, sel: sel, pick: max(pick, 0)}
	st := m.store
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), inputsTimeout)
		defer cancel()
		ps, err := st.PostedFor(ctx, reviewID)
		return postPriorMsg{ReviewID: reviewID, Posted: ps, Err: err}
	}
}

// on reports whether the open flow is still on reviewID, so a late
// result isn't applied to another review.
func (p *postFlow) on(reviewID string) bool { return p != nil && p.review.ID == reviewID }

// updatePost handles the post flow's messages.
func (m Model) updatePost(msg tea.Msg) (_ Model, _ tea.Cmd, ok bool) {
	switch msg := msg.(type) {
	case postPriorMsg:
		if !m.post.on(msg.ReviewID) || m.post.stage != postLoading {
			return m, nil, true
		}
		if msg.Err != nil {
			m.post, m.notice = nil, "could not read earlier posts: "+msg.Err.Error()
			return m, nil, true
		}
		p := *m.post
		p.known = map[string]bool{}
		for _, ps := range msg.Posted {
			p.known[postKey(ps)] = true
		}
		var last *priorPost
		if len(msg.Posted) > 0 {
			last = &priorPost{event: msg.Posted[0].Event, at: msg.Posted[0].At}
		}
		// A post only the model knows of counts as posted all the same.
		if u, ok := m.unconfirmedPosts[msg.ReviewID]; ok && (last == nil || u.At.After(last.at)) {
			last = &priorPost{event: u.Event, at: u.At, uncertain: u.Kind == postUncertain}
		}
		p.stage = postPick
		if last != nil {
			p.ask, p.stage = *last, postRepost
		}
		m.post = &p
	case postEditReadyMsg:
		return m.runEditor(msg)
	case postEditedMsg:
		if !m.post.on(msg.ReviewID) || !m.post.editing {
			return m, nil, true
		}
		p := *m.post
		p.editing = false
		if msg.Err != nil {
			p.err = "could not run the editor: " + msg.Err.Error()
		} else {
			p.sel.Body, p.err = strings.TrimRight(msg.Body, "\n"), ""
			p.rebuild()
		}
		p.previewAt = m.now()
		m.post = &p
	case postDoneMsg:
		next, cmd := m.finishPost(msg)
		return next, cmd, true
	default:
		return m, nil, false
	}
	return m, nil, true
}

// postCapture takes every key while the flow is open; ok is false for a
// ctrl+c that should go on to quit. A held key's repeats are dropped, so
// only deliberate presses move the flow toward posting, and the first
// ctrl+c while posting only warns: the post can't be taken back.
func (m Model) postCapture(msg tea.KeyPressMsg) (_ Model, _ tea.Cmd, ok bool) {
	if msg.IsRepeat {
		return m, nil, true
	}
	// The help overlay over the modal takes every key but ctrl+c, which
	// quits as it does from the modal.
	if m.help {
		switch msg.String() {
		case "ctrl+c":
			return m, nil, false
		case "?", "esc":
			m.help = false
		}
		return m, nil, true
	}
	if msg.String() == "?" && !m.post.inFlight && !m.post.editing &&
		(m.post.stage == postPick || m.post.stage == postPreview) {
		m.help = true
		return m, nil, true
	}
	if msg.String() != "ctrl+c" {
		next, cmd := m.postKey(msg)
		return next, cmd, true
	}
	if m.post.inFlight && !m.post.quitArmed {
		p := *m.post
		p.quitArmed = true
		m.post, m.notice = &p, postQuitWarning
		return m, nil, true
	}
	return m, nil, false
}

func (m Model) postKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := *m.post
	k := msg.String()
	switch p.stage {
	case postLoading:
		if k == "esc" {
			m.post = nil
			return m, nil
		}
	case postRepost:
		switch k {
		case "y", "Y":
			p.stage = postPick
		case "n", "N", "esc":
			m.post = nil
			return m, nil
		}
	case postPick:
		switch k {
		case "j", "down":
			p.pick = min(p.pick+1, len(postEvents)-1)
		case "k", "up":
			p.pick = max(p.pick-1, 0)
		case "enter":
			p.sel.Event, p.stage, p.scroll, p.err = postEvents[p.pick].event, postPreview, 0, ""
			p.previewAt = m.now()
			p.rebuild()
		case "esc":
			m.post = nil
			return m, nil
		}
	case postPreview:
		if p.inFlight || p.editing {
			return m, nil
		}
		return m.previewKey(p, k)
	case postResult:
		if k == "esc" || k == "enter" {
			m.post = nil
			return m, nil
		}
	}
	m.post = &p
	return m, nil
}

func (m Model) previewKey(p postFlow, k string) (Model, tea.Cmd) {
	fs := orderedFindings(p.review.Findings)
	follow := false
	p.holdOn = false
	switch k {
	case "j", "down":
		p.cursor, follow = min(p.cursor+1, max(len(fs)-1, 0)), true
	case "k", "up":
		p.cursor, follow = max(p.cursor-1, 0), true
	case "space":
		if p.cursor < len(fs) {
			id := fs[p.cursor].ID
			p.sel.Include = maps.Clone(p.sel.Include)
			if p.sel.Include == nil {
				p.sel.Include = map[int64]bool{}
			}
			p.sel.Include[id] = !included(p.sel, id)
			p.previewAt = m.now()
			p.rebuild()
		}
	case "m":
		p.raw = !p.raw
	case "ctrl+d", "ctrl+u":
		_, h := m.bodySize()
		step := max(h/2, 1)
		if k == "ctrl+u" {
			step = -step
		}
		p.scroll += step
	case "e":
		p.editing = true
		m.post = &p
		return m, writeEditFile(m.dataDir, p.review.ID, p.sel.Body)
	case "enter":
		if m.now().Sub(p.previewAt) < postConfirmDelay {
			p.previewAt, p.holdOn = m.now(), true
			break
		}
		in, pv, err := p.built.in, p.built.pv, p.built.err
		if err != nil {
			break // the preview shows why
		}
		if p.mustAsk {
			u := m.unconfirmedPosts[p.review.ID]
			p.ask = priorPost{event: u.Event, at: u.At, uncertain: true}
			p.stage, p.mustAsk = postRepost, false
			m.post = &p
			return m, nil
		}
		// Each post gets its own ctrl+c guard.
		p.inFlight, p.quitArmed, p.err = true, false, ""
		m.post = &p
		return m, m.postCmd(p.review, p.sel, in, pv.ThreadFindings, p.known)
	case "esc":
		p.stage, p.err = postPick, ""
		m.post = &p
		return m, nil
	}
	m.post = &p
	m.post.scroll = m.clampPostScroll(follow)
	return m, nil
}

// clampPostScroll keeps the preview's scroll in range and, with follow,
// the checklist cursor on screen.
func (m Model) clampPostScroll(follow bool) int {
	w, h := m.bodySize()
	top, body, cursorRow, rows := m.postFrame(w, h)
	bodyH := max(rows-len(top), 1)
	s := m.post.scroll
	if follow {
		s = min(max(s, cursorRow-bodyH+1), cursorRow)
	}
	return min(max(s, 0), max(len(body)-bodyH, 0))
}

func included(sel post.Selection, id int64) bool {
	inc, ok := sel.Include[id]
	return !ok || inc
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// writeEditFile puts the summary in <data>/tmp for the editor.
func writeEditFile(dataDir, reviewID, body string) tea.Cmd {
	return func() tea.Msg {
		dir := filepath.Join(dataDir, "tmp")
		path := filepath.Join(dir, "post-"+unsafeName.ReplaceAllString(reviewID, "_")+".md")
		err := os.MkdirAll(dir, 0o700)
		if err == nil {
			// A file left by a run that died with the editor open is
			// replaced, never written through.
			_ = os.Remove(path)
			var f *os.File
			if f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
				_, err = f.WriteString(body)
				if cerr := f.Close(); err == nil {
					err = cerr
				}
			}
		}
		if err != nil {
			_ = os.Remove(path)
			return postEditReadyMsg{ReviewID: reviewID, Err: err}
		}
		return postEditReadyMsg{ReviewID: reviewID, Path: path}
	}
}

// runEditor opens the written file in the editor. The file is read back
// and removed in the callback whatever the editor did; one left over from a
// flow that closed meanwhile is removed at once.
func (m Model) runEditor(msg postEditReadyMsg) (Model, tea.Cmd, bool) {
	path, id := msg.Path, msg.ReviewID
	if !m.post.on(id) || !m.post.editing {
		if path == "" {
			return m, nil, true
		}
		return m, func() tea.Msg { _ = os.Remove(path); return nil }, true
	}
	if msg.Err != nil {
		p := *m.post
		p.editing, p.err = false, "could not start the editor: "+msg.Err.Error()
		m.post = &p
		return m, nil, true
	}
	return m, m.execProcess(editorCommand(path), func(err error) tea.Msg {
		b, readErr := os.ReadFile(path)
		_ = os.Remove(path)
		if err == nil {
			err = readErr
		}
		return postEditedMsg{ReviewID: id, Body: string(b), Err: err}
	}), true
}

// postCmd posts in, then records it. It first reads the review's posts
// again, and sends nothing if one was recorded since the flow asked
// (known). A refused post records nothing. Once GitHub has the review the
// record is written at once, before the thread lookup, whatever the modal
// does meanwhile; the threads follow in a second write.
func (m Model) postCmd(rv store.Review, sel post.Selection, in github.PostInput, threadFindings []int64, known map[string]bool) tea.Cmd {
	poster, st, now := m.poster, m.store, m.now
	return func() tea.Msg {
		done := postDoneMsg{ReviewID: rv.ID, PRID: rv.PRID, Event: in.Event, Body: in.Body, NewThreads: len(in.Threads)}
		cctx, ccancel := context.WithTimeout(context.Background(), inputsTimeout)
		ps, err := st.PostedFor(cctx, rv.ID)
		ccancel()
		if err != nil {
			done.Err = &github.RefusedError{Err: fmt.Errorf("could not check earlier posts: %w", err)}
			return done
		}
		for _, p := range ps {
			if !known[postKey(p)] {
				done.Raced = &p
				return done
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), postTimeout)
		defer cancel()
		res, err := poster.PostReview(ctx, in)
		if err != nil {
			done.Err = err
			return done
		}
		done.Posted = store.Posted{
			ReviewID: rv.ID, GitHubReviewID: res.ReviewID, URL: res.URL, Event: in.Event,
			CommitOID: in.CommitOID, FindingIDs: includedIDs(rv, sel), At: now(),
		}
		rctx, rcancel := context.WithTimeout(context.Background(), recordTimeout)
		done.RecordErr = st.RecordPost(rctx, done.Posted, nil)
		rcancel()
		if len(res.Comments) == 0 {
			return done
		}
		ids := make([]string, len(res.Comments))
		for i, c := range res.Comments {
			ids[i] = c.ID
		}
		threadOf, err := poster.ThreadIDs(ctx, in.PRID, ids)
		if err == nil && done.RecordErr == nil {
			sctx, scancel := context.WithTimeout(context.Background(), recordTimeout)
			if err = st.SetThreadIDs(sctx, rv.ID, matchThreads(in, threadFindings, res.Comments, threadOf)); err != nil {
				err = fmt.Errorf("save them: %w", err)
			}
			scancel()
		}
		done.ThreadErr = err
		return done
	}
}

func includedIDs(rv store.Review, sel post.Selection) []int64 {
	var ids []int64
	for _, f := range rv.Findings {
		if included(sel, f.ID) {
			ids = append(ids, f.ID)
		}
	}
	return ids
}

// matchThreads maps each posted comment back to the finding behind its
// thread, by path and the line it was posted on, in payload order, then to
// the comment's thread. findingIDs runs parallel to in.Threads. GitHub's
// line is where the comment sits at head, which is shifted or gone for a
// review of an older commit, so it is only the fallback.
func matchThreads(in github.PostInput, findingIDs []int64, comments []github.PostedComment, threadOf map[string]string) map[int64]string {
	type at struct {
		path string
		line int
	}
	waiting := map[at][]int64{}
	for i, t := range in.Threads {
		k := at{t.Path, t.Line}
		waiting[k] = append(waiting[k], findingIDs[i])
	}
	out := map[int64]string{}
	for _, c := range comments {
		line := c.OriginalLine
		if line == 0 {
			line = c.Line
		}
		k := at{c.Path, line}
		ids := waiting[k]
		if len(ids) == 0 {
			continue
		}
		waiting[k] = ids[1:]
		if t := threadOf[c.ID]; t != "" {
			out[ids[0]] = t
		}
	}
	return out
}

// finishPost applies a post's result. A refusal goes back to the preview
// with the selections kept; a failure that may have posted does too, but
// the next enter asks first. A post recorded meanwhile asks the repost
// question. A post that landed moves the card at once and polls so
// GitHub's view replaces the assumed one, and a record that failed after
// it stays on screen.
func (m Model) finishPost(msg postDoneMsg) (Model, tea.Cmd) {
	current := m.post.on(msg.ReviewID) && m.post.inFlight
	if m.notice == postQuitWarning {
		m.notice = ""
	}
	if r := msg.Raced; r != nil {
		if current {
			p := *m.post
			p.inFlight, p.quitArmed = false, false
			p.known = maps.Clone(p.known)
			if p.known == nil {
				p.known = map[string]bool{}
			}
			p.known[postKey(*r)] = true
			p.ask, p.stage = priorPost{event: r.Event, at: r.At}, postRepost
			m.post = &p
		} else {
			m.notice = "not posted: it was posted meanwhile, " + postedAgo(m.now().Sub(r.At))
		}
		return m, nil
	}
	if msg.Err != nil {
		refused := github.IsRefused(msg.Err)
		text := "post failed: " + msg.Err.Error()
		if !refused {
			text = uncertainPost + msg.Err.Error()
			m = m.withUnconfirmed(msg.ReviewID, unconfirmedPost{Event: msg.Event, At: m.now(), Kind: postUncertain})
			// It may well be on GitHub, so the card stops calling it Drafted.
			m = m.markPosted(msg.ReviewID)
		}
		if current {
			p := *m.post
			p.inFlight, p.quitArmed, p.err = false, false, text
			if !refused {
				p.mustAsk = true
			}
			m.post = &p
		} else {
			m.notice = text
		}
		return m, nil
	}
	if msg.RecordErr != nil {
		m = m.withUnconfirmed(msg.ReviewID, unconfirmedPost{Event: msg.Posted.Event, At: msg.Posted.At, Kind: postUnrecorded})
	}
	// The card drops Drafted and moves to its new lane now; the poll below
	// brings GitHub's view once it has one.
	m = m.markPosted(msg.ReviewID)
	m = m.assumePosted(msg)
	var reload tea.Cmd
	if m.drawer.prID == msg.PRID {
		m.drawer = m.drawer.withPosted(msg.Posted)
		if msg.RecordErr == nil {
			m, reload = m.loadDrawer(msg.PRID)
		}
	}
	// The PR's status catches up on the next poll. One already running
	// started before the post, so another follows it.
	var poll tea.Cmd
	if m.polling {
		m.pollAgain = true
	} else {
		m, poll = m.startPoll()
	}
	switch {
	case msg.RecordErr != nil:
		text := "posted to GitHub, but saving the record failed: " + msg.RecordErr.Error() + " — " + msg.Posted.URL
		if current {
			m.post = &postFlow{stage: postResult, review: m.post.review, result: text}
		} else {
			m.notice = text
		}
	default:
		if current {
			m.post = nil
		}
		if msg.ThreadErr != nil {
			m.notice = "posted, but could not look up its threads: " + msg.ThreadErr.Error()
		}
	}
	m, relog := m.reloadReviewLog()
	m, inputs := m.loadReviewInputs()
	return m, tea.Batch(reload, poll, relog, inputs)
}

// withUnconfirmed remembers a post the store has no row for.
func (m Model) withUnconfirmed(reviewID string, u unconfirmedPost) Model {
	m.unconfirmedPosts = maps.Clone(m.unconfirmedPosts)
	if m.unconfirmedPosts == nil {
		m.unconfirmedPosts = map[string]unconfirmedPost{}
	}
	m.unconfirmedPosts[reviewID] = u
	return m
}

// withUnrecorded adds the posts whose record failed to a drawer load, so
// the drawer keeps saying they were posted.
func (m Model) withUnrecorded(msg drawerLoadedMsg) drawerLoadedMsg {
	for _, r := range msg.Reviews {
		u, ok := m.unconfirmedPosts[r.ID]
		if !ok || u.Kind != postUnrecorded {
			continue
		}
		if p, ok := msg.Posted[r.ID]; ok && !u.At.After(p.At) {
			continue
		}
		msg.Posted = maps.Clone(msg.Posted)
		if msg.Posted == nil {
			msg.Posted = map[string]store.Posted{}
		}
		msg.Posted[r.ID] = store.Posted{ReviewID: r.ID, Event: u.Event, At: u.At}
	}
	return msg
}

func (d drawerModel) withPosted(p store.Posted) drawerModel {
	posted := maps.Clone(d.posted)
	if posted == nil {
		posted = map[string]store.Posted{}
	}
	posted[p.ReviewID] = p
	d.posted = posted
	return d
}
