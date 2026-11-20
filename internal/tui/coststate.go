package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/chrisdaly-3/jawnsnow/internal/costs"
)

// costStateKey is the Cost view's row in the store's view_state table.
const costStateKey = "cost"

// costState is what the Cost view remembers across restarts (A8): its
// Filters line. A preset range is kept by name, so it moves with the
// clock; a custom one by its first and last days.
type costState struct {
	Range   string              `json:"range"`
	From    string              `json:"from,omitempty"`
	To      string              `json:"to,omitempty"` // the last day, included
	Filters map[string][]string `json:"filters,omitempty"`
	Group   string              `json:"group,omitempty"`
	Bucket  string              `json:"bucket,omitempty"`
	Total   bool                `json:"total,omitempty"`
}

type (
	costStateLoadedMsg struct{ Data []byte }
	costStateSavedMsg  struct{ Err error }
)

func (c costModel) state() costState {
	s := costState{Range: costPresets[c.preset].label, Group: string(c.group), Bucket: string(c.bucket), Total: c.cumulative}
	if c.preset == customCostPreset {
		if c.from.IsZero() {
			s.Range = costPresets[defaultCostPreset].label
		} else {
			s.From, s.To = c.from.Format(customRangeLayout), c.to.AddDate(0, 0, -1).Format(customRangeLayout)
		}
	}
	for d, vals := range c.filters {
		if len(vals) > 0 {
			if s.Filters == nil {
				s.Filters = map[string][]string{}
			}
			s.Filters[costDimNames[d]] = slices.Clone(vals)
		}
	}
	return s
}

// encodeCostState is the state as saved. Map keys are sorted, so two equal
// states encode the same.
func encodeCostState(s costState) []byte {
	b, _ := json.Marshal(s) // a struct of strings, bools and slices can't fail
	return b
}

// withState applies a saved state. Anything it doesn't recognise keeps its
// default, so a state from another version of snow can't break the view.
func (c costModel) withState(data []byte) costModel {
	var s costState
	if json.Unmarshal(data, &s) != nil {
		return c
	}
	for i, p := range costPresets {
		if p.label != s.Range {
			continue
		}
		if i != customCostPreset {
			c.preset = i
			break
		}
		if from, to, err := parseRange(s.From + ".." + s.To); err == nil {
			c.preset, c.from, c.to = i, from, to
		}
	}
	for d, name := range costDimNames {
		c.filters[d] = slices.Clone(s.Filters[name])
	}
	if g := costs.GroupBy(s.Group); slices.Contains(costGroups, g) {
		c.group = g
	}
	if b := costs.Bucket(s.Bucket); b == costs.Hour || b == costs.Day || b == costs.Week {
		c.bucket = b
	}
	c.cumulative = s.Total
	return c
}

// loadCostState reads the saved state at startup. A failed read leaves the
// defaults, as no saved state does.
func (m Model) loadCostState() tea.Cmd {
	st := m.store
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), inputsTimeout)
		defer cancel()
		data, _, _ := st.ViewState(ctx, costStateKey)
		return costStateLoadedMsg{Data: data}
	}
}

// restoreCostState applies the loaded state, unless the Cost view changed
// before it arrived: then the newer choice stands.
func (m Model) restoreCostState(msg costStateLoadedMsg) (Model, tea.Cmd) {
	if m.costRestored {
		return m, nil
	}
	m.costRestored = true
	if msg.Data == nil {
		return m, nil
	}
	m.cost = m.cost.withState(msg.Data)
	if m.view == ViewCost {
		return m.enterCost()
	}
	return m, nil
}

// costKeySaved handles a Cost view key, and saves the state when the key
// changed it.
func (m Model) costKeySaved(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	before := encodeCostState(m.cost.state())
	m, cmd := m.costKey(msg)
	after := encodeCostState(m.cost.state())
	if bytes.Equal(before, after) {
		return m, cmd
	}
	m.costRestored = true
	m, save := m.saveCostState(after)
	return m, tea.Batch(cmd, save)
}

// saveCostState writes the state. One write runs at a time, so an older
// state can't land after a newer one; a change made meanwhile waits in
// costPending.
func (m Model) saveCostState(data []byte) (Model, tea.Cmd) {
	if m.readOnly {
		return m, nil
	}
	if m.costSaving {
		m.costPending = data
		return m, nil
	}
	m.costSaving, m.costPending = true, nil
	st := m.store
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), inputsTimeout)
		defer cancel()
		return costStateSavedMsg{Err: st.SaveViewState(ctx, costStateKey, data)}
	}
}

func (m Model) costStateSaved(msg costStateSavedMsg) (Model, tea.Cmd) {
	m.costSaving = false
	if msg.Err != nil {
		m.notice = "could not save the Cost view's filters: " + msg.Err.Error()
	}
	if m.costPending != nil {
		return m.saveCostState(m.costPending)
	}
	return m, nil
}

// flushCostState writes a waiting state before quitting, since nothing
// would deliver a later save's result.
func (m Model) flushCostState(timeout time.Duration) {
	if m.readOnly || m.costPending == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = m.store.SaveViewState(ctx, costStateKey, m.costPending) // the screen is going away
}
