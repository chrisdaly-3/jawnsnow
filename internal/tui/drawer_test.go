package tui

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/status"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
)

func drawerPR() tracker.Tracked {
	return wallCard("a", "chainguard-dev/mono", 85180, "feat(policies): fill Decision.repo_name from the registry", "alice", status.ReadyForReview, false, 120, 8, 2*time.Hour)
}

// drawerReview is a review with a nit, two blocking findings (one fixed),
// and an unanchored question, given in the wrong order on purpose.
func drawerReview() store.Review {
	return store.Review{
		ID: "r1", PRID: "a", Repo: "chainguard-dev/mono", Number: 85180,
		Tier: "first_pass", Model: "claude-opus-5-5", Effort: "high",
		Verdict: "request_changes", VerdictReason: "Two blocking problems in the registry lookup.",
		Understanding: "This PR fills `Decision.repo_name` from the registry.\n\n- looks the repo up by digest\n- falls back to the image reference",
		Assumptions: []store.Assumption{
			{Claim: "The registry returns at most one repo per digest", Evidence: "pkg/registry/lookup.go:30"},
		},
		Note:    "Checks: tests were not run (the reviewer is read-only).",
		Outcome: "complete", CostUSD: 1.234,
		StartedAt: t0.Add(-2 * time.Hour), EndedAt: t0.Add(-2*time.Hour + 4*time.Minute),
		Findings: []store.Finding{
			{ID: 1, Path: "pkg/policy/decision.go", Line: 12, Body: "nit: rename `x` to `repoName`.", Ledger: "new"},
			{ID: 2, Path: "pkg/registry/lookup.go", Line: 40, Blocking: true, Category: "correctness", Ledger: "outstanding",
				Body: "Nil dereference when the **registry** returns no repo.\n\n```go\nrepo := r.Repo\n```\n\nCheck `r` first."},
			{ID: 3, Path: "pkg/policy/decision.go", Line: 8, StartLine: 5, Blocking: true, Category: "correctness", Ledger: "addressed",
				Body: "There is no fallback when the lookup fails:\n\n- return the image reference\n- log the miss"},
			{ID: 4, Unanchored: true, Body: "question: why not cache the lookup per digest?", Ledger: "addressed"},
		},
	}
}

func drawerCtxFor(t *testing.T, height int) drawerCtx {
	t.Helper()
	return drawerCtx{PR: drawerPR(), Now: t0, Theme: testTheme(t), Tick: fakeTick, Height: height}
}

// openDrawer opens a drawer on a terminal termW wide, steps its spring until
// it settles, and loads rs into it.
func openDrawer(t *testing.T, termW, height int, rs ...store.Review) (drawerModel, drawerCtx) {
	t.Helper()
	c := drawerCtxFor(t, height)
	d := newDrawer()
	d, cmd := d.show("a", termW)
	for i := 0; cmd != nil; i++ {
		if i > 1000 {
			t.Fatal("the drawer spring never settled")
		}
		d, cmd, _ = d.update(drawerTickMsg{}, c)
	}
	d, _, _ = d.update(drawerLoadedMsg{PRID: "a", Reviews: rs}, c)
	return d, c
}

func renderDrawer(d drawerModel, c drawerCtx, height int) string {
	return d.view(d.width(), height, c)
}

func TestDrawerGoldenWide(t *testing.T) {
	d, c := openDrawer(t, 160, 40, drawerReview())
	if d.width() != 72 {
		t.Fatalf("settled width = %d, want 72 (45%% of 160)", d.width())
	}
	golden(t, "drawer_wide.golden", renderDrawer(d, c, 40))
}

func TestDrawerGoldenNarrow(t *testing.T) {
	d, c := openDrawer(t, 90, 40, drawerReview())
	if d.width() != 41 {
		t.Fatalf("settled width = %d, want 41 (45%% of 90 is 40.5, rounded up)", d.width())
	}
	golden(t, "drawer_narrow.golden", renderDrawer(d, c, 40))
}

func TestDrawerGoldenExpanded(t *testing.T) {
	d, c := openDrawer(t, 160, 60, drawerReview())
	d, _, _ = d.update(key("u"), c)
	d, _, _ = d.update(key("a"), c)
	golden(t, "drawer_expanded.golden", renderDrawer(d, c, 60))
}

func TestDrawerFindingOrder(t *testing.T) {
	got := orderedFindings(drawerReview().Findings)
	var ids []int64
	for _, f := range got {
		ids = append(ids, f.ID)
	}
	// Blocking first in review order, then nit:, non-blocking:, question:.
	if want := []int64{2, 3, 1, 4}; fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Errorf("order = %v, want %v", ids, want)
	}
	d, c := openDrawer(t, 160, 80, drawerReview())
	out := ansi.Strip(renderDrawer(d, c, 80))
	last := -1
	for _, p := range []string{"pkg/registry/lookup.go:40", "pkg/policy/decision.go:5-8", "pkg/policy/decision.go:12", "unanchored"} {
		i := strings.Index(out, p)
		if i < 0 || i < last {
			t.Fatalf("%q at %d, after %d; frame:\n%s", p, i, last, out)
		}
		last = i
	}
}

func TestDrawerLedgerSummary(t *testing.T) {
	d, c := openDrawer(t, 160, 40, drawerReview())
	if out := ansi.Strip(renderDrawer(d, c, 40)); !strings.Contains(out, "2 fixed · 1 still open · 1 new") {
		t.Errorf("no ledger summary in:\n%s", out)
	}
}

func TestDrawerPassNavigation(t *testing.T) {
	newest, mid, oldest := drawerReview(), drawerReview(), drawerReview()
	newest.ID, newest.Model = "r3", "claude-sonnet-5-5"
	mid.ID, mid.Model, mid.StartedAt = "r2", "claude-haiku-5-5", t0.Add(-5*time.Hour)
	oldest.ID, oldest.Model, oldest.StartedAt = "r1", "claude-fable-5-2", t0.Add(-9*time.Hour)
	d, c := openDrawer(t, 160, 40, newest, mid, oldest)
	header := func() string { return ansi.Strip(renderDrawer(d, c, 40)) }

	if !strings.Contains(header(), "claude-sonnet-5-5") || !strings.Contains(header(), "pass 3/3") {
		t.Fatalf("opens on the newest pass:\n%s", header())
	}
	d, _, _ = d.update(key("<"), c)
	if !strings.Contains(header(), "claude-haiku-5-5") || !strings.Contains(header(), "pass 2/3") {
		t.Errorf("< should show the older pass:\n%s", header())
	}
	d, _, _ = d.update(key("<"), c)
	d, _, _ = d.update(key("<"), c) // stays on the oldest
	if !strings.Contains(header(), "claude-fable-5-2") || !strings.Contains(header(), "pass 1/3") {
		t.Errorf("< twice should show the oldest pass:\n%s", header())
	}
	d, _, _ = d.update(key(">"), c)
	d, _, _ = d.update(key(">"), c)
	d, _, _ = d.update(key(">"), c) // stays on the newest
	if !strings.Contains(header(), "claude-sonnet-5-5") || !strings.Contains(header(), "pass 3/3") {
		t.Errorf("> should come back to the newest:\n%s", header())
	}
}

func TestDrawerFindingNavigationAndScroll(t *testing.T) {
	d, c := openDrawer(t, 160, 12, drawerReview()) // short: it has to scroll
	if d.sel != 0 || d.scroll != 0 {
		t.Fatalf("starts at sel %d scroll %d", d.sel, d.scroll)
	}
	d, _, _ = d.update(key("n"), c)
	d, _, _ = d.update(key("n"), c)
	if d.sel != 2 || d.scroll == 0 {
		t.Errorf("n,n: sel %d scroll %d, want sel 2 and a scrolled drawer", d.sel, d.scroll)
	}
	for range 5 {
		d, _, _ = d.update(key("n"), c)
	}
	if d.sel != 3 {
		t.Errorf("n past the last finding: sel %d, want 3", d.sel)
	}
	d, _, _ = d.update(key("p"), c)
	if d.sel != 2 {
		t.Errorf("p: sel %d, want 2", d.sel)
	}
	d, _, _ = d.update(key("j"), c)
	before := d.scroll
	d, _, _ = d.update(key("k"), c)
	if d.scroll != before-1 && before != 0 {
		t.Errorf("k: scroll %d, want %d", d.scroll, before-1)
	}
}

func TestDrawerActions(t *testing.T) {
	d, c := openDrawer(t, 160, 40, drawerReview())
	act := func(k string) action {
		_, _, a := d.update(key(k), c)
		return a
	}
	if a, ok := act("o").(actionOpen); !ok || a.URL != c.PR.PR.URL {
		t.Errorf("o = %#v, want actionOpen{%s}", act("o"), c.PR.PR.URL)
	}
	// The cursor is on the first finding: pkg/registry/lookup.go:40.
	a, ok := act("O").(actionOpen)
	if !ok || !strings.HasPrefix(a.URL, c.PR.PR.URL+"/files#diff-") || !strings.HasSuffix(a.URL, "R40") {
		t.Errorf("O = %#v, want the PR's /files page anchored on the path and line", act("O"))
	}
	if cp, ok := act("y").(actionCopy); !ok || !strings.HasPrefix(cp.Text, "Nil dereference when the **registry**") {
		t.Errorf("y = %#v, want the finding body", act("y"))
	}
	if a, ok := act("space").(actionReview); !ok || a.ID != "a" {
		t.Errorf("space = %#v, want actionReview{a}", act("space"))
	}
	if a, ok := act("c").(actionPickModel); !ok || a.ID != "a" {
		t.Errorf("c = %#v, want actionPickModel{a}", act("c"))
	}
}

func TestDrawerUnderstandingAndAssumptionsCollapse(t *testing.T) {
	d, c := openDrawer(t, 160, 60, drawerReview())
	out := ansi.Strip(renderDrawer(d, c, 60))
	for _, s := range []string{"Understanding", "Assumptions"} {
		if !strings.Contains(out, s) {
			t.Errorf("no %q heading", s)
		}
	}
	if strings.Contains(out, "looks the repo up by digest") || strings.Contains(out, "at most one repo") {
		t.Errorf("collapsed sections show their text:\n%s", out)
	}
	d, _, _ = d.update(key("u"), c)
	d, _, _ = d.update(key("a"), c)
	out = ansi.Strip(renderDrawer(d, c, 60))
	if !strings.Contains(out, "looks the repo up by digest") || !strings.Contains(out, "at most one repo") {
		t.Errorf("u and a should expand the sections:\n%s", out)
	}
}

func TestDrawerTicker(t *testing.T) {
	d, c := openDrawer(t, 160, 40, drawerReview())
	c.Running, c.Ticker = true, "reading pkg/foo.go"
	out := ansi.Strip(renderDrawer(d, c, 40))
	lines := strings.Split(out, "\n")
	if !strings.Contains(lines[len(lines)-1], "reading pkg/foo.go") {
		t.Errorf("the ticker should be the last line:\n%s", out)
	}
	c.Running = false
	if strings.Contains(ansi.Strip(renderDrawer(d, c, 40)), "reading pkg/foo.go") {
		t.Error("the ticker shows after the review finished")
	}
}

func TestDrawerFailedAndIncompleteLabels(t *testing.T) {
	r := drawerReview()
	r.Outcome = "incomplete"
	d, c := openDrawer(t, 160, 40, r)
	if out := ansi.Strip(renderDrawer(d, c, 40)); !strings.Contains(out, "REQUEST CHANGES") || !strings.Contains(out, "incomplete") {
		t.Errorf("incomplete label missing:\n%s", out)
	}
	r = store.Review{ID: "f", PRID: "a", Outcome: "failed", Error: "claude: exit status 1", StartedAt: t0.Add(-time.Minute)}
	d, c = openDrawer(t, 160, 40, r)
	if out := ansi.Strip(renderDrawer(d, c, 40)); !strings.Contains(out, "failed") || !strings.Contains(out, "exit status 1") {
		t.Errorf("failed review should say so and why:\n%s", out)
	}
}

func TestDrawerEmptyAndLoading(t *testing.T) {
	c := drawerCtxFor(t, 40)
	d, _ := newDrawer().show("a", 160)
	if out := ansi.Strip(d.view(40, 40, c)); !strings.Contains(out, "Loading") {
		t.Errorf("before the load:\n%s", out)
	}
	d, _, _ = d.update(drawerLoadedMsg{PRID: "a"}, c)
	if out := ansi.Strip(d.view(40, 40, c)); !strings.Contains(out, "No reviews yet") {
		t.Errorf("with no reviews:\n%s", out)
	}
}

// The engine writes one check note per line; the drawer keeps them apart.
func TestDrawerCheckNotesKeepTheirLines(t *testing.T) {
	r := drawerReview()
	r.Note = "the reviewer's note\nnot done: read pkg/a.go\nwording: say which test"
	d, c := openDrawer(t, 160, 80, r)
	var lines []string
	for _, l := range strings.Split(ansi.Strip(d.view(d.width(), 80, c)), "\n") {
		lines = append(lines, strings.Trim(l, " │"))
	}
	for _, want := range []string{"the reviewer's note", "not done: read pkg/a.go", "wording: say which test"} {
		if !slices.Contains(lines, want) {
			t.Errorf("no line %q in:\n%s", want, strings.Join(lines, "\n"))
		}
	}
}

func TestDrawerCleansText(t *testing.T) {
	r := drawerReview()
	r.Findings[1].Body = "Bad \x1b]0;pwned\x07\x1b[31mred\x1b[0m text\x1b[2J\n\n- item one\n- item two\x07"
	r.Findings[1].Path = "pkg/x\x1b]52;c;AAAA\x07.go"
	r.Understanding = "under\x1b[1Gstanding \x1b]8;;http://evil\x07link"
	r.VerdictReason = "reason\x1b[31m"
	r.Note = "note\x00\x1b[H"
	r.Assumptions = []store.Assumption{{Claim: "claim\x1b[31m", Evidence: "ev\x07idence"}}
	c := drawerCtxFor(t, 60)
	c.PR.PR.Title = "title\x1b]0;pwned\x07"
	c.Ticker, c.Running = "reading \x1b]0;pwned\x07x", true
	d, _ := openDrawer(t, 160, 60, r)
	d, _, _ = d.update(key("u"), c)
	d, _, _ = d.update(key("a"), c)
	raw := d.view(d.width(), 60, c)
	plain := ansi.Strip(raw)
	if strings.ContainsAny(plain, "\x1b\x07\x00") {
		t.Errorf("control characters survive: %q", plain)
	}
	if strings.Contains(raw, "\x1b]") {
		t.Errorf("an OSC sequence reaches the screen: %q", raw)
	}
	if strings.Contains(plain, "pwned") || strings.Contains(plain, "evil") {
		t.Errorf("escape-sequence payload survives:\n%s", plain)
	}
	for _, want := range []string{"red", "item one", "item two", "standing", "idence"} {
		if !strings.Contains(plain, want) {
			t.Errorf("%q is gone:\n%s", want, plain)
		}
	}
	// The list in the body is still a list: newlines were kept.
	if !strings.Contains(plain, "• item one") || !strings.Contains(plain, "• item two") {
		t.Errorf("markdown structure was flattened:\n%s", plain)
	}
	// y copies cleaned text too.
	_, _, a := d.update(key("y"), c)
	if cp, ok := a.(actionCopy); !ok || strings.ContainsAny(cp.Text, "\x1b\x07") {
		t.Errorf("y = %#v, want a cleaned body", a)
	}
}

func TestDrawerNeverUsesBasicANSIColors(t *testing.T) {
	d, c := openDrawer(t, 160, 60, drawerReview())
	d, _, _ = d.update(key("u"), c)
	raw := renderDrawer(d, c, 60)
	for _, seq := range basicColorSeqs(raw) {
		t.Errorf("a basic ANSI color (0-15) escape %q in the drawer", seq)
	}
}

func TestDrawerSpringSettles(t *testing.T) {
	c := drawerCtxFor(t, 40)
	d, cmd := newDrawer().show("a", 160)
	if cmd == nil || d.width() != 0 {
		t.Fatalf("opening: cmd %v width %d, want a tick and width 0", cmd, d.width())
	}
	widths := []int{}
	for i := 0; cmd != nil; i++ {
		if i > 1000 {
			t.Fatal("never settled")
		}
		d, cmd, _ = d.update(drawerTickMsg{}, c)
		widths = append(widths, d.width())
	}
	if d.width() != 72 {
		t.Errorf("settled at %d, want 45%% of 160 = 72", d.width())
	}
	if i := slices.IndexFunc(widths, func(w int) bool { return w > 0 }); i < 0 || slices.Max(widths[:i+1]) >= 72 {
		t.Errorf("widths %v: the drawer should grow through the cells in between", widths)
	}
	if !slices.IsSorted(widths) {
		t.Errorf("widths %v: critically damped, so never backwards", widths)
	}
	// Close: it slides back to nothing and stops being open.
	d, cmd = d.hide()
	if d.isOpen() {
		t.Error("hide leaves the drawer open")
	}
	for i := 0; cmd != nil; i++ {
		if i > 1000 {
			t.Fatal("never closed")
		}
		d, cmd, _ = d.update(drawerTickMsg{}, c)
	}
	if d.width() != 0 || d.visible() {
		t.Errorf("closed: width %d visible %v, want 0 false", d.width(), d.visible())
	}
}

// ---- in the root model ----

func drawerHarness(t *testing.T, o opts) *harness {
	t.Helper()
	h := newHarness(t, o, wallPR("1"))
	h.finishPoll(t)
	return h
}

func settleDrawer(t *testing.T, h *harness) {
	t.Helper()
	for i := 0; h.m.drawer.animating; i++ {
		if i > 1000 {
			t.Fatal("drawer never settled")
		}
		h.m, _ = settle(t, h.m, drawerTickMsg{})
	}
}

func TestDrawerTabMovesFocus(t *testing.T) {
	h := drawerHarness(t, opts{})
	if got := h.m.paneCount(); got != 1 {
		t.Fatalf("panes with the drawer closed = %d, want 1", got)
	}
	h.press(t, "enter")
	settleDrawer(t, h)
	if !h.m.drawer.isOpen() {
		t.Fatal("enter did not open the drawer")
	}
	if got := h.m.paneCount(); got != 2 {
		t.Fatalf("panes with the drawer open = %d, want 2", got)
	}
	h.press(t, "tab")
	if h.m.focus != 1 {
		t.Errorf("tab: focus %d, want the drawer (1)", h.m.focus)
	}
	h.press(t, "tab")
	if h.m.focus != 0 {
		t.Errorf("tab again: focus %d, want the wall (0)", h.m.focus)
	}
	h.press(t, "tab", "esc")
	if h.m.drawer.isOpen() || h.m.focus != 0 {
		t.Errorf("esc: open %v focus %d, want closed and focus on the wall", h.m.drawer.isOpen(), h.m.focus)
	}
	if got := h.m.paneCount(); got != 1 {
		t.Errorf("panes after closing = %d, want 1", got)
	}
}

func TestDrawerReflowsTheWall(t *testing.T) {
	h := drawerHarness(t, opts{})
	h.press(t, "enter")
	settleDrawer(t, h)
	lines := strings.Split(ansi.Strip(content(h.m)), "\n")
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > 160 {
			t.Fatalf("line %d is %d cells wide in a 160-cell terminal", i, w)
		}
	}
	if !strings.Contains(ansi.Strip(content(h.m)), "│ ") || !strings.Contains(ansi.Strip(content(h.m)), "No reviews yet") {
		t.Errorf("the drawer is not beside the wall:\n%s", ansi.Strip(content(h.m)))
	}
}

func TestDrawerLoadsReviewsFromTheStore(t *testing.T) {
	h := drawerHarness(t, opts{})
	rv := drawerReview()
	rv.PRID = "1"
	if err := h.store.SaveReview(context.Background(), rv); err != nil {
		t.Fatal(err)
	}
	h.press(t, "enter")
	settleDrawer(t, h)
	if id := h.m.drawer.prID; id != "1" {
		t.Fatalf("the drawer is on %q, want 1", id)
	}
	if got := ansi.Strip(content(h.m)); !strings.Contains(got, "Nil dereference") || !strings.Contains(got, "REQUEST CHANGES") {
		t.Errorf("stored review not shown:\n%s", got)
	}
}

func TestDrawerSpaceAndCActOnTheDrawersPR(t *testing.T) {
	h := drawerHarness(t, opts{reviews: true})
	h.press(t, "enter")
	id := h.m.drawer.prID
	h.press(t, "tab") // focus the drawer
	h.press(t, "space")
	h.runReviews(t)
	if ids := h.reviewer.requestedIDs(); len(ids) != 1 || ids[0] != id {
		t.Errorf("space reviewed %v, want [%s]", ids, id)
	}
	h.press(t, "c")
	if h.m.reviews.picker == nil || h.m.reviews.picker.id != id {
		t.Errorf("c: picker %+v, want one for %s", h.m.reviews.picker, id)
	}
}

func TestDrawerYCopiesToTheClipboard(t *testing.T) {
	h := drawerHarness(t, opts{})
	rv := drawerReview()
	rv.PRID = "1"
	if err := h.store.SaveReview(context.Background(), rv); err != nil {
		t.Fatal(err)
	}
	h.press(t, "enter", "tab")
	m, cmd := update(t, h.m, key("y"))
	h.m = m
	if cmd == nil {
		t.Fatal("y produced no command")
	}
	msgs, _ := runCmd(t, cmd)
	if len(msgs) != 1 || !strings.Contains(strings.ToLower(fmt.Sprintf("%T", msgs[0])), "clipboard") {
		t.Errorf("y produced %#v, want a set-clipboard message", msgs)
	}
}

var sgr = regexp.MustCompile("\x1b\\[([0-9;:]*)m")

// basicColorSeqs lists the SGR sequences in s that pick a color from the
// terminal's own 16: 30-37, 40-47, 90-97, 100-107 and 38;5;N or 48;5;N
// with N under 16.
func basicColorSeqs(s string) []string {
	var bad []string
	for _, m := range sgr.FindAllStringSubmatch(s, -1) {
		ps := strings.FieldsFunc(m[1], func(r rune) bool { return r == ';' || r == ':' })
		for i := 0; i < len(ps); i++ {
			n, _ := strconv.Atoi(ps[i])
			switch {
			case n >= 30 && n <= 37, n >= 40 && n <= 47, n >= 90 && n <= 97, n >= 100 && n <= 107:
				bad = append(bad, m[0])
			case (n == 38 || n == 48) && i+2 < len(ps) && ps[i+1] == "5":
				if idx, _ := strconv.Atoi(ps[i+2]); idx < 16 {
					bad = append(bad, m[0])
				}
				i += 2
			case (n == 38 || n == 48) && i+4 < len(ps) && ps[i+1] == "2":
				i += 4
			}
		}
	}
	return bad
}

func reviewAt(id, model string, ago time.Duration) store.Review {
	r := drawerReview()
	r.ID, r.Model, r.StartedAt = id, model, t0.Add(-ago)
	return r
}

func TestDrawerFollowsNewReviewOnNewestPass(t *testing.T) {
	d, c := openDrawer(t, 160, 40, reviewAt("r1", "claude-opus-5-5", time.Hour))
	d.sel, d.scroll = 2, 5
	d, _, _ = d.update(drawerLoadedMsg{PRID: "a", Seq: 1, Reviews: []store.Review{reviewAt("r2", "claude-sonnet-5-5", 0), reviewAt("r1", "claude-opus-5-5", time.Hour)}}, c)
	if d.current().ID != "r2" || d.sel != 0 || d.scroll != 0 {
		t.Errorf("on the newest pass: showing %s sel %d scroll %d, want r2 0 0", d.current().ID, d.sel, d.scroll)
	}
}

func TestDrawerKeepsOlderPassWhenPinned(t *testing.T) {
	d, c := openDrawer(t, 160, 40, reviewAt("r2", "claude-sonnet-5-5", 0), reviewAt("r1", "claude-opus-5-5", time.Hour))
	d, _, _ = d.update(key("<"), c)
	d, _, _ = d.update(drawerLoadedMsg{PRID: "a", Seq: 1, Reviews: []store.Review{reviewAt("r3", "claude-haiku-5-5", 0), reviewAt("r2", "claude-sonnet-5-5", time.Minute), reviewAt("r1", "claude-opus-5-5", time.Hour)}}, c)
	if d.current().ID != "r1" || d.pass != 2 {
		t.Errorf("on an older pass: showing %s at %d, want r1 at 2", d.current().ID, d.pass)
	}
}

func TestDrawerDropsOutOfOrderLoads(t *testing.T) {
	d, c := openDrawer(t, 160, 40)
	d, _, _ = d.update(drawerLoadedMsg{PRID: "a", Seq: 2, Reviews: []store.Review{reviewAt("new", "m", 0)}}, c)
	d, _, _ = d.update(drawerLoadedMsg{PRID: "a", Seq: 1, Reviews: []store.Review{reviewAt("old", "m", time.Hour)}}, c)
	if d.current() == nil || d.current().ID != "new" {
		t.Errorf("a stale load replaced a newer one: %+v", d.current())
	}
}

func TestDrawerSeqIncrementsPerLoad(t *testing.T) {
	h := drawerHarness(t, opts{})
	h.press(t, "enter")
	first := h.m.drawer.issued
	h.m, _ = settle(t, h.m, key("enter"))
	if first < 1 || h.m.drawer.issued <= first {
		t.Errorf("issued %d then %d, want it to grow with each load", first, h.m.drawer.issued)
	}
}

func TestDrawerShowsNoHyperlinks(t *testing.T) {
	r := drawerReview()
	r.Findings[0].Body = "See [the docs](https://evil.example/phish) and <https://x.example>."
	d, c := openDrawer(t, 160, 40, r)
	d, _, _ = d.update(key("n"), c)
	d, _, _ = d.update(key("n"), c)
	d, _, _ = d.update(key("n"), c)
	raw := renderDrawer(d, c, 40)
	if strings.Contains(raw, "\x1b]8;") {
		t.Errorf("an OSC 8 hyperlink reaches the screen: %q", raw)
	}
	if plain := ansi.Strip(raw); !strings.Contains(plain, "the docs") || !strings.Contains(plain, "https://evil.example/phish") {
		t.Errorf("the link's text and URL should both show:\n%s", plain)
	}
}
