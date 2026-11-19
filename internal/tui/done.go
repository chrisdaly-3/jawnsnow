package tui

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/theme"
	"github.com/chrisdaly-3/jawnsnow/internal/tracker"
)

const (
	paneMerged  = 0
	paneWaiting = 1
	donePanes   = 2

	// The columns a table can't spare: author, status or reason, date or
	// since, and the plan 2 and 3 column. The PR column takes the rest.
	colAuthor = 14
	colStatus = 10
	colWhen   = 8
	colClaude = 34
	cellPad   = 2 // the table's cell style pads each side by one
)

// doneModel is the Done view: merged and closed PRs above the PRs waiting
// for new activity. Like the wall it holds only cursors; the PRs come from
// the root model on every call.
type doneModel struct {
	// cursor is the selected row of each table.
	cursor [donePanes]int

	// focus is the table that has the keys. The root model's tab handling
	// owns it and copies it in before each call.
	focus int

	w, h int
	now  time.Time
	loc  *time.Location // the zone dates are shown in
	// verdicts is each waiting PR's latest Claude verdict, by ID. The root
	// model copies it in before each view.
	verdicts map[string]string

	// stats is each PR's review count and cost, for the merged table's last
	// column; issued and applied order the loads so an old answer can't
	// replace a newer one.
	stats           map[string]store.PRStat
	issued, applied int
}

// doneStatsMsg carries the review stats a load read.
type doneStatsMsg struct {
	Seq   int
	Stats map[string]store.PRStat
	Err   error
}

// loadDoneStats reads every PR's review count and cost in a command.
func (m Model) loadDoneStats() (Model, tea.Cmd) {
	st := m.store
	m.done.issued++
	seq := m.done.issued
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), inputsTimeout)
		defer cancel()
		stats, err := st.PRReviewStats(ctx)
		return doneStatsMsg{Seq: seq, Stats: stats, Err: err}
	}
}

// updateDoneStats takes a stats load. A failed read keeps the last good
// numbers.
func (m Model) updateDoneStats(msg tea.Msg) (_ Model, ok bool) {
	d, isStats := msg.(doneStatsMsg)
	if !isStats {
		return m, false
	}
	if d.Seq >= m.done.applied && d.Err == nil {
		m.done.applied, m.done.stats = d.Seq, d.Stats
	}
	return m, true
}

// reviewCostText is the merged table's review cell: the count, then the
// spend, or a note where a run's cost was never reported. A PR with no
// reviews has a blank cell.
func reviewCostText(st store.PRStat) string {
	if st.Reviews == 0 {
		return ""
	}
	text := strconv.Itoa(st.Reviews) + " reviews"
	if st.Reviews == 1 {
		text = "1 review"
	}
	switch {
	case st.Unknown == 0:
		return text + " · " + dollars(st.CostUSD)
	case st.CostUSD > 0:
		return text + " · " + dollars(st.CostUSD) + " · cost unknown"
	default:
		return text + " · cost unknown"
	}
}

func newDone() doneModel { return doneModel{w: 120, h: 30, loc: time.Local} }

func (m *doneModel) setSize(w, h int) { m.w, m.h = w, h }

// mergedRows is the PRs that left the wall for good, newest first.
func mergedRows(prs []tracker.Tracked) []tracker.Tracked {
	return sortedBy(prs, tracker.PlaceDone, func(t tracker.Tracked) time.Time { return t.DoneAt })
}

// waitingRows is the hidden and stale PRs, newest first.
func waitingRows(prs []tracker.Tracked) []tracker.Tracked {
	return sortedBy(prs, tracker.PlaceWaiting, func(t tracker.Tracked) time.Time { return t.WaitingSince })
}

func sortedBy(prs []tracker.Tracked, place tracker.Place, at func(tracker.Tracked) time.Time) []tracker.Tracked {
	rows := slices.DeleteFunc(slices.Clone(prs), func(t tracker.Tracked) bool { return t.Place != place })
	slices.SortStableFunc(rows, func(a, b tracker.Tracked) int {
		if c := at(b).Compare(at(a)); c != 0 {
			return c
		}
		return strings.Compare(a.PR.ID, b.PR.ID)
	})
	return rows
}

func (m doneModel) rows(pane int, prs []tracker.Tracked) []tracker.Tracked {
	if pane == paneWaiting {
		return waitingRows(prs)
	}
	return mergedRows(prs)
}

// at is the selected row's index in pane, kept inside the table when polls
// shrink it.
func (m doneModel) at(pane, n int) int { return min(max(m.cursor[pane], 0), max(n-1, 0)) }

func (m doneModel) update(msg tea.Msg, prs []tracker.Tracked) (doneModel, tea.Cmd, action) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil, actionNone{}
	}
	pane := m.focus
	rows := m.rows(pane, prs)
	cur := m.at(pane, len(rows))
	switch key.String() {
	case "j", "down":
		m.cursor[pane] = min(cur+1, max(len(rows)-1, 0))
	case "k", "up":
		m.cursor[pane] = max(cur-1, 0)
	case "x":
		// Only the waiting table has anything to bring back, hidden or stale.
		if pane == paneWaiting && len(rows) > 0 {
			return m, nil, actionUnhide{ID: rows[cur].PR.ID}
		}
	case "o":
		if len(rows) > 0 {
			return m, nil, actionOpen{URL: rows[cur].PR.URL}
		}
	case "enter":
		if len(rows) > 0 {
			return m, nil, actionOpenDrawer{ID: rows[cur].PR.ID}
		}
	}
	return m, nil, actionNone{}
}

// ---- rendering ----

func (m doneModel) view(width, height int, prs []tracker.Tracked, th theme.Theme) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	merged, waiting := mergedRows(prs), waitingRows(prs)

	// Two titles and a blank line between the tables; the rest is split,
	// and a table that needs less than half gives the rest to the other.
	avail := max(height-3, 0)
	needM, needW := sectionNeed(len(merged)), sectionNeed(len(waiting))
	hM := min(needM, max((avail+1)/2, avail-needW))
	hW := min(needW, avail-hM)
	hM = min(needM, avail-hW)

	lines := []string{m.title("Merged & closed", len(merged), paneMerged, th)}
	lines = append(lines, m.section(paneMerged, merged, width, hM, th)...)
	lines = append(lines, "", m.title("Waiting for new activity", len(waiting), paneWaiting, th))
	lines = append(lines, m.section(paneWaiting, waiting, width, hW, th)...)
	return strings.Join(fit(strings.Join(lines, "\n"), width, height), "\n")
}

// sectionNeed is the lines a table with n rows wants: a header and its
// rows, or the one empty-state line.
func sectionNeed(n int) int {
	if n == 0 {
		return 1
	}
	return n + 1
}

func (m doneModel) title(name string, n, pane int, th theme.Theme) string {
	text := name + " (" + strconv.Itoa(n) + ")"
	if pane == m.focus {
		return th.Accent().Bold(true).Render("▸ " + text)
	}
	return th.Muted().Render("  " + text)
}

// section renders one table into at most h lines.
func (m doneModel) section(pane int, rows []tracker.Tracked, width, h int, th theme.Theme) []string {
	if len(rows) == 0 {
		return []string{th.Muted().Render("  Nothing here yet")}
	}
	if h < 2 {
		return nil
	}
	vis := h - 1 // the header takes a line
	cur := m.at(pane, len(rows))
	first := min(max(cur-vis+1, 0), max(len(rows)-vis, 0))
	shown := rows[first:min(first+vis, len(rows))]

	cols, cells := m.columns(pane, width), make([]table.Row, len(shown))
	for i, t := range shown {
		cells[i] = m.cells(pane, t, th)
	}

	styles := table.DefaultStyles()
	styles.Header = th.Muted().Padding(0, 1)
	styles.Cell = lipgloss.NewStyle().Padding(0, 1)
	styles.Selected = lipgloss.NewStyle()
	if pane == m.focus {
		styles.Selected = th.Accent().Bold(true)
	}
	tbl := table.New(table.WithColumns(cols), table.WithRows(cells), table.WithStyles(styles),
		table.WithWidth(width), table.WithHeight(h), table.WithFocused(pane == m.focus))
	tbl.SetCursor(cur - first)
	return strings.Split(tbl.View(), "\n")
}

func (m doneModel) columns(pane, width int) []table.Column {
	last, status, when := "Claude reviews / your cost", "Status", "Done"
	if pane == paneWaiting {
		last, status, when = "Latest Claude verdict", "Reason", "Since"
	}
	pr := max(width-cellPad*5-colAuthor-colStatus-colWhen-colClaude, 10)
	return []table.Column{
		{Title: "PR", Width: pr},
		{Title: "Author", Width: colAuthor},
		{Title: status, Width: colStatus},
		{Title: when, Width: colWhen},
		{Title: last, Width: colClaude},
	}
}

// cells is one row.
func (m doneModel) cells(pane int, t tracker.Tracked, th theme.Theme) table.Row {
	pr := clean(t.PR.Repo+"#"+strconv.Itoa(t.PR.Number)) + " " + clean(t.PR.Title)
	author := clean(t.PR.Author)
	if pane == paneWaiting {
		return table.Row{pr, author, string(t.WaitReason), sinceAge(t.WaitingSince, m.now), m.verdicts[t.PR.ID]}
	}
	return table.Row{pr, author, th.ChipLine(string(t.Status.Status)), doneDate(t.DoneAt, m.loc), reviewCostText(m.stats[t.PR.ID])}
}

func doneDate(at time.Time, loc *time.Location) string {
	if at.IsZero() {
		return "–"
	}
	return at.In(loc).Format("Jan 2")
}

// sinceAge is how long ago from was, in the largest whole unit.
func sinceAge(from, now time.Time) string {
	if from.IsZero() {
		return "–"
	}
	d := max(now.Sub(from), 0)
	if d >= 24*time.Hour {
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	}
	return ago(d)
}
