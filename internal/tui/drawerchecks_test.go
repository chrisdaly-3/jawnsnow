package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/status"
)

// mixedChecks is mono#86292's shape: an optional failure, a check held for
// a reviewer, a code-owner gate the config ignores, and passing checks.
func mixedChecks() []github.Check {
	return []github.Check{
		{Name: "build", State: github.CheckSuccess, Required: true},
		{Name: "skillup", State: github.CheckFailure, URL: "https://github.com/chainguard-dev/mono/actions/runs/1/job/2"},
		{Name: "presubmit / tf-verify-sentinel-chainops", State: github.CheckActionRequired},
		{Name: "codeowners-gate", State: github.CheckPending, Required: true},
		{Name: "lint", State: github.CheckSuccess},
		{Name: "e2e", State: github.CheckPending},
	}
}

// checksDrawer opens a drawer on a PR with mixedChecks, ignoring
// codeowners-gate, and switches it to the Checks tab.
func checksDrawer(t *testing.T, height int) (drawerModel, drawerCtx) {
	t.Helper()
	d, c := openDrawer(t, 160, height, drawerReview())
	c.PR.PR.Checks = mixedChecks()
	c.IgnoreChecks = []string{"codeowners-gate*"}
	d, _, _ = d.update(key("l"), c)
	if d.tab != tabChecks {
		t.Fatalf("l: tab %d, want the Checks tab", d.tab)
	}
	return d, c
}

func TestLaneLineEdgeCases(t *testing.T) {
	th := testTheme(t)
	line := func(checks []github.Check, ignore []string, inner int) string {
		return ansi.Strip(strings.Join(laneLines(status.Classify(checks, ignore), th, inner), "\n"))
	}
	if got := line(nil, nil, 60); got != "No checks" {
		t.Errorf("no checks: %q", got)
	}
	gate := []github.Check{{Name: "codeowners-gate", State: github.CheckPending}}
	if got := line(gate, []string{"codeowners-gate"}, 60); got != "◌ 1 ignored" {
		t.Errorf("only ignored: %q", got)
	}
	ok := []github.Check{{Name: "build", State: github.CheckSuccess}, {Name: "lint", State: github.CheckSkipped}}
	if got := line(ok, nil, 60); got != "✓ 2 passed" {
		t.Errorf("only passed: %q", got)
	}
	// Passed checks always get a line of their own, even with room to spare.
	want := "✗ 1 failed · ⏸ 1 waiting on you · ● 1 running · ◌ 1 ignored\n✓ 2 passed"
	if got := line(mixedChecks(), []string{"codeowners-gate*"}, 200); got != want {
		t.Errorf("one of each:\n got %q\nwant %q", got, want)
	}
	// Too wide for one line: two groups to a line, the second column
	// lined up.
	columns := "✗ 1 failed    ⏸ 1 waiting on you\n● 1 running   ◌ 1 ignored\n✓ 2 passed"
	if got := line(mixedChecks(), []string{"codeowners-gate*"}, 50); got != columns {
		t.Errorf("two columns:\n got %q\nwant %q", got, columns)
	}
	// Too narrow for two columns: one group to a line, cut with … when one
	// is wider than the drawer.
	single := "✗ 1 failed\n⏸ 1 waiting…\n● 1 running\n◌ 1 ignored\n✓ 2 passed"
	if got := line(mixedChecks(), []string{"codeowners-gate*"}, 12); got != single {
		t.Errorf("one column:\n got %q\nwant %q", got, single)
	}
}

func TestDrawerChecksGolden(t *testing.T) {
	d, c := checksDrawer(t, 30)
	golden(t, "drawer_checks.golden", renderDrawer(d, c, 30))
}

func TestDrawerTabSwitch(t *testing.T) {
	d, c := openDrawer(t, 160, 12, drawerReview()) // short: it has to scroll
	c.PR.PR.Checks = mixedChecks()
	d, _, _ = d.update(key("j"), c)
	d, _, _ = d.update(key("j"), c)
	reviewScroll := d.scroll
	if reviewScroll == 0 {
		t.Fatal("setup: the Review tab didn't scroll")
	}
	d, _, _ = d.update(key("h"), c)
	if d.tab != tabReview {
		t.Errorf("h on the first tab: tab %d, want it to stay on Review", d.tab)
	}
	if row, want := d.tabRow(c.Theme), c.Theme.Tab("Review", true)+" "+c.Theme.Tab("Checks", false); row != want {
		t.Errorf("tab row on Review = %q, want Review filled: %q", row, want)
	}
	d, _, _ = d.update(key("l"), c)
	d, _, _ = d.update(key("l"), c)
	if d.tab != tabChecks {
		t.Errorf("l past the last tab: tab %d, want it to stay on Checks", d.tab)
	}
	d, _, _ = d.update(key("j"), c)
	if d.scroll != reviewScroll || d.checkScroll != 1 {
		t.Errorf("j on Checks: review scroll %d, checks scroll %d; want %d and 1", d.scroll, d.checkScroll, reviewScroll)
	}
	d, _, _ = d.update(key("h"), c)
	if d.tab != tabReview || d.scroll != reviewScroll {
		t.Errorf("back to Review: tab %d scroll %d, want Review at %d", d.tab, d.scroll, reviewScroll)
	}
	if !strings.Contains(ansi.Strip(renderDrawer(d, c, 12)), "n/N finding") {
		t.Errorf("the Review tab's hints are missing:\n%s", ansi.Strip(renderDrawer(d, c, 12)))
	}

	// enter opens the drawer on the Review tab, even on the same PR.
	d, _, _ = d.update(key("l"), c)
	d, _ = d.show("a", 160)
	if d.tab != tabReview {
		t.Errorf("show: tab %d, want Review", d.tab)
	}
}

func TestChecksTabKeys(t *testing.T) {
	d, c := checksDrawer(t, 40)
	// Shown in order: skillup (failing), sentinel (held), e2e (running),
	// codeowners-gate (ignored); passed checks are folded.
	act := func(k string) action {
		var a action
		d, _, a = d.update(key(k), c)
		return a
	}
	if a, ok := act("O").(actionOpen); !ok || a.URL != "https://github.com/chainguard-dev/mono/actions/runs/1/job/2" {
		t.Errorf("O on skillup: %#v, want its own page", a)
	}
	if a, ok := act("y").(actionCopy); !ok || a.Text != "skillup" {
		t.Errorf("y on skillup: %#v", a)
	}
	act("n")
	if a, ok := act("O").(actionOpen); !ok || a.URL != c.PR.PR.URL+"/checks" {
		t.Errorf("O on a check with no link: %#v, want the PR's checks page", a)
	}
	for range 10 {
		act("n")
	}
	if d.checkSel != 3 {
		t.Errorf("n past the last shown check: sel %d, want 3", d.checkSel)
	}
	act("s")
	for range 10 {
		act("n")
	}
	if d.checkSel != 5 {
		t.Errorf("n with passed checks shown: sel %d, want 5", d.checkSel)
	}
	act("s")
	if d.checkSel != 3 {
		t.Errorf("s folding the selected passed check: sel %d, want 3", d.checkSel)
	}
	for range 10 {
		act("N")
	}
	if d.checkSel != 0 {
		t.Errorf("N past the first check: sel %d, want 0", d.checkSel)
	}
	if a, ok := act("o").(actionOpen); !ok || a.URL != c.PR.PR.URL {
		t.Errorf("o on the Checks tab: %#v, want the PR", a)
	}
	if _, ok := act("space").(actionReview); !ok {
		t.Error("space on the Checks tab should review the PR")
	}
}

func TestChecksTabCleansNames(t *testing.T) {
	d, c := openDrawer(t, 160, 30, drawerReview())
	c.PR.PR.Checks = []github.Check{{
		Name:  "evil\x1b[31m red\nsecond line " + strings.Repeat("x", 300),
		State: github.CheckFailure,
	}}
	d, _, _ = d.update(key("l"), c)
	out := renderDrawer(d, c, 30)
	if strings.Contains(out, "\x1b[31m red") {
		t.Error("the check name's escape sequence reached the screen")
	}
	for i, l := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(l); w != d.width() {
			t.Errorf("line %d is %d columns wide, want %d: %q", i, w, d.width(), ansi.Strip(l))
		}
	}
	if !strings.Contains(ansi.Strip(out), "evil") || strings.Contains(ansi.Strip(out), "\nsecond line") {
		t.Errorf("the name should show on one cleaned line:\n%s", ansi.Strip(out))
	}
}

func TestDrawerChecksFollowConfigReload(t *testing.T) {
	pr := wallPR("1")
	pr.PR.Checks = []github.Check{{Name: "codeowners-gate", State: github.CheckPending, Required: true}}
	h := newHarness(t, opts{}, pr)
	h.m.drawer, _ = h.m.drawer.show("1", 160)
	lane := func() string {
		c := h.m.drawerCtx()
		return ansi.Strip(strings.Join(laneLines(checkRows(c), c.Theme, 80), "\n"))
	}
	if got := lane(); got != "● 1 running" {
		t.Fatalf("before the reload: %q", got)
	}
	cfg := config.Defaults()
	cfg.GitHub.IgnoreChecks = []string{"codeowners-gate*"}
	h.m, _ = update(t, h.m, reloadMsg{Config: cfg})
	if got := lane(); got != "◌ 1 ignored" {
		t.Errorf("after ignoring codeowners-gate: %q, want it ignored with no poll", got)
	}
}
