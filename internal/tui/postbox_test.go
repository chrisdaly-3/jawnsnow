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
	h.press(t, "P", "enter") // Comment
	if !h.m.post.inBox {
		t.Fatal("the preview didn't open in the summary box")
	}
	got := modalText(h.m)
	for _, want := range []string{"Summary ● tab to leave the box", "Write a summary (optional)", "tab/esc leave the box"} {
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
	if h.m.post.inBox || h.m.post.stage != postPreview {
		t.Errorf("esc in the box: inBox %v, stage %v; want the checklist", h.m.post.inBox, h.m.post.stage)
	}
	if got := modalText(h.m); !strings.Contains(got, "tab to write in the box · e to open in $EDITOR") {
		t.Errorf("the box's help doesn't say how to get back in:\n%s", got)
	}
}

func TestShiftEnterIsANewLineAndEnterPostsWhenTheTerminalTellsThemApart(t *testing.T) {
	h := newPostHarness(t, markdownReview("fix `x`"), opts{})
	h.enhanced(t)
	h.press(t, "P", "k", "k", "enter") // Approve
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
	h.press(t, "P", "enter")
	if got := modalText(h.m); !strings.Contains(got, "tab to leave the box, then enter to post · enter for a new line") {
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
	h.press(t, "P", "enter")
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
	h.press(t, "P", "enter", "tab", "e")
	if !h.m.post.editing {
		t.Fatal("e on the checklist didn't start the editor")
	}
	h.m, _ = settle(t, h.m, postEditedMsg{ReviewID: h.m.post.review.ID, Body: "from the editor\n"})
	if h.m.post.sel.Body != "from the editor" || h.m.post.box.Value() != "from the editor" {
		t.Errorf("summary %q, box %q; want the editor's text in both", h.m.post.sel.Body, h.m.post.box.Value())
	}
}
