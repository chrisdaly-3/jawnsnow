package tui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/theme"
)

// reviewLogOutcomes is the order o cycles the outcome filter through; ""
// is every outcome.
var reviewLogOutcomes = []string{"", store.OutcomeComplete, store.OutcomeIncomplete, store.OutcomeFailed}

// reviewLogLoadedMsg carries the reviews Filter matched. It is applied only
// while Filter is still the one selected.
type reviewLogLoadedMsg struct {
	Seq     int // which load request this answers; see reviewLogModel.issued
	Filter  store.LogFilter
	Entries []store.LogEntry
	// Posted is the IDs of the reviews that were posted to GitHub.
	Posted map[string]bool
	// Keep is the review ID the cursor stays on, if it is still listed.
	Keep string
	Err  error
}

// reviewLogModel is the Reviews view: every Claude review, newest first.
type reviewLogModel struct {
	entries []store.LogEntry
	posted  map[string]bool
	err     string
	// shown is the filter entries answer; loaded is false until the first
	// answer arrives.
	shown  store.LogFilter
	loaded bool
	// issued counts load requests; applied is the newest one whose answer
	// was taken, so a slow older read can't replace a newer one.
	issued, applied int

	text, outcome string
	// prID filters to one PR; label is the "repo#n" its chip shows.
	prID, label string
	cursor      int

	typing bool
	input  textinput.Model
}

func newReviewLog() reviewLogModel {
	in := textinput.New()
	in.Prompt = "/ "
	in.Placeholder = "repo, number, title or author"
	return reviewLogModel{input: in}
}

// filter is what the store is asked for.
func (r reviewLogModel) filter() store.LogFilter {
	return store.LogFilter{Text: r.text, Outcome: r.outcome, PRID: r.prID}
}

func (r reviewLogModel) filtering() bool { return r.filter() != (store.LogFilter{}) }

// at is the selected row, kept inside the table when a load shrinks it.
func (r reviewLogModel) at() int { return min(max(r.cursor, 0), max(len(r.entries)-1, 0)) }

// enterReviews starts the Reviews view: it applies the PR filter the Cost
// view left for it, then reads the reviews.
func (m Model) enterReviews() (Model, tea.Cmd) {
	if p := m.pendingReviewsFilter; p.PRID != "" {
		m.reviewLog.prID, m.reviewLog.label, m.reviewLog.cursor = p.PRID, clean(p.Label), 0
		m.pendingReviewsFilter = reviewsFilter{}
	}
	return m.loadReviewLog()
}

// loadReviewLog reads the reviews the current filter matches, and which of
// them were posted, in a command.
func (m Model) loadReviewLog() (Model, tea.Cmd) { return m.loadReviewLogKeeping("") }

// loadReviewLogKeeping is loadReviewLog that puts the cursor on the review
// keep when the load still has it.
func (m Model) loadReviewLogKeeping(keep string) (Model, tea.Cmd) {
	st, f := m.store, m.reviewLog.filter()
	m.reviewLog.issued++
	seq := m.reviewLog.issued
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), inputsTimeout)
		defer cancel()
		es, err := st.ReviewLog(ctx, f)
		if err != nil {
			return reviewLogLoadedMsg{Seq: seq, Filter: f, Err: err}
		}
		posted, err := st.PostedReviewIDs(ctx)
		if err != nil {
			return reviewLogLoadedMsg{Seq: seq, Filter: f, Err: err}
		}
		return reviewLogLoadedMsg{Seq: seq, Filter: f, Entries: es, Posted: posted, Keep: keep}
	}
}

// reloadReviewLog re-reads the Reviews view after a review or a post, if it
// is the view on screen, keeping the cursor on the same review.
func (m Model) reloadReviewLog() (Model, tea.Cmd) {
	if m.view != ViewReviews {
		return m, nil
	}
	keep := ""
	if r := m.reviewLog; len(r.entries) > 0 {
		keep = r.entries[r.at()].Review.ID
	}
	return m.loadReviewLogKeeping(keep)
}

// updateReviewLog takes the view's loads and pastes.
func (m Model) updateReviewLog(msg tea.Msg) (_ Model, _ tea.Cmd, ok bool) {
	switch msg := msg.(type) {
	case reviewLogLoadedMsg:
		r := m.reviewLog
		if msg.Filter != r.filter() || msg.Seq < r.applied {
			return m, nil, true
		}
		r.applied = msg.Seq
		r.loaded, r.shown, r.entries, r.posted, r.err = true, msg.Filter, msg.Entries, msg.Posted, ""
		if msg.Err != nil {
			r.entries, r.posted, r.err = nil, nil, "could not read reviews: "+msg.Err.Error()
		}
		if i := slices.IndexFunc(r.entries, func(e store.LogEntry) bool { return msg.Keep != "" && e.Review.ID == msg.Keep }); i >= 0 {
			r.cursor = i
		}
		r.cursor = r.at()
		m.reviewLog = r
		return m, nil, true
	case tea.PasteMsg:
		if m.view == ViewReviews && m.reviewLog.typing {
			m.reviewLog.input, _ = m.reviewLog.input.Update(msg)
			return m, nil, true
		}
	}
	return m, nil, false
}

// reviewLogKey handles a key in the Reviews view's table.
func (m Model) reviewLogKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	r := m.reviewLog
	if r.typing {
		return m.reviewLogInputKey(msg)
	}
	cur := r.at()
	switch msg.String() {
	case "j", "down":
		r.cursor = min(cur+1, max(len(r.entries)-1, 0))
	case "k", "up":
		r.cursor = max(cur-1, 0)
	case "/":
		r.typing = true
		r.input.SetValue(r.text)
		r.input.CursorEnd()
		r.input.Focus()
	case "o":
		i := 0
		for j, o := range reviewLogOutcomes {
			if o == r.outcome {
				i = j
			}
		}
		r.outcome = reviewLogOutcomes[(i+1)%len(reviewLogOutcomes)]
		r.cursor = 0
		m.reviewLog = r
		return m.loadReviewLog()
	case "esc":
		if !r.filtering() {
			return m, nil
		}
		r.text, r.outcome, r.prID, r.label, r.cursor = "", "", "", "", 0
		m.reviewLog = r
		return m.loadReviewLog()
	case "enter":
		if len(r.entries) > 0 {
			e := r.entries[cur].Review
			return m.apply(actionOpenReview{PRID: e.PRID, ReviewID: e.ID})
		}
	}
	m.reviewLog = r
	return m, nil
}

// reviewLogInputKey handles a key in the text filter: enter applies it, esc
// leaves the filter as it was.
func (m Model) reviewLogInputKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	r := m.reviewLog
	switch msg.String() {
	case "esc":
		r.typing = false
		r.input.Blur()
	case "enter":
		r.typing = false
		r.input.Blur()
		r.text, r.cursor = strings.TrimSpace(r.input.Value()), 0
		m.reviewLog = r
		return m.loadReviewLog()
	default:
		r.input, _ = r.input.Update(msg)
	}
	m.reviewLog = r
	return m, nil
}

// ---- rendering ----

// reviewLogCol is one column. Columns with a drop rank go when the table is
// too narrow, lowest rank first; the rest always stay.
type reviewLogCol struct {
	key   string
	title string
	width int
	drop  int
}

var reviewLogCols = []reviewLogCol{
	{"started", "Started", 7, 0},
	{"review", "Review", 0, 0},
	{"model", "Tier · model · effort", 28, 1},
	{"outcome", "Outcome", 10, 0},
	{"verdict", "Verdict", 15, 4},
	{"findings", "Findings", 14, 5},
	{"cost", "Cost", 8, 2},
	{"posted", "Posted", 6, 3},
}

// reviewLogMinTitle is the least room the Review column is left before a
// column is dropped.
const reviewLogMinTitle = 28

// reviewLogColumns is the columns that fit width, the Review column taking
// the slack.
func reviewLogColumns(width int) []reviewLogCol {
	cols := append([]reviewLogCol(nil), reviewLogCols...)
	for {
		used := 0
		for _, c := range cols {
			used += c.width + cellPad
		}
		slack := width - used
		if slack >= reviewLogMinTitle {
			for i := range cols {
				if cols[i].key == "review" {
					cols[i].width = slack
				}
			}
			return cols
		}
		worst := -1
		for i, c := range cols {
			if c.drop > 0 && (worst < 0 || c.drop < cols[worst].drop) {
				worst = i
			}
		}
		if worst < 0 {
			for i := range cols {
				if cols[i].key == "review" {
					cols[i].width = max(slack, 6)
				}
			}
			return cols
		}
		cols = append(cols[:worst], cols[worst+1:]...)
	}
}

func (m Model) reviewLogCells(e store.LogEntry, cols []reviewLogCol, posted bool) table.Row {
	r := e.Review
	row := make(table.Row, len(cols))
	for i, c := range cols {
		switch c.key {
		case "started":
			row[i] = sinceAge(r.StartedAt, m.now())
		case "review":
			row[i] = strings.TrimSpace(clean(r.Repo+"#"+strconv.Itoa(r.Number)) + " " + clean(e.Title))
		case "model":
			var parts []string
			for _, p := range []string{r.Tier, r.Model, r.Effort} {
				if p = strings.TrimSpace(clean(p)); p != "" {
					parts = append(parts, p)
				}
			}
			row[i] = strings.Join(parts, " · ")
		case "outcome":
			row[i] = orDash(clean(r.Outcome))
		case "verdict":
			row[i] = orDash(verdictLabel(r.Verdict))
		case "findings":
			row[i] = "–"
			if e.Findings > 0 {
				row[i] = fmt.Sprintf("%d · %d blocking", e.Findings, e.Blocking)
			}
		case "cost":
			row[i] = reviewCost(r)
		case "posted":
			if posted {
				row[i] = "✓"
			}
		}
	}
	return row
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

// reviewLogChips is the active filters, shown after the title.
func (r reviewLogModel) chips() []string {
	var out []string
	if r.prID != "" {
		out = append(out, "[PR "+r.label+"]")
	}
	if r.outcome != "" {
		out = append(out, "[outcome "+r.outcome+"]")
	}
	if r.text != "" {
		out = append(out, "[/ "+clean(r.text)+"]")
	}
	return out
}

// reviewLogBody renders the Reviews view into width×height.
func (m Model) reviewLogBody(width, height int) string {
	th, r := m.theme, m.reviewLog
	head := th.Accent().Render("Reviews")
	if r.loaded && r.err == "" {
		head += th.Muted().Render(" (" + strconv.Itoa(len(r.entries)) + ")")
	}
	switch {
	case r.typing:
		head += "  " + r.input.View()
	case len(r.chips()) > 0:
		head += "  " + th.Muted().Render(strings.Join(r.chips(), " "))
	}
	lines := []string{head, ""}

	rest := max(height-len(lines), 0)
	var body []string
	switch {
	case r.err != "":
		body = fit(placeholder(th, width, rest, clean(r.err)), width, rest)
	case len(r.entries) == 0 && (!r.loaded || r.shown != r.filter()):
		body = fit(placeholder(th, width, rest, "Loading…"), width, rest)
	case len(r.entries) == 0 && r.filtering():
		body = fit(placeholder(th, width, rest, "No reviews match"), width, rest)
	case len(r.entries) == 0:
		body = fit(placeholder(th, width, rest, "No Claude reviews yet"), width, rest)
	default:
		body = m.reviewLogTable(width, rest, th)
	}
	return strings.Join(fit(strings.Join(append(lines, body...), "\n"), width, height), "\n")
}

func (m Model) reviewLogTable(width, h int, th theme.Theme) []string {
	r := m.reviewLog
	if h < 2 {
		return nil
	}
	vis := h - 1 // the header takes a line
	cur := r.at()
	first := min(max(cur-vis+1, 0), max(len(r.entries)-vis, 0))
	shown := r.entries[first:min(first+vis, len(r.entries))]

	cols := reviewLogColumns(width)
	tcols, rows := make([]table.Column, len(cols)), make([]table.Row, len(shown))
	for i, c := range cols {
		tcols[i] = table.Column{Title: c.title, Width: c.width}
	}
	for i, e := range shown {
		rows[i] = m.reviewLogCells(e, cols, r.posted[e.Review.ID])
	}

	focused := m.focus == 0
	styles := table.DefaultStyles()
	styles.Header = th.Muted().Padding(0, 1)
	styles.Cell = lipgloss.NewStyle().Padding(0, 1)
	styles.Selected = lipgloss.NewStyle()
	if focused {
		styles.Selected = th.Accent().Bold(true)
	}
	tbl := table.New(table.WithColumns(tcols), table.WithRows(rows), table.WithStyles(styles),
		table.WithWidth(width), table.WithHeight(h), table.WithFocused(focused))
	tbl.SetCursor(cur - first)
	return strings.Split(tbl.View(), "\n")
}
