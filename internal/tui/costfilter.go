package tui

import (
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/store"
)

// The Cost view's filter dimensions, in the picker's order.
const (
	dimAuthor = iota
	dimRepo
	dimModel
	dimEffort
	dimTier
	dimMode
	dimOutcome
	costDimCount
)

var costDimNames = [costDimCount]string{"author", "repo", "model", "effort", "tier", "mode", "outcome"}

func dimValue(r store.CostRow, dim int) string {
	switch dim {
	case dimAuthor:
		return r.Author
	case dimRepo:
		return r.Repo
	case dimModel:
		return r.Model
	case dimEffort:
		return r.Effort
	case dimTier:
		return r.Tier
	case dimMode:
		return r.Mode
	}
	return r.Outcome
}

// dimValues lists the dimension's values in the loaded rows, sorted. Values
// already selected stay listed, so a filter from another range can be
// cleared.
func (c costModel) dimValues(dim int) []string {
	seen := map[string]bool{}
	for _, r := range c.rows {
		seen[dimValue(r, dim)] = true
	}
	for _, v := range c.filters[dim] {
		seen[v] = true
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

// filterSummary is "any", or each filtered dimension's values.
func (c costModel) filterSummary() string {
	var parts []string
	for d, vals := range c.filters {
		if len(vals) > 0 {
			shown := make([]string, len(vals))
			for i, v := range vals {
				shown[i] = valueName(v)
			}
			parts = append(parts, costDimNames[d]+"="+strings.Join(shown, ","))
		}
	}
	if len(parts) == 0 {
		return "any"
	}
	return strings.Join(parts, " ")
}

func valueName(v string) string {
	if v == "" {
		return "(none)"
	}
	return clean(v)
}

// toggle adds v to the dimension's filter, or takes it out. The slice is
// rebuilt, so earlier copies of the model keep theirs.
func (c costModel) toggle(dim int, v string) costModel {
	vals := slices.Clone(c.filters[dim])
	if i := slices.Index(vals, v); i >= 0 {
		vals = slices.Delete(vals, i, i+1)
	} else {
		vals = append(vals, v)
		slices.Sort(vals)
	}
	c.filters[dim] = vals
	return c
}

// costPickerKey handles a key while the filter picker is open: h/l pick a
// dimension, j/k a value, space toggles it, esc closes.
func (m Model) costPickerKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	c := m.cost
	vals := c.dimValues(c.pickDim)
	switch msg.String() {
	case "h", "left":
		c.pickDim, c.pickCur = (c.pickDim+costDimCount-1)%costDimCount, 0
	case "l", "right":
		c.pickDim, c.pickCur = (c.pickDim+1)%costDimCount, 0
	case "j", "down":
		c.pickCur = min(c.pickCur+1, max(len(vals)-1, 0))
	case "k", "up":
		c.pickCur = max(c.pickCur-1, 0)
	case "space":
		if c.pickCur < len(vals) {
			c = c.toggle(c.pickDim, vals[c.pickCur])
		}
	case "esc", "f":
		c.picking = false
	}
	m.cost = c.recompute()
	return m, m.resolveLabels()
}

func (m Model) costPickerView(width, height int) []string {
	th, c := m.theme, m.cost
	tabs := make([]string, costDimCount)
	for d, name := range costDimNames {
		if n := len(c.filters[d]); n > 0 {
			name += "(" + strconv.Itoa(n) + ")"
		}
		if d == c.pickDim {
			tabs[d] = th.Accent().Render("[" + name + "]")
		} else {
			tabs[d] = th.Muted().Render(" " + name + " ")
		}
	}
	lines := []string{
		strings.Join(tabs, " "),
		th.Muted().Render("h/l dimension · j/k move · space toggle · esc close"),
	}
	vals := c.dimValues(c.pickDim)
	if len(vals) == 0 {
		lines = append(lines, th.Muted().Render("  no values in this range"))
	}
	// Scroll so the cursor stays in the rows left.
	rows := max(height-len(lines), 1)
	start := min(max(c.pickCur-rows+1, 0), max(len(vals)-rows, 0))
	for i := start; i < len(vals) && i < start+rows; i++ {
		v := vals[i]
		mark, box := "  ", "[ ] "
		if i == c.pickCur {
			mark = th.Accent().Render("▸ ")
		}
		if slices.Contains(c.filters[c.pickDim], v) {
			box = "[x] "
		}
		lines = append(lines, ansi.Truncate(mark+box+valueName(v), max(width, 0), "…"))
	}
	return fit(strings.Join(lines, "\n"), width, height)
}
