package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/harmonica"
	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/theme"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
)

// drawerFraction is the drawer's share of the terminal once it has settled.
const drawerFraction = 0.45

// drawerTickMsg advances the drawer's slide one frame.
type drawerTickMsg struct{}

// drawerLoadedMsg carries a PR's stored reviews, newest first, and the
// newest post of each review that was posted.
type drawerLoadedMsg struct {
	PRID    string
	Seq     int // which load request this answers; see drawerModel.issued
	Reviews []store.Review
	Posted  map[string]store.Posted // by review ID
	Err     error
}

// drawerCtx is what the root model tells the drawer on every call. The
// drawer keeps none of it, so it can't go stale.
type drawerCtx struct {
	PR      tracker.Tracked
	Running bool
	Ticker  string // the running review's latest tool call
	Frame   int    // the spinner frame
	Focused bool
	Now     time.Time
	Theme   theme.Theme
	Tick    func(time.Duration, func(time.Time) tea.Msg) tea.Cmd
	// Height is the room the drawer gets, for scrolling to a finding.
	Height int
}

// drawerModel is the right-hand drawer: one PR's reviews, one pass at a
// time. It slides in with a spring, so its width is a float that eases to
// the target.
type drawerModel struct {
	open bool
	prID string

	pos, vel, target float64
	animating        bool
	spring           harmonica.Spring

	reviews []store.Review
	posted  map[string]store.Posted // the newest post of each review, by ID
	loaded  bool
	pass    int // index into reviews; 0 is the newest
	sel     int // the selected finding, in orderedFindings order
	scroll  int

	understanding, assumptions bool
	flash                      string // a note for the bottom line, until the next key
	// issued counts load requests; applied is the newest one whose answer
	// was taken, so a slow older read can't replace a newer one.
	issued, applied int

	// wantReview is the review ID to select once the PR's reviews load.
	wantReview string

	md *markdownCache
}

func newDrawer() drawerModel {
	return drawerModel{spring: harmonica.NewSpring(harmonica.FPS(60), 6.0, 1.0), md: newMarkdownCache()}
}

func (m drawerModel) isOpen() bool { return m.open }

// visible reports whether any of the drawer is on screen, which includes
// the time it takes to slide out.
func (m drawerModel) visible() bool { return m.width() > 0 }

// width is the drawer's current width in cells.
func (m drawerModel) width() int { return int(math.Round(max(m.pos, 0))) }

func drawerTarget(termW int) float64 { return math.Round(drawerFraction * float64(termW)) }

// startSlide starts the spring toward the model's target. The first frame
// comes at once; later ones from the update's ticks.
func (m drawerModel) startSlide() (drawerModel, tea.Cmd) {
	if m.animating {
		return m, nil // the running spring chases the new target
	}
	m.animating = true
	return m, func() tea.Msg { return drawerTickMsg{} }
}

// show opens the drawer on prID for a termW-wide terminal. Showing another
// PR keeps the drawer where it is and forgets the old PR's reviews.
func (m drawerModel) show(prID string, termW int) (drawerModel, tea.Cmd) {
	if m.prID != prID {
		m.reviews, m.posted, m.loaded, m.pass, m.sel, m.scroll = nil, nil, false, 0, 0, 0
		m.understanding, m.assumptions = false, false
	}
	m.wantReview = ""
	m.open, m.prID, m.target = true, prID, drawerTarget(termW)
	return m.startSlide()
}

// showReview opens the drawer on prID at the pass with reviewID. The PR's
// reviews load asynchronously, so an ID not on screen yet is remembered and
// selected when they arrive; if it isn't among them the default pass stays.
func (m drawerModel) showReview(prID, reviewID string, termW int) (drawerModel, tea.Cmd) {
	m, cmd := m.show(prID, termW)
	m.wantReview = reviewID
	if m.loaded {
		m = m.selectWanted(false)
	}
	return m, cmd
}

// selectWanted moves to the pass of wantReview. The want is dropped once
// found, or when final says no later load will bring it.
func (m drawerModel) selectWanted(final bool) drawerModel {
	if i := slices.IndexFunc(m.reviews, func(r store.Review) bool { return r.ID == m.wantReview }); i >= 0 {
		if i != m.pass {
			m.pass, m.sel, m.scroll = i, 0, 0
		}
		m.wantReview = ""
	} else if final {
		m.wantReview = ""
	}
	return m
}

// hide slides the drawer out.
func (m drawerModel) hide() (drawerModel, tea.Cmd) {
	m.open, m.target = false, 0
	return m.startSlide()
}

// resize re-aims an open drawer at its share of a new terminal width.
func (m drawerModel) resize(termW int) (drawerModel, tea.Cmd) {
	if !m.open {
		return m, nil
	}
	m.target = drawerTarget(termW)
	return m.startSlide()
}

func (m drawerModel) step() drawerModel {
	m.pos, m.vel = m.spring.Update(m.pos, m.vel, m.target)
	if math.Abs(m.pos-m.target) < 0.5 && math.Abs(m.vel) < 0.5 {
		m.pos, m.vel, m.animating = m.target, 0, false
	}
	return m
}

func (m drawerModel) update(msg tea.Msg, c drawerCtx) (drawerModel, tea.Cmd, action) {
	switch msg := msg.(type) {
	case drawerTickMsg:
		if !m.animating {
			return m, nil, actionNone{}
		}
		m = m.step()
		if !m.animating || c.Tick == nil {
			return m, nil, actionNone{}
		}
		return m, c.Tick(scrollTick, func(time.Time) tea.Msg { return drawerTickMsg{} }), actionNone{}
	case drawerLoadedMsg:
		return m.loadedReviews(msg), nil, actionNone{}
	case tea.KeyPressMsg:
		m.flash = ""
		return m.key(msg, c)
	}
	return m, nil, actionNone{}
}

// loadedReviews takes a PR's reviews. The pass on screen stays on screen if
// it is still there, so a new review arriving doesn't move you.
func (m drawerModel) loadedReviews(msg drawerLoadedMsg) drawerModel {
	if msg.PRID != m.prID || msg.Seq < m.applied {
		return m
	}
	if msg.Err != nil {
		// No later load is owed the jump: a reload long after would
		// otherwise move the pass.
		m.wantReview = ""
		return m
	}
	m.applied = msg.Seq
	var shown string
	if r := m.current(); r != nil {
		shown = r.ID
	}
	// On the newest pass you follow the newest review, so a review you
	// just asked for appears. On an older pass you stay on that review.
	following := m.pass == 0 || !m.loaded
	m.reviews, m.posted, m.loaded = msg.Reviews, msg.Posted, true
	if following {
		m.pass = 0
	} else {
		m.pass = max(slices.IndexFunc(m.reviews, func(r store.Review) bool { return r.ID == shown }), 0)
	}
	if r := m.current(); r == nil || r.ID != shown {
		m.sel, m.scroll = 0, 0
	}
	if m.wantReview != "" {
		m = m.selectWanted(true)
	}
	return m
}

// current is the review on screen, or nil.
func (m drawerModel) current() *store.Review {
	if m.pass < 0 || m.pass >= len(m.reviews) {
		return nil
	}
	return &m.reviews[m.pass]
}

func (m drawerModel) key(msg tea.KeyPressMsg, c drawerCtx) (drawerModel, tea.Cmd, action) {
	_, anchors := m.layout(c)
	var fs []store.Finding
	if r := m.current(); r != nil {
		fs = orderedFindings(r.Findings)
	}
	switch k := msg.String(); k {
	case "j", "down":
		m.scroll++
	case "k", "up":
		m.scroll--
	case "n", "p":
		if len(fs) > 0 {
			m.sel = min(max(m.sel+map[string]int{"n": 1, "p": -1}[k], 0), len(fs)-1)
			m.scroll = anchors[m.sel]
		}
	case "<", ">":
		next := m.pass + map[string]int{"<": 1, ">": -1}[k]
		if next >= 0 && next < len(m.reviews) {
			m.pass, m.sel, m.scroll = next, 0, 0
		}
	case "u":
		m.understanding = !m.understanding
	case "a":
		m.assumptions = !m.assumptions
	case "o":
		return m, nil, actionOpen{URL: c.PR.PR.URL}
	case "O":
		if len(fs) > 0 && m.sel < len(fs) {
			return m, nil, actionOpen{URL: findingURL(c.PR.PR.URL, fs[m.sel])}
		}
	case "y":
		if len(fs) > 0 && m.sel < len(fs) {
			m.flash = "copied the finding"
			return m, nil, actionCopy{Text: cleanBlock(fs[m.sel].Body)}
		}
	case "space":
		return m, nil, actionReview{ID: m.prID}
	case "c":
		return m, nil, actionPickModel{ID: m.prID}
	case "P":
		switch r := m.current(); {
		case r == nil:
		case r.Outcome == "complete" || r.Outcome == "incomplete":
			return m, nil, actionPost{ReviewID: r.ID}
		default:
			m.flash = "a failed review can't be posted"
		}
	}
	m.scroll = min(max(m.scroll, 0), m.maxScroll(c))
	return m, nil, actionNone{}
}

// maxScroll is the largest scroll that still fills the drawer.
func (m drawerModel) maxScroll(c drawerCtx) int {
	lines, _ := m.layout(c)
	return max(len(lines)-m.bodyHeight(c.Height), 0)
}

// bodyHeight is the room for scrolling content: all but the bottom line.
func (m drawerModel) bodyHeight(height int) int { return max(height-1, 0) }

// findingURL is the PR's Files tab, anchored on the finding's file and line
// the way GitHub does it: the SHA-256 of the path, then R and the line on
// the new side of the diff.
func findingURL(prURL string, f store.Finding) string {
	if prURL == "" {
		return ""
	}
	url := strings.TrimRight(prURL, "/") + "/files"
	if f.Path == "" {
		return url
	}
	sum := sha256.Sum256([]byte(f.Path))
	url += "#diff-" + hex.EncodeToString(sum[:])
	if f.Line > 0 && !f.Unanchored {
		url += "R" + strconv.Itoa(f.Line)
	}
	return url
}

// findingRank orders findings: blocking first, then the prefixes the
// reviewer uses for the rest.
func findingRank(f store.Finding) int {
	if f.Blocking {
		return 0
	}
	body := strings.ToLower(strings.TrimSpace(f.Body))
	for i, p := range []string{"nit:", "non-blocking:", "question:"} {
		if strings.HasPrefix(body, p) {
			return i + 1
		}
	}
	return 4
}

// orderedFindings is fs in the order the drawer lists them, keeping the
// reviewer's order within each group.
func orderedFindings(fs []store.Finding) []store.Finding {
	out := slices.Clone(fs)
	slices.SortStableFunc(out, func(a, b store.Finding) int { return findingRank(a) - findingRank(b) })
	return out
}

// ---- rendering ----

// layoutWidth is the width content is laid out at: the settled width while
// the drawer slides, so text doesn't rewrap on every frame. The frame cuts
// it off at the drawer's edge.
func (m drawerModel) layoutWidth(width int) int { return max(width, int(m.target)) }

// innerWidth is the room for content inside the border, its space and a
// right margin.
func innerWidth(layoutW int) int { return max(layoutW-3, 10) }

var verdictBanners = map[string]struct {
	label string
	color int
}{
	"approve":         {"APPROVE", 41},
	"request_changes": {"REQUEST CHANGES", 203},
	"comment":         {"COMMENT", 39},
}

// verdictLabel is a verdict's text as the drawer's banner shows it, so the
// Reviews view reads the same. An unknown verdict is upper-cased as it came.
func verdictLabel(verdict string) string {
	if b, ok := verdictBanners[verdict]; ok {
		return b.label
	}
	return strings.ToUpper(cleanLine(verdict))
}

var ledgerLabels = []struct{ ledger, label string }{
	{"addressed", "fixed"}, {"outstanding", "still open"}, {"answered", "answered"}, {"new", "new"},
}

// ledgerSummary is "2 fixed · 1 still open", or "" when no finding has a
// ledger state.
func ledgerSummary(fs []store.Finding) string {
	var parts []string
	for _, l := range ledgerLabels {
		n := 0
		for _, f := range fs {
			if f.Ledger == l.ledger {
				n++
			}
		}
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, l.label))
		}
	}
	return strings.Join(parts, " · ")
}

// findingLocation is "path:line", "path:start-line", or "path · unanchored".
func findingLocation(f store.Finding) string {
	path := cleanLine(f.Path)
	switch {
	case f.Unanchored && path == "":
		return "unanchored"
	case f.Unanchored:
		return path + " · unanchored"
	case f.Line > 0 && f.StartLine > 0 && f.StartLine < f.Line:
		return fmt.Sprintf("%s:%d-%d", path, f.StartLine, f.Line)
	case f.Line > 0:
		return fmt.Sprintf("%s:%d", path, f.Line)
	case path == "":
		return "unanchored"
	}
	return path
}

// layout lays the whole drawer out as lines, and returns the line each
// finding (in orderedFindings order) starts on.
func (m drawerModel) layout(c drawerCtx) (lines []string, anchors []int) {
	th := c.Theme
	inner := innerWidth(m.layoutWidth(m.width()))
	add := func(ls ...string) { lines = append(lines, ls...) }
	wrap := func(s string) []string { return strings.Split(ansi.Wrap(s, inner, ""), "\n") }

	for _, l := range wrap(cleanLine(c.PR.PR.Title)) {
		add(th.Accent().Bold(true).Render(l))
	}
	chip := th.ChipLine(string(c.PR.Status.Status))
	add(ansi.Truncate(chip+th.Muted().Render("  "+cleanLine(c.PR.PR.Repo)+"#"+strconv.Itoa(c.PR.PR.Number)), inner, "…"))

	r := m.current()
	switch {
	case !m.loaded:
		add("", th.Muted().Render("Loading reviews…"))
		return lines, nil
	case r == nil:
		add("", th.Muted().Render("No reviews yet. Press space to review."))
		return lines, nil
	}

	meta := []string{cleanLine(r.Tier), cleanLine(r.Model), cleanLine(r.Effort), costText(*r)}
	if !r.StartedAt.IsZero() {
		meta = append(meta, ago(c.Now.Sub(r.StartedAt))+" ago")
	}
	if len(m.reviews) > 1 {
		meta = append(meta, fmt.Sprintf("pass %d/%d", len(m.reviews)-m.pass, len(m.reviews)))
	}
	meta = slices.DeleteFunc(meta, func(s string) bool { return s == "" })
	for _, l := range wrap(strings.Join(meta, " · ")) {
		add(th.Muted().Render(l))
	}
	add("")

	banner := ""
	if b, ok := verdictBanners[r.Verdict]; ok {
		banner = th.Banner(b.color, b.label)
	} else if r.Verdict != "" {
		banner = th.Muted().Render(verdictLabel(r.Verdict))
	}
	switch r.Outcome {
	case "incomplete":
		banner += "  " + th.StatusFg("CI_WAITING").Render("incomplete")
	case "failed":
		banner += "  " + th.StatusFg("CI_FAILED").Render("failed")
	}
	if strings.TrimSpace(banner) != "" {
		add(banner)
	}
	if p, ok := m.posted[r.ID]; ok {
		add(th.Accent().Render(ansi.Truncate("Posted as "+eventLabel(p.Event)+" · "+postedAgo(c.Now.Sub(p.At)), inner, "…")))
	}
	if reason := cleanLine(r.VerdictReason); reason != "" {
		add(wrap(reason)...)
	}
	if r.Outcome == "failed" && r.Error != "" {
		for _, l := range wrap(cleanLine(r.Error)) {
			add(th.StatusFg("CI_FAILED").Render(l))
		}
	}
	if s := ledgerSummary(r.Findings); s != "" {
		add(th.Muted().Render(s))
	}
	add("")

	fs := orderedFindings(r.Findings)
	if len(fs) == 0 {
		add(th.Muted().Render("No findings."), "")
	}
	for i, f := range fs {
		anchors = append(anchors, len(lines))
		add(m.findingHeader(c, f, i == m.sel, inner)...)
		for _, l := range m.md.render(cleanBlock(f.Body), inner-2, th) {
			add("  " + l)
		}
		add("")
	}

	lines = append(lines, m.section(th, "Understanding", m.understanding, "u", inner, func() []string {
		return m.md.render(cleanBlock(r.Understanding), inner-2, th)
	})...)
	lines = append(lines, m.section(th, "Assumptions", m.assumptions, "a", inner, func() []string {
		var out []string
		for _, a := range r.Assumptions {
			for i, l := range strings.Split(ansi.Wrap(cleanLine(a.Claim), max(inner-4, 1), ""), "\n") {
				out = append(out, map[bool]string{true: "• ", false: "  "}[i == 0]+l)
			}
			if ev := cleanLine(a.Evidence); ev != "" {
				for _, l := range strings.Split(ansi.Wrap(ev, max(inner-4, 1), ""), "\n") {
					out = append(out, th.Muted().Render("  "+l))
				}
			}
		}
		if len(out) == 0 {
			out = []string{th.Muted().Render("None.")}
		}
		return out
	})...)
	if note := cleanBlock(r.Note); strings.TrimSpace(note) != "" {
		add(th.Accent().Render("Check notes"))
		// One note per line, as the engine writes them; only the spacing
		// inside each is tidied.
		for _, n := range strings.Split(note, "\n") {
			if n = strings.Join(strings.Fields(n), " "); n == "" {
				continue
			}
			for _, l := range wrap(n) {
				add(th.Muted().Render(l))
			}
		}
	}
	return lines, anchors
}

func (m drawerModel) section(th theme.Theme, title string, expanded bool, key string, inner int, body func() []string) []string {
	if !expanded {
		return []string{th.Accent().Render("▸ "+title) + th.Muted().Render("  "+key), ""}
	}
	out := []string{th.Accent().Render("▾ "+title) + th.Muted().Render("  "+key)}
	for _, l := range body() {
		out = append(out, "  "+l)
	}
	return append(out, "")
}

// findingHeader is a finding's location line, and its tags (blocking, the
// category, where the ledger has it) after it, on a line of their own when
// they don't fit.
func (m drawerModel) findingHeader(c drawerCtx, f store.Finding, selected bool, inner int) []string {
	th := c.Theme
	mark := "  "
	if selected {
		mark = th.Accent().Render("▸ ")
	}
	loc := mark + th.Accent().Render(findingLocation(f))
	var tags []string
	if f.Blocking {
		tags = append(tags, th.StatusFg("CI_FAILED").Render("blocking"))
	}
	if cat := cleanLine(f.Category); cat != "" {
		tags = append(tags, th.Muted().Render(cat))
	}
	for _, l := range ledgerLabels[:3] {
		if f.Ledger == l.ledger {
			tags = append(tags, th.Muted().Render("["+l.label+"]"))
		}
	}
	if len(tags) == 0 {
		return []string{ansi.Truncate(loc, inner, "…")}
	}
	joined := strings.Join(tags, "  ")
	if ansi.StringWidth(loc)+2+ansi.StringWidth(joined) <= inner {
		return []string{loc + "  " + joined}
	}
	return []string{ansi.Truncate(loc, inner, "…"), ansi.Truncate("  "+joined, inner, "…")}
}

// view draws the drawer width cells wide and height lines high: a border on
// its left edge, then the content scrolled to m.scroll, and a bottom line
// that is the live ticker while a review runs.
func (m drawerModel) view(width, height int, c drawerCtx) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	th := c.Theme
	c.Height = height
	lines, _ := m.layout(c)
	bodyH := m.bodyHeight(height)
	scroll := min(max(m.scroll, 0), max(len(lines)-bodyH, 0))
	lines = lines[min(scroll, len(lines)):]
	lines = fit(strings.Join(lines, "\n"), 1<<20, bodyH)
	lines = append(lines, m.bottomLine(c, innerWidth(m.layoutWidth(width))))

	edge := th.Muted()
	if c.Focused {
		edge = th.Accent()
	}
	bar := edge.Render("│")
	out := make([]string, len(lines))
	for i, l := range lines {
		l = bar + " " + ansi.Truncate(l, max(width-2, 0), "")
		out[i] = l + strings.Repeat(" ", max(width-ansi.StringWidth(l), 0))
	}
	return strings.Join(out, "\n")
}

func (m drawerModel) bottomLine(c drawerCtx, inner int) string {
	th := c.Theme
	switch {
	case c.Running:
		frame := reviewSpinner.Frames[c.Frame%len(reviewSpinner.Frames)]
		text := cleanLine(c.Ticker)
		if text == "" {
			text = "Claude reviewing…"
		}
		return th.Accent().Render(strings.TrimSpace(frame)) + " " + text
	case m.flash != "":
		return th.Accent().Render(m.flash)
	}
	return th.Muted().Render(ansi.Truncate("j/k scroll · n/p finding · </> pass · y copy · O open line · P post", inner, "…"))
}

// dollars is an amount of money the way every view writes it.
func dollars(usd float64) string { return fmt.Sprintf("$%.2f", usd) }

// costAmount is a cost the way every view writes it, from the known total
// and how many runs did and didn't report a cost: "$X.XX", "$X.XX+?" when
// some runs reported none, or "?" when none did.
func costAmount(usd float64, known, unknown int) string {
	switch {
	case unknown == 0:
		return dollars(usd)
	case known > 0:
		return dollars(usd) + "+?"
	default:
		return "?"
	}
}

// reviewRuns is how many of r's runs did and didn't report a cost. A
// review with no ledger rows falls back on its own flag and total.
func reviewRuns(r store.Review) (known, unknown int) {
	known, unknown = r.CostRuns.Known, r.CostRuns.Unknown
	if known+unknown == 0 && r.CostUnknown {
		unknown = 1
		if r.CostUSD > 0 {
			known = 1
		}
	}
	return known, unknown
}

// reviewCost is a review's costAmount.
func reviewCost(r store.Review) string {
	known, unknown := reviewRuns(r)
	return costAmount(r.CostUSD, known, unknown)
}

// costText is the drawer's review cost: the shared amount, saying in words
// when a run's cost was never reported.
func costText(r store.Review) string {
	known, unknown := reviewRuns(r)
	amount := costAmount(r.CostUSD, known, unknown)
	switch {
	case unknown == 0:
		return amount
	case known > 0:
		return amount + " (some cost unknown)"
	default:
		return amount + " (cost unknown)"
	}
}

// cleanLine is clean for text that belongs on one line.
func cleanLine(s string) string { return strings.TrimSpace(clean(s)) }

// cleanBlock is clean for text with line breaks, like a markdown body: it
// strips the same escape sequences and control characters, but keeps the
// newlines markdown needs.
func cleanBlock(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = clean(l)
	}
	return strings.Join(lines, "\n")
}
