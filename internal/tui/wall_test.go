package tui

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/status"
	"github.com/chrisdaly-3/jawnsnow/internal/theme"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files")

func testTheme(t *testing.T) theme.Theme {
	t.Helper()
	th, err := theme.New(config.ThemeConfig{})
	if err != nil {
		t.Fatalf("theme.New: %v", err)
	}
	return th
}

// wallCard is a wall PR with the given status, seen age ago.
func wallCard(id, repo string, n int, title, author string, st status.Status, spinning bool, add, del int, age time.Duration) tracker.Tracked {
	return tracker.Tracked{
		PR: github.Snapshot{
			ID: id, Repo: repo, Number: n, Title: title, Author: author,
			URL: "https://github.com/" + repo + "/pull/" + id, State: github.PROpen,
			Additions: add, Deletions: del,
		},
		Status: status.Result{Status: st, Spinning: spinning},
		Place:  tracker.PlaceWall, HeadSeenAt: t0.Add(-age),
	}
}

// wallPRs is eight PRs over all five lanes: CI 2, Ready 2, New 2, Author 1,
// Approved 1.
func wallPRs() []tracker.Tracked {
	return []tracker.Tracked{
		wallCard("a", "chainguard-dev/mono", 85180, "feat(policies): fill Decision.repo_name from the registry", "alice", status.CIWaiting, true, 120, 8, 2*time.Hour),
		wallCard("b", "chainguard-dev/mono", 85190, "fix(registry): retry on 503 from the index", "dave", status.CIFailed, false, 9, 3, 3*time.Hour),
		wallCard("c", "chainguard-dev/mono", 85211, "fix(oidc): refresh the signing key before expiry", "bob", status.ReadyForReview, false, 14, 2, 5*time.Hour),
		wallCard("d", "chainguard-dev/os", 4012, "bump openssl to 3.5.1", "erin", status.ReadyForReview, false, 4, 4, 9*time.Hour),
		wallCard("e", "chainguard-dev/mono", 85102, "chore: bump driftlessaf", "carol", status.NewChanges, true, 300, 40, 26*time.Hour),
		wallCard("f", "chainguard-dev/enforce", 311, "refactor: split the poller", "frank", status.NewChanges, false, 52, 60, 50*time.Hour),
		wallCard("g", "chainguard-dev/mono", 85077, "feat(sentinel): add popularity projections", "gina", status.ChangesRequested, false, 77, 12, 30*time.Hour),
		wallCard("h", "chainguard-dev/mono", 85044, "docs: explain the deploy order", "me", status.ApprovedByYou, false, 20, 0, 70*time.Hour),
	}
}

// readyPR is a harness wall PR that has a status, so it lands in a lane.
func readyPR(id string) tracker.Tracked {
	t := wallPR(id)
	t.Status = status.Result{Status: status.ReadyForReview}
	return t
}

func testWall(width, height int) wallModel {
	w := newWall()
	w.now = t0
	w.setSize(width, height)
	return w
}

func renderWall(t *testing.T, w wallModel, width, height int, prs []tracker.Tracked) string {
	t.Helper()
	w.setSize(width, height)
	return w.view(width, height, prs, testTheme(t), 0)
}

func press(t *testing.T, w wallModel, prs []tracker.Tracked, keys ...string) (wallModel, action) {
	t.Helper()
	var a action = actionNone{}
	for _, k := range keys {
		w, _, a = w.update(key(k), prs)
	}
	return w, a
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	got = ansi.Strip(got) + "\n"
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the frame; run with -update and read it\n--- got ---\n%s", name, got)
	}
}

func TestWallGoldenWide(t *testing.T) {
	golden(t, "wall_wide.golden", renderWall(t, testWall(160, 30), 160, 30, wallPRs()))
}

func TestWallGoldenNarrow(t *testing.T) {
	golden(t, "wall_narrow.golden", renderWall(t, testWall(90, 30), 90, 30, wallPRs()))
}

func TestWallNavigation(t *testing.T) {
	prs := wallPRs()
	w := testWall(160, 30)
	w, _ = press(t, w, prs, "h")
	if lane, row := w.position(prs); lane != 0 || row != 0 {
		t.Errorf("h at the first lane: lane %d row %d, want 0 0", lane, row)
	}
	w, _ = press(t, w, prs, "l", "l")
	if lane, _ := w.position(prs); lane != int(status.LaneNew) {
		t.Errorf("l,l: lane %d, want New changes (%d)", lane, status.LaneNew)
	}
	w, _ = press(t, w, prs, "j")
	if _, row := w.position(prs); row != 1 {
		t.Errorf("j: row %d, want 1", row)
	}
	w, _ = press(t, w, prs, "j")
	if _, row := w.position(prs); row != 1 {
		t.Errorf("j past the last card: row %d, want to stay at 1", row)
	}
}

func TestWallNarrowNavigationWalksEveryCard(t *testing.T) {
	prs := wallPRs()
	w := testWall(90, 30)
	w, _ = press(t, w, prs, "j", "j", "j") // CI b, ready c ... order below
	// Order within lanes is newest HeadSeenAt first: CI a,b; Ready c,d; New e,f.
	if lane, row := w.position(prs); lane != int(status.LaneReady) || row != 1 {
		t.Errorf("j×3 narrow: lane %d row %d, want Ready row 1", lane, row)
	}
	w, _ = press(t, w, prs, "l")
	if lane, row := w.position(prs); lane != int(status.LaneNew) || row != 0 {
		t.Errorf("l narrow: lane %d row %d, want New row 0", lane, row)
	}
	w, _ = press(t, w, prs, "j", "h")
	if lane, row := w.position(prs); lane != int(status.LaneNew) || row != 0 {
		t.Errorf("h mid-lane: lane %d row %d, want its own heading (New row 0)", lane, row)
	}
}

func TestWallNarrowScrollSettles(t *testing.T) {
	prs := wallPRs()
	w := testWall(90, 12)
	for range 7 {
		w, _ = press(t, w, prs, "j")
	}
	if !w.animating || w.target <= 0 {
		t.Fatalf("moving down a long column: animating=%v target=%v, want an eased scroll", w.animating, w.target)
	}
	target := w.target
	var cmd tea.Cmd
	for i := 0; i < 1000 && w.animating; i++ {
		w, cmd, _ = w.update(wallTickMsg{}, prs)
	}
	if w.animating || cmd != nil {
		t.Fatalf("spring never settled (animating=%v)", w.animating)
	}
	if w.pos != target {
		t.Errorf("settled at %v, want the target %v", w.pos, target)
	}
	// The selected card is on screen.
	out := ansi.Strip(renderWall(t, w, 90, 12, prs))
	if !strings.Contains(out, "▌") {
		t.Errorf("selected card marker not visible after scrolling:\n%s", out)
	}
}

func TestWallTinyTerminal(t *testing.T) {
	for _, size := range [][2]int{{40, 10}, {1, 1}, {0, 0}, {10, 3}, {119, 4}, {120, 5}} {
		w := testWall(size[0], size[1])
		out := renderWall(t, w, size[0], size[1], wallPRs())
		for i, line := range strings.Split(out, "\n") {
			if lw := lipgloss.Width(line); lw > size[0] {
				t.Errorf("%v line %d is %d wide: %q", size, i, lw, line)
			}
		}
		if got := len(strings.Split(out, "\n")); size[1] > 0 && got > size[1] {
			t.Errorf("%v rendered %d lines", size, got)
		}
	}
}

func TestCardTruncatesByDisplayWidth(t *testing.T) {
	c := wallCard("x", "acme/widgets", 7, "修复 🐛 handle 長いタイトル…", "alice", status.ReadyForReview, false, 1, 1, time.Hour)
	lines := renderCard(c, 24, false, testTheme(t), t0, 0)
	for i, l := range lines {
		if w := lipgloss.Width(l); w > 24 {
			t.Errorf("card line %d is %d wide: %q", i, w, l)
		}
	}
	title := strings.TrimRight(ansi.Strip(lines[1]), " ")
	if !strings.HasSuffix(title, "…") {
		t.Errorf("title line %q does not end with …", title)
	}
}

func TestCardStripsControlSequencesFromTitle(t *testing.T) {
	c := wallCard("x", "acme/widgets", 7, "evil \x1b]0;pwned\x07\x1b[2J title", "al\x1bice", status.ReadyForReview, false, 1, 1, time.Hour)
	for _, l := range renderCard(c, 40, false, testTheme(t), t0, 0) {
		if p := ansi.Strip(l); strings.ContainsAny(p, "\x1b\x07") || strings.Contains(p, "pwned") {
			t.Errorf("control sequence survived: %q", l)
		}
	}
}

func TestCardGlyphs(t *testing.T) {
	th := testTheme(t)
	first := func(c tracker.Tracked) string { return ansi.Strip(renderCard(c, 30, false, th, t0, 0)[0]) }
	spin := wallCard("x", "a/b", 1, "t", "u", status.CIWaiting, true, 0, 0, time.Hour)
	if !strings.HasPrefix(first(spin), " ⣾ b#1") {
		t.Errorf("spinning card line 1 = %q", first(spin))
	}
	fail := wallCard("x", "a/b", 1, "t", "u", status.CIFailed, false, 0, 0, time.Hour)
	if !strings.HasPrefix(first(fail), " ✗ b#1") {
		t.Errorf("failed card line 1 = %q", first(fail))
	}
	plain := wallCard("x", "a/b", 1, "t", "u", status.ReadyForReview, false, 0, 0, time.Hour)
	if !strings.HasPrefix(first(plain), "   b#1") {
		t.Errorf("plain card line 1 = %q", first(plain))
	}
	for _, c := range []tracker.Tracked{spin, fail, plain, wallCard("x", "a/b", 1, "t", "u", status.ApprovedByYou, false, 0, 0, time.Hour)} {
		if n := len(renderCard(c, 30, false, th, t0, 0)); n != cardLines {
			t.Errorf("%s card has %d lines, want %d", c.Status.Status, n, cardLines)
		}
	}
}

func TestWallHideEmitsAction(t *testing.T) {
	prs := wallPRs()
	w := testWall(160, 30)
	_, a := press(t, w, prs, "x")
	if got, ok := a.(actionHide); !ok || got.ID != "a" {
		t.Errorf("x = %#v, want actionHide{a}", a)
	}
	_, a = press(t, w, prs, "o")
	if got, ok := a.(actionOpen); !ok || got.URL != prs[0].PR.URL {
		t.Errorf("o = %#v, want actionOpen of the first card", a)
	}
	_, a = press(t, testWall(160, 30), nil, "x")
	if _, ok := a.(actionNone); !ok {
		t.Errorf("x on an empty wall = %#v, want actionNone", a)
	}
}

func TestWallFilterText(t *testing.T) {
	prs := wallPRs()
	w, _ := press(t, testWall(160, 30), prs, "/", "o", "i", "d", "c")
	if !w.capturing() {
		t.Fatal("not capturing while the filter is open")
	}
	w, _ = press(t, w, prs, "enter")
	if w.capturing() {
		t.Error("still capturing after enter")
	}
	out := ansi.Strip(renderWall(t, w, 160, 30, prs))
	if !strings.Contains(out, "mono#85211") {
		t.Errorf("matching card missing:\n%s", out)
	}
	for _, gone := range []string{"mono#85180", "mono#85044", "os#4012"} {
		if strings.Contains(out, gone) {
			t.Errorf("%s should be filtered out:\n%s", gone, out)
		}
	}
	if !strings.Contains(out, "oidc") || !strings.Contains(out, "esc") {
		t.Errorf("footer does not show the active filter:\n%s", out)
	}
	w, _ = press(t, w, prs, "esc")
	if out := ansi.Strip(renderWall(t, w, 160, 30, prs)); !strings.Contains(out, "mono#85180") {
		t.Errorf("esc did not clear the filter:\n%s", out)
	}
}

func TestWallFilterMatchesRepoNumberAndAuthor(t *testing.T) {
	prs := wallPRs()
	for q, want := range map[string]string{"ENFORCE": "enforce#311", "4012": "os#4012", "GINA": "mono#85077", "#85044": "mono#85044"} {
		w, _ := press(t, testWall(160, 30), prs, "/")
		for _, r := range q {
			w, _ = press(t, w, prs, string(r))
		}
		w, _ = press(t, w, prs, "enter")
		if out := ansi.Strip(renderWall(t, w, 160, 30, prs)); !strings.Contains(out, want) {
			t.Errorf("filter %q: %s missing:\n%s", q, want, out)
		}
	}
}

func TestWallAuthorPicker(t *testing.T) {
	prs := wallPRs()
	w, _ := press(t, testWall(160, 30), prs, "a")
	if !w.capturing() {
		t.Fatal("not capturing while the picker is open")
	}
	out := ansi.Strip(renderWall(t, w, 160, 30, prs))
	for _, name := range []string{"alice", "bob", "gina"} {
		if !strings.Contains(out, name) {
			t.Errorf("picker is missing %s:\n%s", name, out)
		}
	}
	// Authors are sorted: alice, bob, carol, ... ; pick bob.
	w, _ = press(t, w, prs, "j", "enter")
	if w.capturing() {
		t.Error("still capturing after enter")
	}
	out = ansi.Strip(renderWall(t, w, 160, 30, prs))
	if !strings.Contains(out, "mono#85211") || strings.Contains(out, "mono#85180") {
		t.Errorf("author filter did not narrow the wall to bob:\n%s", out)
	}
	if !strings.Contains(out, "author: bob") {
		t.Errorf("footer does not show the author filter:\n%s", out)
	}
	w, _ = press(t, w, prs, "esc")
	if out := ansi.Strip(renderWall(t, w, 160, 30, prs)); !strings.Contains(out, "mono#85180") {
		t.Errorf("esc did not clear the author filter:\n%s", out)
	}
	w, _ = press(t, w, prs, "a", "esc")
	if w.capturing() {
		t.Error("esc left the picker open")
	}
}

func TestWallEmptyState(t *testing.T) {
	w := testWall(100, 10)
	w.feeds = []string{"review-requested", "mine"}
	out := ansi.Strip(w.view(100, 10, nil, testTheme(t), 0))
	if !strings.Contains(out, "Nothing on the wall. Feeds: review-requested, mine · press R to poll") {
		t.Errorf("empty state wrong:\n%s", out)
	}
}

func TestWallOnlyShowsWallPlace(t *testing.T) {
	prs := wallPRs()
	waiting := wallCard("w", "chainguard-dev/mono", 1111, "waiting pr", "x", status.ReadyForReview, false, 0, 0, time.Hour)
	waiting.Place = tracker.PlaceWaiting
	done := wallCard("d2", "chainguard-dev/mono", 2222, "done pr", "x", status.Merged, false, 0, 0, time.Hour)
	done.Place = tracker.PlaceDone
	out := ansi.Strip(renderWall(t, testWall(160, 30), 160, 30, append(prs, waiting, done)))
	if strings.Contains(out, "mono#1111") || strings.Contains(out, "mono#2222") {
		t.Errorf("non-wall PR rendered:\n%s", out)
	}
}

func TestWallSortsNewestHeadFirst(t *testing.T) {
	prs := wallPRs()
	// b is seen more recently than a, so it leads the CI lane.
	prs[1].HeadSeenAt = t0.Add(-time.Minute)
	w := testWall(160, 30)
	_, a := press(t, w, prs, "x")
	if got, ok := a.(actionHide); !ok || got.ID != "b" {
		t.Errorf("first CI card = %#v, want b", a)
	}
}

func TestWallLaneHeaderCountsAndSpinner(t *testing.T) {
	out := ansi.Strip(renderWall(t, testWall(160, 30), 160, 30, wallPRs()))
	first := strings.Split(out, "\n")[0]
	for _, want := range []string{"CI ⣾ 2", "Ready for you 2", "New changes ⣾ 2", "Waiting on author 1", "Approved 1"} {
		if !strings.Contains(first, want) {
			t.Errorf("header line missing %q: %q", want, first)
		}
	}
}

// The root model must give every key to the wall while its filter or picker
// is open; otherwise typing "q" in the filter would quit.
func TestWallCapturesKeysOverGlobalBindings(t *testing.T) {
	h := newHarness(t, opts{}, readyPR("1"), readyPR("22"))
	h.press(t, "/")
	h.press(t, "q", "1", "]")
	if h.m.view != ViewWall {
		t.Errorf("view = %v, want Wall", h.m.view)
	}
	if got := h.m.wall.input.Value(); got != "q1]" {
		t.Errorf("filter text = %q, want %q", got, "q1]")
	}
	h.press(t, "enter")
	_, cmd := update(t, h.m, key("q"))
	if cmd == nil {
		t.Fatal("q after enter returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q after the filter closed should quit")
	}
}

func TestWallPickerCapturesKeys(t *testing.T) {
	h := newHarness(t, opts{}, readyPR("1"))
	h.press(t, "a", "q", "]", "m")
	if h.m.view != ViewWall || h.m.mode != modeWatcher {
		t.Errorf("view %v mode %q: keys leaked to the global bindings", h.m.view, h.m.mode)
	}
	if !h.m.wall.capturing() {
		t.Error("picker closed unexpectedly")
	}
}

func TestRootHidesFromWall(t *testing.T) {
	h := newHarness(t, opts{noClient: true}, readyPR("1"))
	h.press(t, "x")
	if got := h.m.prs["1"]; got.Place != tracker.PlaceWaiting || got.WaitReason != tracker.WaitHidden {
		t.Errorf("after x place = %v reason %v, want hidden", got.Place, got.WaitReason)
	}
}

func TestRootOpensFromWall(t *testing.T) {
	h := newHarness(t, opts{noClient: true}, readyPR("1"))
	h.press(t, "o")
	if len(h.opened) != 1 || h.opened[0] != "https://github.com/acme/widgets/pull/1" {
		t.Errorf("opened %v", h.opened)
	}
}

func TestSpinnerTicksOnlyWhileSomethingSpins(t *testing.T) {
	spinning := readyPR("1")
	spinning.Status = status.Result{Status: status.CIWaiting, Spinning: true}
	h := newHarness(t, opts{noClient: true}, spinning)
	m, _ := settle(t, h.m)
	m, cmd := update(t, m, spinTickMsg{})
	if cmd == nil || m.spinFrame != 1 {
		t.Fatalf("spinning wall: frame %d cmd nil=%v, want the loop to continue", m.spinFrame, cmd == nil)
	}
	calm := readyPR("2")
	h2 := newHarness(t, opts{noClient: true}, calm)
	m2, cmd := update(t, h2.m, spinTickMsg{})
	if cmd != nil || m2.spinning {
		t.Errorf("calm wall: spin tick continued (cmd nil=%v spinning=%v)", cmd == nil, m2.spinning)
	}
}

func spinnerTheme(t *testing.T, over map[string]config.StatusStyle) theme.Theme {
	t.Helper()
	th, err := theme.New(config.ThemeConfig{Status: over})
	if err != nil {
		t.Fatalf("theme.New: %v", err)
	}
	return th
}

func TestCardUsesItsStatusSpinner(t *testing.T) {
	th := spinnerTheme(t, map[string]config.StatusStyle{"CI_WAITING": {Spinner: "line"}})
	c := wallCard("x", "a/b", 1, "t", "u", status.CIWaiting, true, 0, 0, time.Hour)
	for frame, want := range spinner.Line.Frames {
		got := ansi.Strip(renderCard(c, 30, false, th, t0, frame)[0])
		if !strings.HasPrefix(got, " "+strings.TrimSpace(want)+" b#1") {
			t.Errorf("frame %d: line 1 = %q, want spinner.Line frame %q", frame, got, want)
		}
	}
	lane := ansi.Strip(laneLabel(int(status.LaneCI), []tracker.Tracked{c}, th, 1, true))
	if !strings.Contains(lane, strings.TrimSpace(spinner.Line.Frames[1])) {
		t.Errorf("lane header %q does not use the card's spinner", lane)
	}
}

func TestNoneSpinnerShowsNoGlyphAndNoTick(t *testing.T) {
	over := map[string]config.StatusStyle{"NEW_CHANGES": {Spinner: "none"}}
	th := spinnerTheme(t, over)
	c := wallCard("x", "a/b", 1, "t", "u", status.NewChanges, true, 0, 0, time.Hour)
	if got := ansi.Strip(renderCard(c, 30, false, th, t0, 0)[0]); !strings.HasPrefix(got, "   b#1") {
		t.Errorf("none spinner: line 1 = %q, want a blank glyph", got)
	}
	if lane := ansi.Strip(laneLabel(int(status.LaneNew), []tracker.Tracked{c}, th, 0, true)); strings.ContainsAny(lane, "⣾⣽⣻⢿⡿⣟⣯⣷") {
		t.Errorf("lane header %q shows a spinner", lane)
	}

	h := newHarness(t, opts{noClient: true}, func() tracker.Tracked {
		p := readyPR("1")
		p.Status = status.Result{Status: status.NewChanges, Spinning: true}
		return p
	}())
	h.m.theme, h.m.spinning = th, false // the harness's load ran under the default theme
	m, cmd := update(t, h.m, clockMsg{})
	_, ticks := runCmd(t, cmd)
	for _, tk := range ticks {
		if _, ok := tk.msg.(spinTickMsg); ok {
			t.Error("a spin tick was scheduled for a spinner-less status")
		}
	}
	if m.spinning {
		t.Error("spinning set with only a none-spinner card")
	}
}

func TestSpinTickUsesFastestSpinner(t *testing.T) {
	h := newHarness(t, opts{noClient: true}, readyPR("1"))
	p := h.m.prs["1"]
	p.Status = status.Result{Status: status.CIWaiting, Spinning: true}
	h.m.prs["1"] = p
	h.m.theme = spinnerTheme(t, map[string]config.StatusStyle{"CI_WAITING": {Spinner: "line"}})
	if got := h.m.spinInterval(); got != spinner.Line.FPS {
		t.Errorf("interval = %v, want spinner.Line.FPS %v", got, spinner.Line.FPS)
	}
}

// On first launch every head is seen at once, so the age comes from the
// head commit's time when GitHub gave one.
func TestCardAgeUsesHeadCommitTime(t *testing.T) {
	for _, c := range []struct {
		name                         string
		committed, headSeen, firstAt time.Time
		want                         string
	}{
		{"commit time", t0.Add(-3 * time.Hour), t0, t0, "3h"},
		{"no commit time", time.Time{}, t0.Add(-20 * time.Minute), t0.Add(-time.Hour), "20m"},
		{"only first seen", time.Time{}, time.Time{}, t0.Add(-50 * time.Hour), "2d"},
		{"nothing", time.Time{}, time.Time{}, time.Time{}, "–"},
	} {
		tr := tracker.Tracked{PR: github.Snapshot{HeadCommittedAt: c.committed}, HeadSeenAt: c.headSeen, FirstSeen: c.firstAt}
		if got := cardAge(tr, t0); got != c.want {
			t.Errorf("%s: cardAge = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestOutlinedChipIsOneLineOnACard(t *testing.T) {
	out := ansi.Strip(renderWall(t, testWall(90, 12), 90, 12, wallPRs()[7:]))
	var chip string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "Approved by you") {
			chip = l
		}
	}
	if !strings.Contains(chip, "[Approved by you · needs more]") || strings.Contains(chip, "│") {
		t.Errorf("chip line = %q, want the whole label in brackets and no box:\n%s", chip, out)
	}
}
