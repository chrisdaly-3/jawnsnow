package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
)

// fakePoster records every post and answers with result, or fails with err.
// Commands run on the test goroutine, so it needs no lock.
type fakePoster struct {
	posts     []github.PostInput
	err       error
	result    github.PostResult
	threads   map[string]string
	threadIDs [][]string
}

func (f *fakePoster) PostReview(_ context.Context, in github.PostInput) (github.PostResult, error) {
	f.posts = append(f.posts, in)
	if f.err != nil {
		return github.PostResult{}, f.err
	}
	return f.result, nil
}

func (f *fakePoster) ThreadIDs(_ context.Context, _ string, ids []string) (map[string]string, error) {
	f.threadIDs = append(f.threadIDs, slices.Clone(ids))
	return f.threads, nil
}

type execCall struct {
	cmd *exec.Cmd
	fn  tea.ExecCallback
}

type postH struct {
	*harness
	poster *fakePoster
	execs  []execCall
}

// postReview is drawerReview on the harness's PR "1", reviewed at its
// current head.
func postReview() store.Review {
	rv := drawerReview()
	rv.PRID, rv.HeadOID = "1", "head-1"
	return rv
}

// newPostHarness tracks PR "1", stores rv, and opens the drawer on it with
// the drawer focused. The editor never runs: execs records it instead.
func newPostHarness(t *testing.T, rv store.Review, o opts) *postH {
	t.Helper()
	fp := &fakePoster{
		result: github.PostResult{
			ReviewID: "PRR_1", URL: "https://github.com/acme/widgets/pull/1#pullrequestreview-1",
			Comments: []github.PostedComment{{ID: "PRRC_1", Path: "pkg/policy/decision.go", Line: 12}},
		},
		threads: map[string]string{"PRRC_1": "PRRT_1"},
	}
	if o.postOff == "" {
		o.poster = fp
	}
	h := &postH{harness: newHarness(t, o, wallPR("1")), poster: fp}
	h.finishPoll(t)
	if err := h.store.SaveReview(context.Background(), rv); err != nil {
		t.Fatal(err)
	}
	h.m.execProcess = func(c *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
		h.execs = append(h.execs, execCall{c, fn})
		return nil
	}
	h.m, _ = settle(t, h.m, tea.WindowSizeMsg{Width: 160, Height: 60})
	h.press(t, "enter", "tab")
	settleDrawer(t, h.harness)
	return h
}

// modalText is the frame without styling or box lines, its whitespace
// collapsed, so a check doesn't depend on where a line wraps.
func modalText(m Model) string {
	s := ansi.Strip(content(m))
	s = strings.NewReplacer("│", " ", "╭", " ", "╮", " ", "╰", " ", "╯", " ", "─", " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

func ctrl(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl} }

// wait lets the confirm delay pass, so the next enter in the preview posts.
func (h *postH) wait() { h.clock.now = h.clock.now.Add(postConfirmDelay) }

func repeat(s string) tea.KeyPressMsg {
	k := key(s)
	k.IsRepeat = true
	return k
}

func TestPostFlowHappyPath(t *testing.T) {
	h := newPostHarness(t, postReview(), opts{})
	h.press(t, "P")
	if h.m.post == nil || h.m.post.stage != postPick {
		t.Fatalf("P: post flow %+v, want the picker", h.m.post)
	}
	// request_changes is highlighted; k moves up to Comment.
	if got := modalText(h.m); !strings.Contains(got, "▸ Request changes") {
		t.Errorf("the verdict's event is not highlighted:\n%s", got)
	}
	h.press(t, "k", "enter")
	if h.m.post.stage != postPreview || h.m.post.sel.Event != "COMMENT" {
		t.Fatalf("enter: stage %v event %q, want the preview of a COMMENT", h.m.post.stage, h.m.post.sel.Event)
	}
	// The cursor is on the first finding the drawer lists, lookup.go:40;
	// the fixed ones are left out already.
	h.press(t, "space")
	h.wait()
	h.press(t, "enter")

	if len(h.poster.posts) != 1 {
		t.Fatalf("PostReview called %d times, want 1", len(h.poster.posts))
	}
	want := github.PostInput{
		PRID: "1", CommitOID: "head-1", Event: "COMMENT", Body: "",
		Threads: []github.PostThread{{Path: "pkg/policy/decision.go", Line: 12, Body: "nit: rename `x` to `repoName`."}},
	}
	if got := h.poster.posts[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("posted %#v\nwant %#v", got, want)
	}
	if got := h.poster.threadIDs; len(got) != 1 || !slices.Equal(got[0], []string{"PRRC_1"}) {
		t.Errorf("ThreadIDs asked for %v, want [[PRRC_1]]", got)
	}

	posted, err := h.store.PostedFor(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(posted) != 1 {
		t.Fatalf("%d posted rows, want 1", len(posted))
	}
	p := posted[0]
	if p.Event != "COMMENT" || p.GitHubReviewID != "PRR_1" || p.URL != h.poster.result.URL || p.CommitOID != "head-1" ||
		!slices.Equal(p.FindingIDs, []int64{1}) || !p.At.Equal(h.clock.now) {
		t.Errorf("posted row = %+v", p)
	}
	rs, err := h.store.Reviews(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range rs[0].Findings {
		if want := map[int64]string{1: "PRRT_1"}[f.ID]; f.ThreadID != want {
			t.Errorf("finding %d thread = %q, want %q", f.ID, f.ThreadID, want)
		}
	}

	if h.m.post != nil {
		t.Errorf("the modal is still open: %+v", h.m.post)
	}
	if got := modalText(h.m); !strings.Contains(got, "Posted as Comment · just now") {
		t.Errorf("the drawer does not say it was posted:\n%s", got)
	}
	if h.client.polls != 2 {
		t.Errorf("%d polls, want a second one after the post", h.client.polls)
	}
}

func TestConfirmTwiceSendsOnce(t *testing.T) {
	h := newPostHarness(t, postReview(), opts{})
	h.press(t, "P", "k", "enter")
	h.wait()
	m, first := update(t, h.m, key("enter"))
	m, second := update(t, m, key("enter"))
	msgs, _ := runCmd(t, first)
	more, _ := runCmd(t, second)
	h.m, _ = settle(t, m, append(msgs, more...)...)
	if n := len(h.poster.posts); n != 1 {
		t.Fatalf("two enters posted %d times, want 1", n)
	}
	if posted, _ := h.store.PostedFor(context.Background(), "r1"); len(posted) != 1 {
		t.Errorf("%d posted rows, want 1", len(posted))
	}
}

func TestPostErrorRecordsNothing(t *testing.T) {
	h := newPostHarness(t, postReview(), opts{})
	h.poster.err = errors.New("github: post review: Line could not be resolved")
	h.press(t, "P", "k", "enter", "space")
	h.wait()
	h.press(t, "enter")

	if h.m.post == nil || h.m.post.stage != postPreview || h.m.post.inFlight {
		t.Fatalf("after a failed post: %+v, want the preview open and not posting", h.m.post)
	}
	if got := modalText(h.m); !strings.Contains(got, "Line could not be resolved") {
		t.Errorf("GitHub's message is not shown:\n%s", got)
	}
	if inc, ok := h.m.post.sel.Include[2]; !ok || inc {
		t.Errorf("the selection was lost: Include = %v", h.m.post.sel.Include)
	}
	if h.m.post.sel.Event != "COMMENT" {
		t.Errorf("event = %q, want COMMENT kept", h.m.post.sel.Event)
	}
	if posted, _ := h.store.PostedFor(context.Background(), "r1"); len(posted) != 0 {
		t.Errorf("%d posted rows after a failed post, want 0", len(posted))
	}
	if len(h.poster.threadIDs) != 0 {
		t.Errorf("ThreadIDs was asked after a failed post")
	}
	// It can be sent again once fixed.
	h.poster.err = nil
	h.press(t, "enter")
	if len(h.poster.posts) != 2 || h.m.post != nil {
		t.Errorf("retry: %d posts, modal %+v; want 2 and closed", len(h.poster.posts), h.m.post)
	}
}

func TestRepostAsksFirst(t *testing.T) {
	h := newPostHarness(t, postReview(), opts{})
	prior := store.Posted{ReviewID: "r1", GitHubReviewID: "PRR_0", Event: "APPROVE", CommitOID: "head-1", At: t0.Add(-2 * time.Hour)}
	if err := h.store.RecordPost(context.Background(), prior, nil); err != nil {
		t.Fatal(err)
	}
	h.press(t, "P")
	if got := modalText(h.m); !strings.Contains(got, "Already posted as Approve 2h ago — post again? y/n") {
		t.Fatalf("no repost question:\n%s", got)
	}
	h.press(t, "n")
	if h.m.post != nil {
		t.Fatalf("n: modal %+v, want closed", h.m.post)
	}
	h.press(t, "P", "y")
	if h.m.post == nil || h.m.post.stage != postPick {
		t.Fatalf("y: %+v, want the picker", h.m.post)
	}
	if len(h.poster.posts) != 0 {
		t.Errorf("asking posted %d times", len(h.poster.posts))
	}
}

func TestHeadMovedWarning(t *testing.T) {
	rv := postReview()
	rv.HeadOID = "0ld0ld0ld"
	h := newPostHarness(t, rv, opts{})
	h.press(t, "P", "k", "enter")
	if got := modalText(h.m); !strings.Contains(got, "PR has new commits since this review — comments will show as outdated") {
		t.Errorf("no head-moved warning:\n%s", got)
	}
	h.wait()
	h.press(t, "enter")
	if len(h.poster.posts) != 1 || h.poster.posts[0].CommitOID != "0ld0ld0ld" {
		t.Errorf("posts = %+v, want one against the reviewed commit", h.poster.posts)
	}

	// At the PR's head there is no warning.
	h = newPostHarness(t, postReview(), opts{})
	h.press(t, "P", "k", "enter")
	if got := modalText(h.m); strings.Contains(got, "PR has new commits") {
		t.Errorf("a warning at the PR's head:\n%s", got)
	}
}

func TestRequestChangesBlocksWithoutBody(t *testing.T) {
	h := newPostHarness(t, postReview(), opts{})
	h.press(t, "P", "enter") // Request changes, highlighted from the verdict
	if got := modalText(h.m); !strings.Contains(got, "Request changes needs a summary — press e to write one") {
		t.Errorf("no validation error:\n%s", got)
	}
	h.wait()
	h.press(t, "enter")
	if len(h.poster.posts) != 0 || h.m.post == nil || h.m.post.inFlight {
		t.Errorf("enter with a validation error: %d posts, flow %+v", len(h.poster.posts), h.m.post)
	}
}

func TestEditBodyUsesTempFileAndCleansUp(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "my-editor --wait")
	h := newPostHarness(t, postReview(), opts{})
	h.press(t, "P", "enter", "e")
	if len(h.execs) != 1 {
		t.Fatalf("e ran the editor %d times, want 1", len(h.execs))
	}
	path := filepath.Join(h.m.dataDir, "tmp", "post-r1.md")
	if got, want := h.execs[0].cmd.Args, []string{"my-editor", "--wait", path}; !slices.Equal(got, want) {
		t.Errorf("editor command = %v, want %v", got, want)
	}
	if b, err := os.ReadFile(path); err != nil || len(b) != 0 {
		t.Errorf("the editor's file: %q, %v; want it empty, the summary starting blank", b, err)
	}
	if err := os.WriteFile(path, []byte("Two **blocking** problems.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.m, _ = settle(t, h.m, h.execs[0].fn(nil))
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the file is still there: %v", err)
	}
	if got := h.m.post.sel.Body; got != "Two **blocking** problems." {
		t.Errorf("body = %q", got)
	}
	if got := modalText(h.m); strings.Contains(got, "needs a summary") || !strings.Contains(got, "Two blocking problems.") {
		t.Errorf("the preview doesn't show the summary:\n%s", got)
	}

	// The editor failing still removes the file and keeps the body.
	h.press(t, "e")
	if len(h.execs) != 2 {
		t.Fatalf("second e ran the editor %d times in all, want 2", len(h.execs))
	}
	if b, _ := os.ReadFile(path); string(b) != "Two **blocking** problems." {
		t.Errorf("the editor opened on %q, want the current summary", b)
	}
	h.m, _ = settle(t, h.m, h.execs[1].fn(errors.New("exit status 1")))
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the file is still there after a failed editor: %v", err)
	}
	if got := h.m.post.sel.Body; got != "Two **blocking** problems." {
		t.Errorf("a failed editor changed the body to %q", got)
	}
	if got := modalText(h.m); !strings.Contains(got, "exit status 1") {
		t.Errorf("the editor's failure is not shown:\n%s", got)
	}
}

func TestNoPosterShowsReason(t *testing.T) {
	h := newPostHarness(t, postReview(), opts{postOff: "gh is not logged in"})
	h.press(t, "P")
	if h.m.post != nil {
		t.Errorf("the modal opened with no poster: %+v", h.m.post)
	}
	if got := modalText(h.m); !strings.Contains(got, "posting unavailable: gh is not logged in") {
		t.Errorf("no reason shown:\n%s", got)
	}
}

func TestRecordFailureAfterPostIsVisible(t *testing.T) {
	h := newPostHarness(t, postReview(), opts{})
	h.press(t, "P", "k", "enter")
	h.wait()
	m, cmd := update(t, h.m, key("enter"))
	// The store going away between the post and its record stands in for a
	// failed write.
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	msgs, _ := runCmd(t, cmd)
	h.m, _ = settle(t, m, msgs...)
	if len(h.poster.posts) != 1 {
		t.Fatalf("%d posts, want 1", len(h.poster.posts))
	}
	got := modalText(h.m)
	if !strings.Contains(got, "posted to GitHub, but saving the record failed:") || !strings.Contains(got, h.poster.result.URL) {
		t.Errorf("the partial outcome is not shown:\n%s", got)
	}
	h.press(t, "esc")
	if h.m.post != nil {
		t.Errorf("esc did not close the notice: %+v", h.m.post)
	}
}

// markdownReview has one anchored finding with the given body.
func markdownReview(body string) store.Review {
	rv := postReview()
	rv.Verdict = "comment"
	rv.Findings = []store.Finding{{ID: 1, Path: "a.go", Line: 3, Body: body, Ledger: "new"}}
	return rv
}

func TestPreviewRendersMarkdown(t *testing.T) {
	h := newPostHarness(t, markdownReview("**must** fix `x`"), opts{})
	h.press(t, "P", "enter")
	got := modalText(h.m)
	if !strings.Contains(got, "must fix x") || strings.Contains(got, "**must**") {
		t.Errorf("the preview is not rendered:\n%s", got)
	}
	if !strings.Contains(got, "· Comment · head-1") || strings.Contains(got, "COMMENT") {
		t.Errorf("no event or commit in the header:\n%s", got)
	}
}

func TestPreviewRawToggle(t *testing.T) {
	h := newPostHarness(t, markdownReview("**must** fix `x`"), opts{})
	h.press(t, "P", "enter")
	if got := modalText(h.m); !strings.Contains(got, "m raw") {
		t.Errorf("the footer doesn't offer raw:\n%s", got)
	}
	h.press(t, "m")
	got := modalText(h.m)
	if !strings.Contains(got, "**must** fix `x`") || !strings.Contains(got, "m rendered") {
		t.Errorf("m does not show the raw markdown:\n%s", got)
	}
	h.press(t, "m")
	if got := modalText(h.m); strings.Contains(got, "**must**") || !strings.Contains(got, "m raw") {
		t.Errorf("m again is not rendered:\n%s", got)
	}
}

func TestPreviewWarnsHiddenContent(t *testing.T) {
	h := newPostHarness(t, markdownReview("Looks fine. <!-- hidden --> cc @alice"), opts{})
	h.press(t, "P", "enter")
	check := func(mode string) {
		t.Helper()
		got := modalText(h.m)
		i := strings.Index(got, "Rendered view hides:")
		if i < 0 || !strings.Contains(got[i:], "press m to see the raw text") {
			t.Fatalf("%s: no warning line:\n%s", mode, got)
		}
		warning := got[i:strings.Index(got, "press m to see the raw text")]
		if !strings.Contains(warning, "HTML comment") || !strings.Contains(warning, "mentions @alice") {
			t.Errorf("%s: the warning doesn't name both: %q", mode, warning)
		}
		if parts := strings.Index(got, "a.go:3 "); parts >= 0 && parts < i && strings.LastIndex(got, "a.go:3") < i {
			t.Errorf("%s: the warning is below the parts:\n%s", mode, got)
		}
	}
	check("rendered")
	h.press(t, "m")
	check("raw")
}

func TestPreviewScrollsAndMovesTheCursor(t *testing.T) {
	h := newPostHarness(t, postReview(), opts{})
	h.press(t, "P", "k", "enter", "j")
	if h.m.post.cursor != 1 {
		t.Errorf("j: cursor %d, want 1", h.m.post.cursor)
	}
	// Short enough that the preview has to scroll.
	h.m, _ = settle(t, h.m, tea.WindowSizeMsg{Width: 160, Height: 16})
	h.m, _ = settle(t, h.m, ctrl('d'))
	if h.m.post.scroll == 0 {
		t.Errorf("ctrl+d did not scroll:\n%s", modalText(h.m))
	}
	h.m, _ = settle(t, h.m, ctrl('u'), ctrl('u'))
	if h.m.post.scroll != 0 {
		t.Errorf("ctrl+u: scroll %d, want 0", h.m.post.scroll)
	}
	for range 3 {
		h.press(t, "j")
	}
	if got := modalText(h.m); !strings.Contains(got, "▸ [ ] unanchored") {
		t.Errorf("the cursor scrolled off screen:\n%s", got)
	}
	h.press(t, "esc")
	if h.m.post == nil || h.m.post.stage != postPick {
		t.Errorf("esc from the preview: %+v, want the picker", h.m.post)
	}
	h.press(t, "esc")
	if h.m.post != nil {
		t.Errorf("esc from the picker: %+v, want closed", h.m.post)
	}
}

func TestDrawerPOnFailedReviewDoesNotOpen(t *testing.T) {
	rv := postReview()
	rv.Outcome = "failed"
	h := newPostHarness(t, rv, opts{})
	h.press(t, "P")
	if h.m.post != nil {
		t.Errorf("P opened the post flow on a failed review")
	}
}

func TestDrawerShowsPostedAs(t *testing.T) {
	d, c := openDrawer(t, 160, 40, drawerReview())
	d, _, _ = d.update(drawerLoadedMsg{PRID: "a", Seq: 1, Reviews: []store.Review{drawerReview()},
		Posted: map[string]store.Posted{"r1": {ReviewID: "r1", Event: "APPROVE", At: t0.Add(-2 * time.Hour)}}}, c)
	if got := ansi.Strip(renderDrawer(d, c, 40)); !strings.Contains(got, "Posted as Approve · 2h ago") {
		t.Errorf("no posted line:\n%s", got)
	}
}

func TestEnterBounceDoesNotPost(t *testing.T) {
	h := newPostHarness(t, markdownReview("fix `x`"), opts{})
	h.press(t, "P", "enter", "enter") // pick Comment, then a bounce
	if h.m.post == nil || h.m.post.stage != postPreview {
		t.Fatalf("flow %+v, want the preview", h.m.post)
	}
	if len(h.poster.posts) != 0 {
		t.Fatalf("a bounced enter posted %d times", len(h.poster.posts))
	}
	h.wait()
	h.press(t, "enter")
	if len(h.poster.posts) != 1 {
		t.Errorf("enter after the delay posted %d times, want 1", len(h.poster.posts))
	}
}

func TestRepeatKeysIgnored(t *testing.T) {
	h := newPostHarness(t, markdownReview("fix `x`"), opts{})
	prior := store.Posted{ReviewID: "r1", Event: "COMMENT", CommitOID: "head-1", At: t0.Add(-time.Hour)}
	if err := h.store.RecordPost(context.Background(), prior, nil); err != nil {
		t.Fatal(err)
	}
	h.press(t, "P")
	h.m, _ = settle(t, h.m, repeat("y"))
	if h.m.post == nil || h.m.post.stage != postRepost {
		t.Fatalf("a repeated y answered the question: %+v", h.m.post)
	}
	h.press(t, "y")
	h.m, _ = settle(t, h.m, repeat("enter"))
	if h.m.post.stage != postPick {
		t.Fatalf("a repeated enter picked: stage %v", h.m.post.stage)
	}
	h.press(t, "enter")
	h.wait()
	h.m, _ = settle(t, h.m, repeat("enter"))
	if len(h.poster.posts) != 0 || h.m.post.inFlight {
		t.Errorf("a repeated enter posted %d times", len(h.poster.posts))
	}
}

func TestMatchThreadsByPostedLine(t *testing.T) {
	in := github.PostInput{Threads: []github.PostThread{
		{Path: "a.go", Line: 3, Body: "one"}, {Path: "a.go", Line: 3, Body: "two"}, {Path: "b.go", Line: 9, Body: "three"},
	}}
	comments := []github.PostedComment{
		{ID: "C1", Path: "a.go", Line: 3, OriginalLine: 3},
		{ID: "C2", Path: "a.go", Line: 3, OriginalLine: 3},
		// Posted on an older commit: the line is gone at head.
		{ID: "C3", Path: "b.go", Line: 0, OriginalLine: 9},
	}
	threadOf := map[string]string{"C1": "T1", "C2": "T2", "C3": "T3"}
	got := matchThreads(in, []int64{10, 20, 30}, comments, threadOf)
	if want := map[int64]string{10: "T1", 20: "T2", 30: "T3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("threads = %v, want %v", got, want)
	}
	// A line shifted at head still matches by the line it was posted on.
	comments[2].Line = 14
	if got := matchThreads(in, []int64{10, 20, 30}, comments, threadOf); got[30] != "T3" {
		t.Errorf("shifted line: threads = %v, want 30 → T3", got)
	}
	// With no original line, the line is used.
	comments[2] = github.PostedComment{ID: "C3", Path: "b.go", Line: 9}
	if got := matchThreads(in, []int64{10, 20, 30}, comments, threadOf); got[30] != "T3" {
		t.Errorf("no original line: threads = %v, want 30 → T3", got)
	}
}

func TestCtrlCWhilePostingAsksAgain(t *testing.T) {
	h := newPostHarness(t, markdownReview("fix `x`"), opts{})
	h.press(t, "P", "enter")
	h.wait()
	m, _ := update(t, h.m, key("enter")) // the post is in flight
	m, cmd := update(t, m, ctrl('c'))
	if isQuit(t, cmd) {
		t.Fatal("the first ctrl+c quit with a post in flight")
	}
	if !strings.Contains(modalText(m), "posting to GitHub — ctrl+c again to quit anyway") {
		t.Errorf("no warning:\n%s", modalText(m))
	}
	_, cmd = update(t, m, ctrl('c'))
	if !isQuit(t, cmd) {
		t.Error("the second ctrl+c did not quit")
	}
}

func TestRepostAsksAfterUnrecordedPost(t *testing.T) {
	h := newPostHarness(t, markdownReview("fix `x`"), opts{})
	// What a post whose record failed leaves: the drawer knows, the store
	// doesn't.
	h.m.drawer = h.m.drawer.withPosted(store.Posted{ReviewID: "r1", Event: "COMMENT", At: t0.Add(-3 * time.Minute)})
	h.press(t, "P")
	if got := modalText(h.m); !strings.Contains(got, "Already posted as Comment 3m ago — post again? y/n") {
		t.Errorf("no repost question:\n%s", got)
	}
}

func TestPostDuringPollPollsAfter(t *testing.T) {
	h := newPostHarness(t, markdownReview("fix `x`"), opts{})
	m, held := update(t, h.m, key("R"))
	if !m.polling || held == nil {
		t.Fatal("R did not start a poll")
	}
	h.m = m
	h.press(t, "P", "enter")
	h.wait()
	h.press(t, "enter")
	if len(h.poster.posts) != 1 {
		t.Fatalf("%d posts, want 1", len(h.poster.posts))
	}
	before := h.client.polls
	msgs, _ := runCmd(t, held)
	h.m, _ = settle(t, h.m, msgs...)
	// The held poll, then the one the post asked for.
	if got := h.client.polls - before; got != 2 {
		t.Errorf("%d polls after the held one was let go, want 2", got)
	}
	if h.m.pollAgain {
		t.Error("the follow-up poll is still pending")
	}
}

func TestEditorOpensOnOwnSummaryOnly(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "my-editor")
	rv := markdownReview("fix `x`")
	rv.Findings = append(rv.Findings, store.Finding{ID: 2, Path: "b.go", Unanchored: true, Body: "a note", Ledger: "new"})
	h := newPostHarness(t, rv, opts{})
	h.press(t, "P", "enter", "e")
	path := filepath.Join(h.m.dataDir, "tmp", "post-r1.md")
	if b, err := os.ReadFile(path); err != nil || len(b) != 0 {
		t.Errorf("the editor opened on %q (%v), want an empty file: the notes are not the summary", b, err)
	}
	h.m, _ = settle(t, h.m, h.execs[0].fn(nil))
	h.wait()
	h.press(t, "enter")
	if len(h.poster.posts) != 1 || !strings.Contains(h.poster.posts[0].Body, "**Other notes**") {
		t.Errorf("posts = %+v, want a body carrying the Other notes", h.poster.posts)
	}
}

func TestEditReplacesStaleFile(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "my-editor")
	h := newPostHarness(t, markdownReview("fix `x`"), opts{})
	path := filepath.Join(h.m.dataDir, "tmp", "post-r1.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.press(t, "P", "enter", "e")
	if len(h.execs) != 1 {
		t.Fatalf("the editor ran %d times, want 1", len(h.execs))
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) != 0 {
		t.Errorf("file = %q (%v), want the stale one replaced by the empty summary", b, err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v (%v), want 0600", fi.Mode().Perm(), err)
	}
}

const quitWarning = "posting to GitHub — ctrl+c again to quit anyway"

func TestCtrlCGuardResetsAfterFailedPost(t *testing.T) {
	h := newPostHarness(t, markdownReview("fix `x`"), opts{})
	h.poster.err = errors.New("github: post review: no permission")
	h.press(t, "P", "enter")
	h.wait()
	m, post1 := update(t, h.m, key("enter"))
	m, cmd := update(t, m, ctrl('c'))
	if isQuit(t, cmd) {
		t.Fatal("the first ctrl+c quit during post 1")
	}
	msgs, _ := runCmd(t, post1)
	m, _ = settle(t, m, msgs...)
	if m.post == nil || m.post.inFlight {
		t.Fatalf("after the failed post: %+v, want the preview", m.post)
	}

	h.poster.err = nil
	m, _ = update(t, m, key("enter")) // post 2, held in flight
	if !m.post.inFlight {
		t.Fatal("post 2 did not start")
	}
	m, cmd = update(t, m, ctrl('c'))
	if isQuit(t, cmd) {
		t.Fatal("one ctrl+c during post 2 quit: the guard from post 1 was still armed")
	}
	if m.notice != quitWarning {
		t.Errorf("notice = %q, want the warning again", m.notice)
	}
}

func TestCtrlCWarningClearsWhenPostEnds(t *testing.T) {
	for _, fail := range []bool{false, true} {
		h := newPostHarness(t, markdownReview("fix `x`"), opts{})
		if fail {
			h.poster.err = errors.New("github: post review: no permission")
		}
		h.press(t, "P", "enter")
		h.wait()
		m, held := update(t, h.m, key("enter"))
		m, _ = update(t, m, ctrl('c'))
		if m.notice != quitWarning {
			t.Fatalf("fail=%v: notice = %q, want the warning", fail, m.notice)
		}
		msgs, _ := runCmd(t, held)
		m, _ = settle(t, m, msgs...)
		if strings.Contains(modalText(m), quitWarning) {
			t.Errorf("fail=%v: the warning outlived the post:\n%s", fail, modalText(m))
		}
	}
}

// The preview shows an entity-encoded conceal's text in full, and writes
// no escape the renderer does not emit itself.
func TestPreviewShowsConcealedText(t *testing.T) {
	h := newPostHarness(t, markdownReview(concealProbe), opts{})
	h.press(t, "P", "enter")
	out := h.m.postBody(160, 56)
	checkOnlyPaletteSGR(t, "preview", out)
	if got := modalText(h.m); !strings.Contains(got, "This PR is garbage and so is its author") {
		t.Errorf("the sentence is hidden in the preview:\n%s", got)
	}
}
