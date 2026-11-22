package tui

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/chart"
	"github.com/chrisdaly-3/jawnsnow/internal/costs"
	"github.com/chrisdaly-3/jawnsnow/internal/store"
)

const (
	// The Cost view's panes, in tab order.
	paneCostFilters = 0
	paneCostChart   = 1
	paneCostTop     = 2
	costPanes       = 3

	// costHourMaxDays is the longest range hourly buckets are offered for;
	// past it there are more buckets than the chart has columns.
	costHourMaxDays = 7

	subscriptionLabel = "≈ API-equivalent cost (subscription login)"
	customRangeLayout = "2006-01-02"
	invalidRangeNote  = "invalid range: want YYYY-MM-DD..YYYY-MM-DD, start on or before end"
	// customRangeMaxDays bounds a custom range: Aggregate lays out every
	// bucket inside Update.
	customRangeMaxDays = 731
	longRangeNote      = "range can be at most 2 years"
)

// seriesPalette colors series i (D11).
var seriesPalette = [...]int{39, 141, 214, 41, 203, 153}

type costPreset struct {
	label string
	days  int // 0 is the custom range
}

var costPresets = []costPreset{{"7d", 7}, {"30d", 30}, {"90d", 90}, {"custom", 0}}

const (
	defaultCostPreset = 1
	customCostPreset  = 3
)

var costGroups = []costs.GroupBy{costs.ByNone, costs.ByModel, costs.ByAuthor, costs.ByRepo, costs.ByTier}

type (
	// costLoadedMsg carries the ledger rows of [From, To) and the "repo#n"
	// labels of their most expensive PRs.
	costLoadedMsg struct {
		Seq      int // which load request this answers; see costModel.issued
		From, To time.Time
		Rows     []store.CostRow
		Labels   map[string]string
		Err      error
	}
	// costLabelsMsg carries labels for top PRs a filter change brought in.
	costLabelsMsg struct{ Labels map[string]string }
)

// reviewsFilter is a PR filter waiting for the Reviews view to apply it
// (D15): enter on a top PR sets it and switches views.
type reviewsFilter struct{ PRID, Label string }

// costModel is the Cost view. rows are the loaded range's ledger rows;
// everything else is derived from them in memory.
type costModel struct {
	preset   int
	from, to time.Time
	// loaded is set once rows for [from, to) arrive.
	loaded bool
	// issued counts load requests; applied is the newest one whose answer
	// was taken, so a slow older read can't replace a newer one.
	issued, applied int
	rows            []store.CostRow
	err             string
	// labels maps PR IDs to "repo#n". A PR the store has no review for maps
	// to "", so it isn't asked about again.
	labels map[string]string

	filters    [costDimCount][]string
	group      costs.GroupBy
	bucket     costs.Bucket
	cumulative bool
	cursor     int // the chart bucket the readout shows
	top        int // the selected top PR

	series []costs.Series
	sum    costs.Summary

	typing   bool
	input    textinput.Model
	inputErr string

	picking          bool
	pickDim, pickCur int
}

func newCost() costModel {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "YYYY-MM-DD..YYYY-MM-DD"
	return costModel{preset: defaultCostPreset, bucket: costs.Day, input: in}
}

// capturing reports whether the range input or the filter picker is open.
// While it is, every key belongs to the Cost view.
func (c costModel) capturing() bool { return c.typing || c.picking }

func localDay(t time.Time) time.Time {
	y, mo, d := t.In(time.Local).Date()
	return time.Date(y, mo, d, 0, 0, 0, 0, time.Local)
}

// presetRange is the last n local days, today included.
func presetRange(now time.Time, days int) (from, to time.Time) {
	day := localDay(now)
	return day.AddDate(0, 0, 1-days), day.AddDate(0, 0, 1)
}

// parseRange reads "YYYY-MM-DD..YYYY-MM-DD" in local time, both days
// included (D12).
func parseRange(s string) (from, to time.Time, err error) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), "..")
	if !ok {
		return from, to, errors.New(invalidRangeNote)
	}
	from, errA := time.ParseInLocation(customRangeLayout, strings.TrimSpace(a), time.Local)
	last, errB := time.ParseInLocation(customRangeLayout, strings.TrimSpace(b), time.Local)
	if errA != nil || errB != nil || last.Before(from) {
		return time.Time{}, time.Time{}, errors.New(invalidRangeNote)
	}
	to = last.AddDate(0, 0, 1)
	if (costModel{from: from, to: to}).days() > customRangeMaxDays {
		return time.Time{}, time.Time{}, errors.New(longRangeNote)
	}
	return from, to, nil
}

// days is the range's length in calendar days.
func (c costModel) days() int { return int(math.Round(c.to.Sub(c.from).Hours() / 24)) }

func (c costModel) rangeLabel() string {
	if c.preset != customCostPreset {
		return costPresets[c.preset].label
	}
	return c.from.Format(customRangeLayout) + ".." + c.to.AddDate(0, 0, -1).Format(customRangeLayout)
}

func (c costModel) query() costs.Query {
	f := c.filters
	return costs.Query{
		Filter: store.CostFilter{
			From: c.from, To: c.to, Authors: f[dimAuthor], Repos: f[dimRepo], Models: f[dimModel],
			Efforts: f[dimEffort], Tiers: f[dimTier], Modes: f[dimMode], Outcomes: f[dimOutcome],
		},
		Bucket: c.bucket, GroupBy: c.group, Cumulative: c.cumulative,
	}
}

func bucketCount(series []costs.Series) int {
	if len(series) == 0 {
		return 0
	}
	return len(series[0].Points)
}

// recompute derives the series and summary from the rows. A change in the
// number of buckets puts the cursor back on the newest one.
func (c costModel) recompute() costModel {
	before := bucketCount(c.series)
	c.series, c.sum = costs.Aggregate(c.rows, c.query())
	n := bucketCount(c.series)
	if n != before {
		c.cursor = n - 1
	}
	c.cursor = min(max(c.cursor, 0), max(n-1, 0))
	c.top = min(max(c.top, 0), max(len(c.sum.Top)-1, 0))
	return c
}

// setRange selects [from, to) and loads its rows. Hourly buckets give way
// to daily ones over a range too long for them.
func (m Model) setRange(from, to time.Time) (Model, tea.Cmd) {
	c := m.cost
	if !from.Equal(c.from) || !to.Equal(c.to) {
		c.from, c.to, c.loaded, c.rows, c.err = from, to, false, nil, ""
	}
	if c.bucket == costs.Hour && c.days() > costHourMaxDays {
		c.bucket = costs.Day
	}
	m.cost = c.recompute()
	return m.loadCost()
}

// enterCost loads the selected range again, so new spend shows. A preset
// range moves with the clock.
func (m Model) enterCost() (Model, tea.Cmd) {
	from, to := m.cost.from, m.cost.to
	if m.cost.preset != customCostPreset {
		from, to = presetRange(m.now(), costPresets[m.cost.preset].days)
	}
	return m.setRange(from, to)
}

// loadCost reads the range's rows in a command, with the labels of the
// PRs that top it under the current filters.
func (m Model) loadCost() (Model, tea.Cmd) {
	st, from, to, q := m.store, m.cost.from, m.cost.to, m.cost.query()
	m.cost.issued++
	seq := m.cost.issued
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), inputsTimeout)
		defer cancel()
		rows, err := st.CostRows(ctx, store.CostFilter{From: from, To: to})
		if err != nil {
			return costLoadedMsg{Seq: seq, From: from, To: to, Err: err}
		}
		_, sum := costs.Aggregate(rows, q)
		return costLoadedMsg{Seq: seq, From: from, To: to, Rows: rows, Labels: topLabels(ctx, st, sum.Top)}
	}
}

// topLabels looks each PR up by its newest review: the ledger has only the
// PR's node ID.
func topLabels(ctx context.Context, st *store.Store, top []costs.TopPR) map[string]string {
	out := make(map[string]string, len(top))
	for _, p := range top {
		out[p.PRID] = ""
		if rs, err := st.Reviews(ctx, p.PRID); err == nil && len(rs) > 0 && rs[0].Number > 0 {
			out[p.PRID] = fmt.Sprintf("%s#%d", rs[0].Repo, rs[0].Number)
		}
	}
	return out
}

// resolveLabels asks for the labels of top PRs not looked up yet.
func (m Model) resolveLabels() tea.Cmd {
	var missing []costs.TopPR
	for _, p := range m.cost.sum.Top {
		if _, ok := m.cost.labels[p.PRID]; !ok {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	st := m.store
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), inputsTimeout)
		defer cancel()
		return costLabelsMsg{Labels: topLabels(ctx, st, missing)}
	}
}

func (c costModel) withLabels(add map[string]string) costModel {
	labels := make(map[string]string, len(c.labels)+len(add))
	maps.Copy(labels, c.labels)
	maps.Copy(labels, add)
	c.labels = labels
	return c
}

// updateCost handles the Cost view's messages. A load for a range no longer
// selected is dropped.
func (m Model) updateCost(msg tea.Msg) (_ Model, _ tea.Cmd, ok bool) {
	switch msg := msg.(type) {
	case costLoadedMsg:
		c := m.cost
		if !msg.From.Equal(c.from) || !msg.To.Equal(c.to) || msg.Seq < c.applied {
			return m, nil, true
		}
		c.applied = msg.Seq
		c.loaded, c.rows, c.err = true, msg.Rows, ""
		if msg.Err != nil {
			c.rows, c.err = nil, "could not read costs: "+msg.Err.Error()
		}
		m.cost = c.withLabels(msg.Labels).recompute()
		return m, m.resolveLabels(), true
	case costLabelsMsg:
		m.cost = m.cost.withLabels(msg.Labels)
		return m, nil, true
	case costStateLoadedMsg:
		m, cmd := m.restoreCostState(msg)
		return m, cmd, true
	case costStateSavedMsg:
		m, cmd := m.costStateSaved(msg)
		return m, cmd, true
	case tea.PasteMsg:
		if m.view == ViewCost && m.cost.typing {
			m.cost.input, _ = m.cost.input.Update(msg)
			return m, nil, true
		}
	}
	return m, nil, false
}

// costKey handles a key in the Cost view.
func (m Model) costKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch {
	case m.cost.typing:
		return m.costRangeKey(msg)
	case m.cost.picking:
		return m.costPickerKey(msg)
	}
	c, requery := m.cost, false
	switch msg.String() {
	case "r":
		next := (c.preset + 1) % len(costPresets)
		if next == customCostPreset {
			// The range stays until a valid one is entered.
			c.preset, c.typing, c.inputErr = next, true, ""
			c.input.Reset()
			c.input.Focus()
			m.cost = c
			return m, nil
		}
		m.cost.preset = next
		return m.setRange(presetRange(m.now(), costPresets[next].days))
	case "f":
		c.picking, c.pickDim, c.pickCur = true, 0, 0
	case "g":
		i := 0
		for j, g := range costGroups {
			if g == c.group {
				i = j
			}
		}
		c.group, requery = costGroups[(i+1)%len(costGroups)], true
	case "b":
		c.bucket, requery = c.nextBucket(), true
	case "t":
		c.cumulative, requery = !c.cumulative, true
	case "h", "left":
		c.cursor = max(c.cursor-1, 0)
	case "l", "right":
		c.cursor = min(c.cursor+1, max(bucketCount(c.series)-1, 0))
	case "j", "down":
		c.top = min(c.top+1, max(len(c.sum.Top)-1, 0))
	case "k", "up":
		c.top = max(c.top-1, 0)
	case "enter":
		if c.top < len(c.sum.Top) {
			p := c.sum.Top[c.top]
			m.pendingReviewsFilter = reviewsFilter{PRID: p.PRID, Label: c.topLabel(p)}
			return m.showView(ViewReviews)
		}
	}
	// Only a change to the query re-aggregates the rows; the other keys
	// move a cursor that is already clamped.
	if requery {
		c = c.recompute()
	}
	m.cost = c
	return m, m.resolveLabels()
}

// nextBucket cycles day → week → hour, offering hour only over a short
// range.
func (c costModel) nextBucket() costs.Bucket {
	switch c.bucket {
	case costs.Day:
		return costs.Week
	case costs.Week:
		if c.days() <= costHourMaxDays {
			return costs.Hour
		}
	}
	return costs.Day
}

// costRangeKey handles a key while the custom range input is open.
func (m Model) costRangeKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	c := m.cost
	switch msg.String() {
	case "esc":
		c.typing, c.inputErr = false, ""
		c.input.Blur()
	case "enter":
		from, to, err := parseRange(c.input.Value())
		if err != nil {
			c.inputErr = err.Error()
			break
		}
		c.typing, c.inputErr = false, ""
		c.input.Blur()
		m.cost = c
		return m.setRange(from, to)
	default:
		c.input, _ = c.input.Update(msg)
		c.inputErr = ""
	}
	m.cost = c
	return m, nil
}

// topLabel is the PR's "repo#n", or its repo while the number is unknown.
func (c costModel) topLabel(p costs.TopPR) string {
	if l := c.labels[p.PRID]; l != "" {
		return clean(l)
	}
	return clean(p.Repo) + "#?"
}

// ---- rendering ----

func paletteStyle(i int, noColor bool) lipgloss.Style {
	if noColor {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(strconv.Itoa(seriesPalette[i%len(seriesPalette)])))
}

// usdLabel is a dollar amount in cents, or to three significant digits
// below a cent, where axis labels for tiny spend land.
func usdLabel(v float64) string {
	if v != 0 && math.Abs(v) < 0.01 {
		return "$" + strconv.FormatFloat(v, 'g', 3, 64)
	}
	return "$" + money(v)
}

// bucketLabel names a bucket: short for the x axis, long for the readout.
func bucketLabel(t time.Time, b costs.Bucket, long bool) string {
	switch {
	case b == costs.Hour && long:
		return t.Format("Jan 2 15:04")
	case b == costs.Hour:
		return t.Format("15:04")
	case b == costs.Week && long:
		return "week of " + t.Format("Jan 2")
	}
	return t.Format("Jan 2")
}

// xLabels picks a few evenly spaced bucket labels across width cells.
func xLabels(points []costs.Point, b costs.Bucket, width int) []string {
	n := len(points)
	if n == 0 {
		return nil
	}
	k := min(n, max(width/14, 2), 8)
	if k == 1 {
		return []string{bucketLabel(points[0].Start, b, false)}
	}
	out := make([]string, k)
	for i := range k {
		out[i] = bucketLabel(points[int(math.Round(float64(i)*float64(n-1)/float64(k-1)))].Start, b, false)
	}
	return out
}

func (m Model) costBody(width, height int) string {
	th, c := m.theme, m.cost
	header := th.Accent().Render("Cost · " + c.rangeLabel())
	if m.claude.AuthMethod == "claude.ai" {
		header += "  " + th.Muted().Render(subscriptionLabel)
	}
	lines := []string{header, m.paneTitleLine(paneCostFilters, "Filters") + "  " + m.costFilterBar(), ""}

	rest := max(height-len(lines), 0)
	var body []string
	switch {
	case c.picking:
		body = m.costPickerView(width, rest)
	case !c.loaded:
		body = fit(placeholder(th, width, rest, "Loading…"), width, rest)
	case c.err != "":
		body = fit(placeholder(th, width, rest, clean(c.err)), width, rest)
	case c.sum.Reviews == 0 && len(c.rows) == 0:
		body = fit(placeholder(th, width, rest, "No review spend in this range"), width, rest)
	case c.sum.Reviews == 0:
		body = fit(placeholder(th, width, rest, "No review spend matches these filters"), width, rest)
	default:
		body = m.costChartAndSummary(width, rest)
	}
	return strings.Join(fit(strings.Join(append(lines, body...), "\n"), width, height), "\n")
}

func (m Model) costFilterBar() string {
	th, c := m.theme, m.cost
	if c.typing {
		s := "range " + c.input.View()
		if c.inputErr != "" {
			s += "  " + th.Accent().Render(c.inputErr)
		}
		return s
	}
	group := string(c.group)
	if group == "" {
		group = "none"
	}
	mode := "per bucket"
	if c.cumulative {
		mode = "running total"
	}
	parts := []string{"range " + c.rangeLabel(), "group " + group, "bucket " + string(c.bucket), mode, "filter " + c.filterSummary()}
	return strings.Join(parts, th.Muted().Render(" · "))
}

// costChartAndSummary is the chart, its cursor readout, the summary and the
// top PRs, in height lines.
func (m Model) costChartAndSummary(width, height int) []string {
	th, c := m.theme, m.cost
	sum := c.sum
	topLines := m.costTopLines(width)
	// Title, readout, blank, two summary lines, blank, then the top PRs.
	chartH := max(height-6-len(topLines), 5)

	verb := "Spend per "
	if c.cumulative {
		verb = "Running total by "
	}
	lines := []string{m.paneTitleLine(paneCostChart, verb+string(c.bucket))}

	chartLines := make([]chart.Line, len(c.series))
	for i, s := range c.series {
		vals := make([]float64, len(s.Points))
		for j, p := range s.Points {
			vals[j] = p.USD
		}
		chartLines[i] = chart.Line{Name: clean(s.Name), Values: vals, Color: seriesPalette[i%len(seriesPalette)]}
	}
	var points []costs.Point
	if len(c.series) > 0 {
		points = c.series[0].Points
	}
	lines = append(lines, chart.Render(chartLines, chart.Options{
		Width: width, Height: chartH, Cursor: c.cursor, YLabel: usdLabel,
		XLabels: xLabels(points, c.bucket, width-8), NoColor: th.NoColor(),
	})...)

	if c.cursor < len(points) {
		vals := make([]string, len(c.series))
		for i, s := range c.series {
			vals[i] = paletteStyle(i, th.NoColor()).Render("■ " + clean(s.Name) + " " + usdLabel(s.Points[c.cursor].USD))
		}
		lines = append(lines, bucketLabel(points[c.cursor].Start, c.bucket, true)+" · "+strings.Join(vals, "  "))
	}

	total := "Total " + usdLabel(sum.Total)
	switch {
	case sum.Unknown == 1:
		total += " · 1 run cost unknown"
	case sum.Unknown > 1:
		total += fmt.Sprintf(" · %d runs cost unknown", sum.Unknown)
	}
	reviews := "reviews"
	if sum.Reviews == 1 {
		reviews = "review"
	}
	lines = append(lines, "", total,
		fmt.Sprintf("%d %s · avg %s per review · first-pass %s · follow-up %s",
			sum.Reviews, reviews, usdLabel(sum.AvgPerReview), usdLabel(sum.FirstPass), usdLabel(sum.FollowUp)),
		"")
	return fit(strings.Join(append(lines, topLines...), "\n"), width, height)
}

func (m Model) paneTitleLine(pane int, s string) string {
	if m.focus == pane {
		return m.theme.Accent().Render(s)
	}
	return m.theme.Muted().Render(s)
}

// costTopLines lists the most expensive PRs with their share of the total.
func (m Model) costTopLines(width int) []string {
	th, c := m.theme, m.cost
	lines := []string{m.paneTitleLine(paneCostTop, "Top PRs")}
	labelW := 0
	for _, p := range c.sum.Top {
		labelW = max(labelW, lipgloss.Width(c.topLabel(p)))
	}
	labelW = min(labelW, max(width-24, 8))
	for i, p := range c.sum.Top {
		mark := "  "
		if i == c.top {
			mark = th.Accent().Render("▸ ")
		}
		label := ansi.Truncate(c.topLabel(p), labelW, "…")
		share := ""
		if c.sum.Total > 0 {
			share = fmt.Sprintf("%3.0f%%", p.USD/c.sum.Total*100)
		}
		lines = append(lines, fmt.Sprintf("%s%s%s  %9s  %s", mark, label,
			strings.Repeat(" ", labelW-lipgloss.Width(label)), usdLabel(p.USD), th.Muted().Render(share)))
	}
	return lines
}
