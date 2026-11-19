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
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

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
)

const headMovedWarning = "PR has new commits since this review — comments will show as outdated"

// postFlow is the open post modal. Like the picker it is replaced, never
// written in place, since older copies of the model share it.
type postFlow struct {
	stage  postStage
	review store.Review // a copy, so a drawer reload can't change what posts
	prior  []store.Posted
	pick   int // index into postEvents
	sel    post.Selection
	cursor int // the checklist row, in orderedFindings order
	scroll int
	raw    bool
	// inFlight is set from the confirming enter until the post's result
	// arrives; every key but ctrl+c is ignored meanwhile, so a second
	// enter sends nothing. editing is the same for the editor.
	inFlight, editing bool
	err               string // the last post or editor failure
	result            string // postResult's text
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
	// postDoneMsg ends a post. Err means GitHub refused it and nothing was
	// recorded; otherwise it posted, and RecordErr says whether the record
	// was saved.
	postDoneMsg struct {
		ReviewID, PRID string
		Posted         store.Posted
		Err            error
		ThreadErr      error
		RecordErr      error
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
		p.prior, p.stage = msg.Posted, postPick
		if len(msg.Posted) > 0 {
			p.stage = postRepost
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
		}
		m.post = &p
	case postDoneMsg:
		next, cmd := m.finishPost(msg)
		return next, cmd, true
	default:
		return m, nil, false
	}
	return m, nil, true
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
		in, _, err := post.Build(p.review, p.review.PRID, p.sel)
		if err != nil {
			break // the preview shows why
		}
		p.inFlight, p.err = true, ""
		m.post = &p
		return m, m.postCmd(p.review, p.sel, in)
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
			err = os.WriteFile(path, []byte(body), 0o600)
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

// postCmd posts in, then records it. A refused post records nothing. Once
// GitHub has the review the record is always attempted, whatever the
// modal does meanwhile.
func (m Model) postCmd(rv store.Review, sel post.Selection, in github.PostInput) tea.Cmd {
	poster, st, now := m.poster, m.store, m.now
	return func() tea.Msg {
		done := postDoneMsg{ReviewID: rv.ID, PRID: rv.PRID}
		ctx, cancel := context.WithTimeout(context.Background(), postTimeout)
		defer cancel()
		res, err := poster.PostReview(ctx, in)
		if err != nil {
			done.Err = err
			return done
		}
		var threadOf map[string]string
		if len(res.Comments) > 0 {
			ids := make([]string, len(res.Comments))
			for i, c := range res.Comments {
				ids[i] = c.ID
			}
			threadOf, done.ThreadErr = poster.ThreadIDs(ctx, in.PRID, ids)
		}
		done.Posted = store.Posted{
			ReviewID: rv.ID, GitHubReviewID: res.ReviewID, URL: res.URL, Event: in.Event,
			CommitOID: in.CommitOID, FindingIDs: includedIDs(rv, sel), At: now(),
		}
		rctx, rcancel := context.WithTimeout(context.Background(), recordTimeout)
		defer rcancel()
		done.RecordErr = st.RecordPost(rctx, done.Posted, matchThreads(rv, sel, res.Comments, threadOf))
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

// matchThreads maps each posted comment back to its finding by path and
// line, taking findings in payload order, then to the comment's thread.
func matchThreads(rv store.Review, sel post.Selection, comments []github.PostedComment, threadOf map[string]string) map[int64]string {
	type at struct {
		path string
		line int
	}
	waiting := map[at][]int64{}
	for _, f := range rv.Findings {
		if included(sel, f.ID) && !f.Unanchored {
			k := at{post.Sanitize(f.Path), f.Line}
			waiting[k] = append(waiting[k], f.ID)
		}
	}
	out := map[int64]string{}
	for _, c := range comments {
		k := at{c.Path, c.Line}
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
// with the selections kept; a post that landed polls now so the PR's
// status catches up, and a record that failed after it stays on screen.
func (m Model) finishPost(msg postDoneMsg) (Model, tea.Cmd) {
	current := m.post.on(msg.ReviewID) && m.post.inFlight
	if msg.Err != nil {
		text := "post failed: " + msg.Err.Error()
		if current {
			p := *m.post
			p.inFlight, p.err = false, text
			m.post = &p
		} else {
			m.notice = text
		}
		return m, nil
	}
	var reload tea.Cmd
	if m.drawer.prID == msg.PRID {
		m.drawer = m.drawer.withPosted(msg.Posted)
		if msg.RecordErr == nil {
			m, reload = m.loadDrawer(msg.PRID)
		}
	}
	m, poll := m.startPoll()
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
	return m, tea.Batch(reload, poll)
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

// postedAgo is "just now" for the first minute, then "3m ago".
func postedAgo(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	return ago(d) + " ago"
}

func (p postFlow) hints() []keyHint {
	switch p.stage {
	case postLoading:
		return postLoadingKeys
	case postRepost:
		return postRepostKeys
	case postPick:
		return postPickKeys
	case postResult:
		return postResultKeys
	}
	if p.inFlight {
		return postPostingKeys
	}
	return postPreviewKeys(p.raw)
}

// ---- rendering ----

// postBody draws the modal over the view.
func (m Model) postBody(width, height int) string {
	top, body, _, rows := m.postFrame(width, height)
	inner := postInner(width)
	bodyH := max(rows-len(top), 0)
	scroll := min(max(m.post.scroll, 0), max(len(body)-bodyH, 0))
	lines := append(slices.Clone(top), body[min(scroll, len(body)):]...)
	lines = lines[:min(len(lines), rows)]
	for i, l := range lines {
		l = ansi.Truncate(l, inner, "")
		lines[i] = l + strings.Repeat(" ", max(inner-ansi.StringWidth(l), 0))
	}
	box := m.theme.Border(true).Padding(0, 2).Render(strings.Join(lines, "\n"))
	return lipgloss.Place(max(width, 0), max(height, 0), lipgloss.Center, lipgloss.Center, box)
}

// postInner is the content width inside the box's border and padding.
func postInner(width int) int { return max(min(width, postMaxWidth)-6, 10) }

// postFrame lays the modal out: the lines that stay put, the lines that
// scroll, the scrolling line the cursor is on, and the rows inside the box.
func (m Model) postFrame(width, height int) (top, body []string, cursorRow, rows int) {
	p, th := m.post, m.theme
	inner := postInner(width)
	rows = max(height-2, 1)
	wrap := func(s string, style lipgloss.Style) []string {
		var out []string
		for _, l := range strings.Split(ansi.Wrap(s, inner, ""), "\n") {
			out = append(out, style.Render(l))
		}
		return out
	}
	title := "Post review · " + cleanLine(p.review.Repo) + "#" + strconv.Itoa(p.review.Number)

	switch p.stage {
	case postLoading:
		return []string{th.Accent().Render(title), "", th.Muted().Render("Checking earlier posts…")}, nil, 0, rows
	case postRepost:
		last := p.prior[0]
		q := fmt.Sprintf("Already posted as %s %s — post again? y/n", cleanLine(last.Event), postedAgo(m.now().Sub(last.At)))
		return append([]string{th.Accent().Render(title), ""}, wrap(q, th.Accent())...), nil, 0, rows
	case postResult:
		return append([]string{th.Accent().Render(title), ""}, wrap(cleanLine(p.result), th.StatusFg("CI_FAILED"))...), nil, 0, rows
	case postPick:
		top = []string{th.Accent().Render(title), ""}
		for i, e := range postEvents {
			mark := "  "
			if i == p.pick {
				mark = th.Accent().Render("▸ ")
			}
			top = append(top, mark+e.label)
		}
		return top, nil, 0, rows
	}

	_, pv, err := post.Build(p.review, p.review.PRID, p.sel)
	event, commit := string(p.sel.Event), shortCommit(p.review.HeadOID)
	if err == nil {
		event, commit = string(pv.Event), pv.Commit
	}
	top = []string{th.Accent().Render(title + " · " + event + " · " + cleanLine(commit))}
	if t, ok := m.prs[p.review.PRID]; ok && t.PR.HeadOID != "" && t.PR.HeadOID != p.review.HeadOID {
		top = append(top, wrap(headMovedWarning, th.StatusFg("CI_WAITING"))...)
	}
	if err != nil {
		top = append(top, wrap(cleanLine(err.Error()), th.StatusFg("CI_FAILED"))...)
	}
	if p.err != "" {
		top = append(top, wrap(cleanLine(p.err), th.StatusFg("CI_FAILED"))...)
	}
	if p.inFlight {
		top = append(top, th.Accent().Render("Posting to GitHub…"))
	}
	if len(pv.Hidden) > 0 {
		items := make([]string, len(pv.Hidden))
		for i, h := range pv.Hidden {
			items[i] = cleanLine(h)
		}
		top = append(top, wrap("Rendered view hides: "+strings.Join(items, "; ")+" — press m to see the raw text", th.StatusFg("CI_WAITING"))...)
	}
	top = append(top, "")

	fs := orderedFindings(p.review.Findings)
	if len(fs) > 0 {
		body = append(body, th.Accent().Render("Findings"))
	}
	for i, f := range fs {
		mark, box := "  ", "[ ] "
		if i == p.cursor {
			mark, cursorRow = th.Accent().Render("▸ "), len(body)
		}
		if included(p.sel, f.ID) {
			box = "[x] "
		}
		row := mark + box + findingLocation(f)
		for _, l := range ledgerLabels[:3] {
			if f.Ledger == l.ledger {
				row += th.Muted().Render("  [" + l.label + "]")
			}
		}
		if f.ThreadID != "" {
			row += th.Muted().Render("  [posted]")
		}
		body = append(body, ansi.Truncate(row, inner, "…"))
	}
	if len(fs) > 0 {
		body = append(body, "")
	}
	if err == nil && len(pv.Parts) == 0 {
		body = append(body, th.Muted().Render("No summary or comments: only the event is posted."))
	}
	// Each part is drawn from the payload's own string, so what shows is
	// what is sent.
	for _, part := range pv.Parts {
		body = append(body, th.Accent().Render(ansi.Truncate(cleanLine(part.Label), inner, "…")))
		var ls []string
		if p.raw {
			ls = strings.Split(ansi.Wrap(cleanBlock(part.Markdown), inner-2, ""), "\n")
		} else {
			ls = m.drawer.md.render(part.Markdown, inner-2, th)
		}
		for _, l := range ls {
			body = append(body, "  "+l)
		}
		body = append(body, "")
	}
	return top, body, cursorRow, rows
}

func shortCommit(oid string) string { return oid[:min(len(oid), 7)] }
