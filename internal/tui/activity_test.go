package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
)

// timeline is PR 1's activity, oldest first: your review 240 minutes
// before t0 divides it, with a bot comment on each side.
func timeline() []github.ActivityItem {
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	return []github.ActivityItem{
		{Kind: github.ActivityCommit, Author: "sam", At: at(-300), OID: "aaaaaaa1", Text: "First pass", URL: "https://github.com/acme/widgets/pull/1/commits/aaaaaaa1"},
		{Kind: github.ActivityComment, Author: "ci", Bot: true, At: at(-290), Text: "preview ready", URL: "https://github.com/acme/widgets/pull/1#issuecomment-1"},
		{Kind: github.ActivityReview, Author: "me", At: at(-240), State: "CHANGES_REQUESTED", Text: "fix the retry"},
		{Kind: github.ActivityCommit, Author: "sam", At: at(-60), OID: "bbbbbbb2", Text: "Fix the retry"},
		{Kind: github.ActivityCommit, Author: "sam", At: at(-59), OID: "ccccccc3", Text: "Test the retry"},
		{Kind: github.ActivityForcePush, Author: "sam", At: at(-50), BeforeOID: "ccccccc3", OID: "ddddddd4"},
		{Kind: github.ActivityReview, Author: "ann", At: at(-40), State: "APPROVED", InlineComments: 2, Text: "LGTM", URL: "https://github.com/acme/widgets/pull/1#pullrequestreview-9"},
		{Kind: github.ActivityComment, Author: "ci", Bot: true, At: at(-30), Text: "coverage up"},
		{Kind: github.ActivityComment, Author: "ann", At: at(-20), Text: "One more thing.\n\nCan we log the retry count?\nThird line, cut."},
	}
}

func yourReview() *github.Review {
	return &github.Review{Author: "me", State: "CHANGES_REQUESTED", SubmittedAt: t0.Add(-240 * time.Minute)}
}

// activityHarness is a Wall with PR 1, which you reviewed, and a client
// that answers its timeline.
func activityHarness(t *testing.T) *harness {
	t.Helper()
	h := drawerHarness(t, opts{})
	h.client.activity = map[string][]github.ActivityItem{"1": timeline()}
	pr := h.m.prs["1"]
	pr.Status.YourReview = yourReview()
	h.m.prs["1"] = pr
	return h
}

func TestActivityFetchDoesNotBlockOpen(t *testing.T) {
	h := activityHarness(t)
	m, cmd := update(t, h.m, key("enter"))
	if !m.drawer.isOpen() {
		t.Fatal("enter didn't open the drawer at once")
	}
	if a := m.activity["1"]; !a.Loading || a.Loaded {
		t.Fatalf("activity on open: %+v, want a fetch on its way", a)
	}
	if len(h.client.activityCalls) != 0 {
		t.Fatal("the fetch ran inside Update")
	}
	m.drawer.tab = tabActivity
	m.drawer.pos = m.drawer.target // as if it had slid in before the answer
	if !strings.Contains(ansi.Strip(content(m)), activityLoading) {
		t.Errorf("the tab before the answer should say %q:\n%s", activityLoading, ansi.Strip(content(m)))
	}
	msgs, _ := runCmd(t, cmd)
	m, _ = settle(t, m, msgs...)
	if a := m.activity["1"]; !a.Loaded || a.Loading || len(a.Items) != len(timeline()) {
		t.Errorf("after the answer: %+v", a)
	}
	if len(h.client.activityCalls) != 1 {
		t.Errorf("fetches %v, want one", h.client.activityCalls)
	}
}

func TestActivityStaleFetchDropped(t *testing.T) {
	h := activityHarness(t)
	m, first := h.m.fetchActivity("1")
	// Pretend the first failed early, so a second can start.
	m.activity["1"] = activityState{seq: m.activity["1"].seq, Err: "boom"}
	m, second := m.fetchActivity("1")
	older, _ := runCmd(t, first)
	newer, _ := runCmd(t, second)
	m, _ = settle(t, m, newer...)
	stale := older[0].(activityMsg)
	stale.Items = []github.ActivityItem{{Kind: github.ActivityComment, Author: "stale", Text: "old answer"}}
	m, _ = settle(t, m, stale)
	if a := m.activity["1"]; len(a.Items) != len(timeline()) {
		t.Errorf("the older answer replaced the newer: %+v", a.Items)
	}
}

func TestActivityCache(t *testing.T) {
	h := activityHarness(t)
	h.press(t, "enter")
	settleDrawer(t, h)
	h.press(t, "esc")
	h.press(t, "enter")
	if n := len(h.client.activityCalls); n != 1 {
		t.Fatalf("reopening an unchanged PR fetched again: %d fetches", n)
	}
	h.press(t, "esc")
	pr := h.m.prs["1"]
	pr.PR.UpdatedAt = pr.PR.UpdatedAt.Add(time.Minute)
	h.m.prs["1"] = pr
	h.press(t, "enter")
	if n := len(h.client.activityCalls); n != 2 {
		t.Errorf("reopening a changed PR: %d fetches, want 2", n)
	}
}

func TestActivityRateLimitedAndFailed(t *testing.T) {
	h := activityHarness(t)
	h.m.quota.ObserveRateLimited(t0.Add(time.Hour))
	h.press(t, "enter")
	settleDrawer(t, h)
	h.press(t, "l", "l")
	if len(h.client.activityCalls) != 0 {
		t.Error("a fetch started while rate limited")
	}
	if got := ansi.Strip(content(h.m)); !strings.Contains(got, activityRateLimited) {
		t.Errorf("rate limited tab:\n%s", got)
	}

	h = activityHarness(t)
	h.client.activityErr = errors.New("502 Bad Gateway")
	h.press(t, "enter")
	settleDrawer(t, h)
	h.press(t, "l", "l")
	if got := ansi.Strip(content(h.m)); !strings.Contains(got, "could not load activity: 502 Bad Gateway") {
		t.Errorf("failed tab:\n%s", got)
	}
	if n := len(h.client.activityCalls); n != 1 {
		t.Errorf("a failure retried on its own: %d fetches", n)
	}
	h.client.activityErr = nil
	h.press(t, "esc", "enter")
	if n := len(h.client.activityCalls); n != 2 || !h.m.activity["1"].Loaded {
		t.Errorf("the next open after a failure: %d fetches, loaded %v", n, h.m.activity["1"].Loaded)
	}
}

func TestActivityRefetchOnChange(t *testing.T) {
	h := activityHarness(t)
	h.press(t, "enter")
	changed := h.m.prs["1"].PR
	changed.UpdatedAt = changed.UpdatedAt.Add(time.Minute)
	h.client.found = []string{"1"}
	h.client.snaps = map[string]github.Snapshot{"1": changed}
	h.press(t, "R")
	if n := len(h.client.activityCalls); n != 2 {
		t.Errorf("a poll that changed the open PR: %d fetches, want 2", n)
	}
	if got := h.m.activity["1"].UpdatedAt; !got.Equal(changed.UpdatedAt) {
		t.Errorf("cached at %v, want the new updatedAt %v", got, changed.UpdatedAt)
	}
}

// activityDrawer is a drawer on the Activity tab with timeline() loaded.
func activityDrawer(t *testing.T, height int) (drawerModel, drawerCtx) {
	t.Helper()
	d, c := openDrawer(t, 160, height, drawerReview())
	c.PR.Status.YourReview = yourReview()
	c.Viewer = "me"
	c.Activity = activityState{Loaded: true, Items: timeline()}
	for range 2 {
		d, _, _ = d.update(key("l"), c)
	}
	if d.tab != tabActivity {
		t.Fatalf("tab %d, want Activity", d.tab)
	}
	return d, c
}

func TestDrawerActivityGolden(t *testing.T) {
	d, c := activityDrawer(t, 30)
	golden(t, "drawer_activity.golden", renderDrawer(d, c, 30))
}

func TestActivityTabKeys(t *testing.T) {
	d, c := activityDrawer(t, 40)
	act := func(k string) action {
		var a action
		d, _, a = d.update(key(k), c)
		return a
	}
	// Listed: ann's comment, the bot fold, ann's approval, the force-push,
	// sam's two commits as one run; then below the divider sam's first
	// commit. Bots are folded.
	if a, ok := act("y").(actionCopy); !ok || !strings.HasPrefix(a.Text, "One more thing.") {
		t.Errorf("y on ann's comment: %#v", a)
	}
	if a, ok := act("O").(actionOpen); !ok || a.URL != c.PR.PR.URL {
		t.Errorf("O on a comment with no link: %#v, want the PR", a)
	}
	act("n")
	if a, ok := act("O").(actionOpen); !ok || a.URL != "https://github.com/acme/widgets/pull/1#pullrequestreview-9" {
		t.Errorf("O on ann's review: %#v", a)
	}
	act("n")
	act("n")
	if a, ok := act("y").(actionCopy); !ok || a.Text != "Test the retry\nFix the retry" {
		t.Errorf("y on the commit run: %#v", a)
	}
	act("J")
	if d.actSel != 4 {
		t.Errorf("J: sel %d, want the last entry, 4", d.actSel)
	}
	act("b")
	act("J")
	if d.actSel != 6 {
		t.Errorf("J with bots shown: sel %d, want 6", d.actSel)
	}
	act("b")
	if d.actSel != 4 {
		t.Errorf("b folding the selected bot: sel %d, want 4", d.actSel)
	}
	act("K")
	if d.actSel != 0 {
		t.Errorf("K: sel %d, want 0", d.actSel)
	}
}

func TestActivityTabCleansText(t *testing.T) {
	d, c := activityDrawer(t, 30)
	c.Activity.Items = []github.ActivityItem{{
		Kind: github.ActivityComment, Author: "evil\x1b[31mname", At: t0.Add(-time.Minute),
		Text: "red\x1b[31m text\nsecond " + strings.Repeat("x", 300),
	}}
	out := renderDrawer(d, c, 30)
	if strings.Contains(out, "\x1b[31m") {
		t.Error("an escape sequence from the comment reached the screen")
	}
	for i, l := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(l); w != d.width() {
			t.Errorf("line %d is %d columns wide, want %d: %q", i, w, d.width(), ansi.Strip(l))
		}
	}
}

func TestActivityTabLoadingAndEmpty(t *testing.T) {
	d, c := activityDrawer(t, 20)
	c.Activity = activityState{Loading: true}
	if got := ansi.Strip(renderDrawer(d, c, 20)); !strings.Contains(got, activityLoading) {
		t.Errorf("loading:\n%s", got)
	}
	if _, _, a := d.update(key("O"), c); a != (actionNone{}) {
		t.Errorf("O while loading: %#v, want nothing", a)
	}
	c.Activity = activityState{Loaded: true}
	c.PR.Status.YourReview = nil
	if got := ansi.Strip(renderDrawer(d, c, 20)); !strings.Contains(got, activityNone) {
		t.Errorf("empty:\n%s", got)
	}
}
