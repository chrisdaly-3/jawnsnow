package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

var (
	shiftEnter = tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}
	backspace  = tea.KeyPressMsg{Code: tea.KeyBackspace}
)

// typeText types s into the flow one key at a time.
func (h *postH) typeText(t *testing.T, s string) {
	t.Helper()
	for _, r := range s {
		h.m, _ = settle(t, h.m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// enhanced tells the model the terminal reports shift+enter apart from
// enter, as Ghostty, kitty or WezTerm do.
func (h *postH) enhanced(t *testing.T) {
	t.Helper()
	h.m, _ = settle(t, h.m, tea.KeyboardEnhancementsMsg{Flags: 1})
}

func TestPreviewOpensInTheSummaryBox(t *testing.T) {
	h := newPostHarness(t, markdownReview("fix `x`"), opts{})
	h.press(t, "P") // Comment
	if !h.m.post.inBox() {
		t.Fatal("the preview didn't open in the summary box")
	}
	got := modalText(h.m)
	for _, want := range []string{"Summary ● enter for a new line · tab for the findings", "Write a summary (optional). Comment posts with or without one.", "tab/esc findings, then enter to post"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in the preview:\n%s", want, got)
		}
	}
	h.typeText(t, "Looks **good**")
	if h.m.post.sel.Body != "Looks **good**" {
		t.Errorf("summary %q, want what was typed", h.m.post.sel.Body)
	}
	if b := h.m.post.built.in.Body; !strings.Contains(b, "Looks **good**") {
		t.Errorf("payload body %q lacks the typed summary", b)
	}
	// Keys the checklist uses are typed in the box: space, j, m.
	h.typeText(t, " jm")
	if h.m.post.sel.Body != "Looks **good** jm" || h.m.post.raw {
		t.Errorf("summary %q, raw %v: checklist keys reached the checklist", h.m.post.sel.Body, h.m.post.raw)
	}
	h.press(t, "esc")
	if h.m.post.inBox() || h.m.post.stage != postPreview {
		t.Errorf("esc in the box: inBox %v, stage %v; want the checklist", h.m.post.inBox(), h.m.post.stage)
	}
	if got := modalText(h.m); !strings.Contains(got, "Summary ● shift+tab to write here · e to open in $EDITOR") {
		t.Errorf("the box's help doesn't say how to get back in:\n%s", got)
	}
}

func TestShiftEnterIsANewLineAndEnterPostsWhenTheTerminalTellsThemApart(t *testing.T) {
	h := newPostHarness(t, markdownReview("fix `x`"), opts{})
	h.enhanced(t)
	h.press(t, "P", "shift+tab", "h", "h", "space", "tab") // Approve, then back into the box
	if got := modalText(h.m); !strings.Contains(got, "shift+enter for a new line · enter to post") {
		t.Errorf("the box's help doesn't say enter posts:\n%s", got)
	}
	h.typeText(t, "Ship it")
	h.m, _ = settle(t, h.m, shiftEnter)
	h.typeText(t, "Thanks")
	if h.m.post.sel.Body != "Ship it\nThanks" {
		t.Fatalf("summary %q, want two lines", h.m.post.sel.Body)
	}
	// An enter just after typing is held, like any enter just after the
	// preview changed.
	h.press(t, "enter")
	if len(h.poster.posts) != 0 || !h.m.post.holdOn {
		t.Fatalf("an enter right after typing: %d posts, holdOn %v", len(h.poster.posts), h.m.post.holdOn)
	}
	h.wait()
	h.press(t, "enter")
	if len(h.poster.posts) != 1 {
		t.Fatalf("enter in the box posted %d times, want 1", len(h.poster.posts))
	}
	if p := h.poster.posts[0]; p.Event != "APPROVE" || p.Body != "Ship it\nThanks" {
		t.Errorf("posted %s with body %q, want APPROVE with the typed summary", p.Event, p.Body)
	}
}

func TestEnterIsANewLineWhenTheTerminalCantTellShiftEnterApart(t *testing.T) {
	h := newPostHarness(t, markdownReview("fix `x`"), opts{})
	h.press(t, "P")
	if got := modalText(h.m); !strings.Contains(got, "enter for a new line · tab for the findings, then enter to post") {
		t.Errorf("the box's help doesn't say how to post:\n%s", got)
	}
	h.typeText(t, "one")
	h.wait()
	h.press(t, "enter")
	h.typeText(t, "two")
	if len(h.poster.posts) != 0 || h.m.post.sel.Body != "one\ntwo" {
		t.Fatalf("enter in the box: %d posts, summary %q; want a new line", len(h.poster.posts), h.m.post.sel.Body)
	}
	h.press(t, "tab")
	h.wait()
	h.press(t, "enter")
	if len(h.poster.posts) != 1 || h.poster.posts[0].Body != "one\ntwo" {
		t.Errorf("tab then enter: %d posts; want the summary posted", len(h.poster.posts))
	}
}

func TestSummaryBoxTakesPastesAndHeldKeys(t *testing.T) {
	h := newPostHarness(t, markdownReview("fix `x`"), opts{})
	h.enhanced(t)
	h.press(t, "P")
	h.m, _ = settle(t, h.m, tea.PasteMsg{Content: "pasted\ntext"})
	if h.m.post.sel.Body != "pasted\ntext" {
		t.Fatalf("summary %q, want the paste", h.m.post.sel.Body)
	}
	held := backspace
	held.IsRepeat = true
	for range 4 {
		h.m, _ = settle(t, h.m, held)
	}
	if h.m.post.sel.Body != "pasted\n" && h.m.post.sel.Body != "pasted" {
		t.Errorf("summary %q after four held backspaces, want %q", h.m.post.sel.Body, "pasted")
	}
	// A held enter is still dropped, since enter posts here.
	h.wait()
	h.m, _ = settle(t, h.m, repeat("enter"))
	if len(h.poster.posts) != 0 {
		t.Errorf("a held enter posted")
	}
}

func TestEditorResultFillsTheSummaryBox(t *testing.T) {
	h := newPostHarness(t, markdownReview("fix `x`"), opts{})
	h.press(t, "P", "tab", "e")
	if !h.m.post.editing {
		t.Fatal("e on the checklist didn't start the editor")
	}
	h.m, _ = settle(t, h.m, postEditedMsg{ReviewID: h.m.post.review.ID, Body: "from the editor\n"})
	if h.m.post.sel.Body != "from the editor" || h.m.post.box.Value() != "from the editor" {
		t.Errorf("summary %q, box %q; want the editor's text in both", h.m.post.sel.Body, h.m.post.box.Value())
	}
}

func TestEventRowChangesTheEvent(t *testing.T) {
	h := newPostHarness(t, postReview(), opts{}) // verdict: Request changes
	h.press(t, "P")
	if h.m.post.box.Placeholder != "Write a summary. Request changes needs one." {
		t.Errorf("placeholder %q for Request changes", h.m.post.box.Placeholder)
	}
	// In the box h is typed, not an event change.
	h.press(t, "h")
	if h.m.post.sel.Event != "REQUEST_CHANGES" || h.m.post.sel.Body != "h" {
		t.Fatalf("h in the box: event %q, summary %q", h.m.post.sel.Event, h.m.post.sel.Body)
	}
	h.m, _ = settle(t, h.m, backspace)
	// shift+tab reaches the event row with its cursor on the checked event.
	h.press(t, "shift+tab")
	if h.m.post.focus != focusEvent || h.m.post.eventCursor != 2 {
		t.Fatalf("shift+tab: focus %v cursor %d, want the event row on Request changes", h.m.post.focus, h.m.post.eventCursor)
	}
	// Moving the cursor doesn't change what posts; space does.
	h.press(t, "h", "h", "h") // past the left end stays on Approve
	if h.m.post.eventCursor != 0 || h.m.post.sel.Event != "REQUEST_CHANGES" {
		t.Fatalf("h h h: cursor %d event %q, want the cursor on Approve and Request changes still chosen", h.m.post.eventCursor, h.m.post.sel.Event)
	}
	h.press(t, "space")
	if h.m.post.sel.Event != "APPROVE" {
		t.Fatalf("space: event %q, want APPROVE", h.m.post.sel.Event)
	}
	if !strings.HasPrefix(h.m.post.box.Placeholder, "Write a summary (optional). Approve posts") {
		t.Errorf("placeholder %q for Approve", h.m.post.box.Placeholder)
	}
	if got := modalText(h.m); !strings.Contains(got, "✓ Approve Comment Request changes") {
		t.Errorf("the check isn't on Approve:\n%s", got)
	}
	// Leaving the row with the cursor elsewhere keeps the checked event.
	h.press(t, "l", "tab")
	if h.m.post.sel.Event != "APPROVE" || h.m.post.focus != focusSummary {
		t.Errorf("l then tab: event %q focus %v, want APPROVE and the box", h.m.post.sel.Event, h.m.post.focus)
	}
	// j and k belong to the checklist; on the event row they do nothing.
	h.press(t, "shift+tab", "j", "k")
	if h.m.post.cursor != 0 || h.m.post.focus != focusEvent {
		t.Errorf("j k on the event row: checklist cursor %d focus %v", h.m.post.cursor, h.m.post.focus)
	}
}

func TestEventRowCursorIsFilledOnlyWithFocus(t *testing.T) {
	h := newPostHarness(t, postReview(), opts{})
	h.press(t, "P")
	row := func() string {
		for l := range strings.SplitSeq(content(h.m), "\n") {
			if strings.Contains(l, "Request changes") && strings.Contains(l, "Approve") {
				return l
			}
		}
		t.Fatal("no event row")
		return ""
	}
	if strings.Contains(row(), "48;5;237") || strings.Contains(row(), "▸") {
		t.Errorf("the event row shows a cursor while the box has focus: %q", row())
	}
	h.press(t, "shift+tab")
	if !strings.Contains(row(), "48;5;237") || !strings.Contains(row(), "▸") {
		t.Errorf("the event row's cursor isn't marked and filled with focus: %q", row())
	}
}

func TestChecklistCursorIsFilledOnlyWithFocus(t *testing.T) {
	h := newPostHarness(t, postReview(), opts{})
	h.press(t, "P")
	filled := func() bool {
		for l := range strings.SplitSeq(content(h.m), "\n") {
			if strings.Contains(l, "48;5;237") && strings.Contains(l, "lookup.go:40") {
				return true
			}
		}
		return false
	}
	if filled() {
		t.Error("the checklist's cursor is filled while the box has focus")
	}
	h.press(t, "tab")
	if !filled() {
		t.Error("the checklist's cursor isn't filled once it has focus")
	}
}

func TestBoxHeights(t *testing.T) {
	for _, c := range []struct{ height, lo, hi int }{
		{60, 6, 12}, {40, 6, 12}, {24, 4, 6}, {16, 3, 4},
	} {
		if lo, hi := boxHeights(c.height); lo != c.lo || hi != c.hi {
			t.Errorf("boxHeights(%d) = %d, %d; want %d, %d", c.height, lo, hi, c.lo, c.hi)
		}
	}
	// Empty, the box is boxMinHeight lines inside its border on a usual
	// window.
	h := newPostHarness(t, postReview(), opts{})
	h.press(t, "P")
	if got := h.m.post.box.Height(); got != boxMinHeight {
		t.Errorf("empty box height %d, want %d", got, boxMinHeight)
	}
}

// enter on the event row posts, as on the checklist; it posts the checked
// event, not the one under the cursor.
func TestEnterOnTheEventRowPostsTheCheckedEvent(t *testing.T) {
	h := newPostHarness(t, postReview(), opts{}) // verdict: Request changes
	h.press(t, "P")
	h.typeText(t, "Please fix the lookup.")
	h.press(t, "shift+tab", "h") // cursor on Comment; Request changes still checked
	h.wait()
	h.press(t, "enter")
	if len(h.poster.posts) != 1 || h.poster.posts[0].Event != "REQUEST_CHANGES" {
		t.Fatalf("posts = %+v, want one REQUEST_CHANGES", h.poster.posts)
	}
}
