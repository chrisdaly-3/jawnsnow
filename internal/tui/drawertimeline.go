package tui

import (
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/github"
	"github.com/chrisdaly-3/jawnsnow/internal/status"
	"github.com/chrisdaly-3/jawnsnow/internal/theme"
)

// Messages the Activity tab shows in place of a timeline.
const (
	activityLoading     = "Loading…"
	activityRateLimited = "Rate limited — activity loads after GitHub's limit resets"
	activityNone        = "No activity"
	activityOlder       = "Older activity isn't shown."
	activityBodyLines   = 2 // G5
	activityRunHeadings = 3 // G4: headlines listed under a commit run
)

// activityView groups the drawer's PR's timeline around your latest review.
func activityView(c drawerCtx) status.ActivityView {
	return status.GroupActivity(c.Activity.Items, c.PR.Status.YourReview, c.Viewer)
}

// shownActivity is the Activity tab's selectable entries in the order it
// lists them: each side's people, then its bots when they're shown.
func shownActivity(v status.ActivityView, showBots bool) []status.ActivityEntry {
	var out []status.ActivityEntry
	for _, s := range []status.ActivitySide{v.Since, v.Before} {
		out = append(out, s.Entries...)
		if showBots {
			out = append(out, s.Bots...)
		}
	}
	return out
}

// activityLayout is the Activity tab under the header (plan 5): the
// entries since your review, the divider, the older ones, each side's bots
// folded at its bottom. spans holds where each selectable entry sits,
// in shownActivity order.
func (m drawerModel) activityLayout(c drawerCtx, lines []string, inner int) ([]string, []itemSpan) {
	th, a := c.Theme, c.Activity
	lines = append(lines, "")
	if a.Err != "" {
		lines = append(lines, ansi.Truncate(th.Accent().Render("could not load activity: "+cleanLine(a.Err)), inner, "…"))
	}
	switch {
	case !a.Loaded && a.RateLimited:
		return append(lines, th.Muted().Render(activityRateLimited)), nil
	case !a.Loaded && a.Err != "":
		return lines, nil
	case !a.Loaded:
		return append(lines, th.Muted().Render(activityLoading)), nil
	}
	v := activityView(c)
	shown := shownActivity(v, m.showBots)
	if len(shown) == 0 && len(v.Since.Bots)+len(v.Before.Bots) == 0 && v.Yours == nil {
		return append(lines, th.Muted().Render(activityNone)), nil
	}
	sel := min(m.actSel, max(len(shown)-1, 0))
	var spans []itemSpan
	i := 0
	entries := func(es []status.ActivityEntry) {
		for _, e := range es {
			start := len(lines)
			lines = append(lines, activityEntryLines(th, e, i == sel, c.Now, inner)...)
			spans = append(spans, itemSpan{start: start, head: len(lines), end: len(lines)})
			i++
		}
	}
	sideLines := func(s status.ActivitySide) {
		entries(s.Entries)
		if len(s.Bots) == 0 {
			return
		}
		mark := map[bool]string{true: "▾ ", false: "▸ "}[m.showBots]
		noun := "bot items"
		if len(s.Bots) == 1 {
			noun = "bot item"
		}
		lines = append(lines, ansi.Truncate(th.Muted().Render("  "+mark+strconv.Itoa(len(s.Bots))+" "+noun+foldHint("b", m.showBots)), inner, "…"))
		if m.showBots {
			entries(s.Bots)
		}
	}
	sideLines(v.Since)
	if y := v.Yours; y != nil {
		lines = append(lines, "", yourReviewDivider(th, *y, c.Now, inner), "")
		sideLines(v.Before)
	}
	if len(a.Items) >= github.ActivityLimit {
		lines = append(lines, "", th.Muted().Render(activityOlder))
	}
	return lines, spans
}

// yourReviewDivider is the line at your latest review (G3):
// "── your review · approved · 3h ago ──", filled to the drawer's width.
func yourReviewDivider(th theme.Theme, r github.Review, now time.Time, inner int) string {
	text := "── your review · " + reviewVerb(r.State) + " · " + activityAgo(now.Sub(r.SubmittedAt)) + " "
	if w := ansi.StringWidth(text); w < inner {
		text += strings.Repeat("─", inner-w)
	}
	return th.Accent().Render(ansi.Truncate(text, inner, ""))
}

func reviewVerb(state string) string {
	switch state {
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "changes requested"
	case "DISMISSED":
		return "dismissed"
	}
	return "commented"
}

// activityAgo is an age in seconds, minutes or hours, then days past two.
func activityAgo(d time.Duration) string {
	if d >= 48*time.Hour {
		return strconv.Itoa(int(d/(24*time.Hour))) + "d ago"
	}
	return ago(d) + " ago"
}

// activityGlyph is an entry's mark (G7): shape for the kind, and colour for
// a review's outcome.
func activityGlyph(th theme.Theme, it github.ActivityItem) string {
	switch it.Kind {
	case github.ActivityCommit:
		return th.Accent().Render("◆")
	case github.ActivityForcePush:
		return th.Accent().Render("↻")
	case github.ActivityReview:
		switch it.State {
		case "APPROVED":
			return th.StatusFg("APPROVED").Render("✓")
		case "CHANGES_REQUESTED":
			return th.StatusFg("CHANGES_REQUESTED").Render("✗")
		}
		return th.Accent().Render("◇")
	}
	return th.Accent().Render("›")
}

func shortOID(oid string) string { return oid[:min(len(oid), 7)] }

// activityEntryLines draws one entry: its headline, then for a commit run
// up to three headlines, and for a review or comment its first lines (G5).
// A bot's entry is muted throughout.
func activityEntryLines(th theme.Theme, e status.ActivityEntry, selected bool, now time.Time, inner int) []string {
	it := e.Item
	mark := "  "
	if selected {
		mark = th.Accent().Render("▸ ")
	}
	who := cleanLine(it.Author)
	if who == "" {
		who = "ghost"
	}
	var what string
	var meta []string
	var body []string
	switch it.Kind {
	case github.ActivityCommit:
		if len(e.Commits) > 1 {
			what = "pushed " + strconv.Itoa(len(e.Commits)) + " commits"
			for i, cm := range e.Commits {
				if i == activityRunHeadings {
					body = append(body, "+"+strconv.Itoa(len(e.Commits)-i)+" more")
					break
				}
				body = append(body, shortOID(cleanLine(cm.OID))+" "+cleanLine(cm.Text))
			}
		} else {
			// The headline goes under the line, as a run's do, so a long
			// one can't push the age off the edge.
			what = "pushed " + shortOID(cleanLine(it.OID))
			body = []string{cleanLine(it.Text)}
		}
	case github.ActivityForcePush:
		what = "force-pushed " + shortOID(cleanLine(it.BeforeOID)) + " → " + shortOID(cleanLine(it.OID))
	case github.ActivityReview:
		what = map[string]string{"APPROVED": "approved", "CHANGES_REQUESTED": "requested changes", "DISMISSED": "review dismissed"}[it.State]
		if what == "" {
			what = "reviewed"
		}
		if n := it.InlineComments; n > 0 {
			meta = append(meta, strconv.Itoa(n)+map[bool]string{true: " comment", false: " comments"}[n == 1])
		}
		body = excerpt(it.Text)
	default:
		what = "commented"
		body = excerpt(it.Text)
	}
	meta = append(meta, activityAgo(now.Sub(it.At)))

	head := activityGlyph(th, it) + " " + who + " " + what
	tail := " · " + strings.Join(meta, " · ")
	if it.Bot {
		head = th.Muted().Render("· " + who + " " + what)
	}
	out := []string{ansi.Truncate(mark+head+th.Muted().Render(tail), inner, "…")}
	for _, b := range body {
		out = append(out, ansi.Truncate(th.Muted().Render("    "+b), inner, "…"))
	}
	return out
}

// excerpt is a body's first non-blank lines, each cleaned to one line.
func excerpt(text string) []string {
	var out []string
	for l := range strings.SplitSeq(text, "\n") {
		if l = cleanLine(l); l != "" {
			out = append(out, l)
			if len(out) == activityBodyLines {
				break
			}
		}
	}
	return out
}

// activityKey handles a key on the Activity tab (G6).
func (m drawerModel) activityKey(msg tea.KeyPressMsg, c drawerCtx) (drawerModel, tea.Cmd, action) {
	_, spans := m.layout(c)
	shown := shownActivity(activityView(c), m.showBots)
	if !c.Activity.Loaded {
		shown = nil
	}
	m.actSel = min(m.actSel, max(len(shown)-1, 0))
	switch k := msg.String(); k {
	case "j", "down":
		m.actScroll++
	case "k", "up":
		m.actScroll--
	case "n", "N":
		if len(shown) > 0 && len(spans) == len(shown) {
			m.actSel = min(max(m.actSel+map[string]int{"n": 1, "N": -1}[k], 0), len(shown)-1)
			m.actScroll = scrollToItem(m.actScroll, spans[m.actSel], m.bodyHeight(c.Height))
		}
	case "b":
		m.showBots = !m.showBots
		if c.Activity.Loaded {
			m.actSel = min(m.actSel, max(len(shownActivity(activityView(c), m.showBots))-1, 0))
		}
	case "O":
		if len(shown) > 0 {
			return m, nil, actionOpen{URL: activityURL(c.PR.PR.URL, shown[m.actSel].Item)}
		}
	case "y":
		if len(shown) > 0 {
			m.flash = "copied the entry"
			return m, nil, actionCopy{Text: activityText(shown[m.actSel])}
		}
	default:
		return m.sharedKey(k, c)
	}
	m.actScroll = min(max(m.actScroll, 0), m.maxScroll(c))
	return m, nil, actionNone{}
}

// activityURL is the entry's page, or the PR for one with none, such as a
// force-push.
func activityURL(prURL string, it github.ActivityItem) string {
	if u := it.URL; strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
		return u
	}
	return prURL
}

// activityText is what y copies: a body, a commit's headline, or a run's
// headlines, one to a line.
func activityText(e status.ActivityEntry) string {
	if len(e.Commits) > 1 {
		hs := make([]string, len(e.Commits))
		for i, cm := range e.Commits {
			hs[i] = cleanLine(cm.Text)
		}
		return strings.Join(hs, "\n")
	}
	return cleanBlock(e.Item.Text)
}
