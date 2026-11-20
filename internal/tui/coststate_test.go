package tui

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/costs"
	"github.com/chrisdaly-3/jawnsnow/internal/theme"
)

// restarted is a new model over h's store, as snow is after a restart,
// with the Cost view's saved state delivered.
func restarted(t *testing.T, h *harness, now time.Time) Model {
	t.Helper()
	th, _ := theme.New(config.ThemeConfig{})
	m := New(Deps{Config: config.Defaults(), Theme: th, Store: h.store, Now: func() time.Time { return now }})
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	msgs, _ := runCmd(t, m.loadCostState())
	for _, msg := range msgs {
		m, _ = update(t, m, msg)
	}
	return m
}

func savedCostState(t *testing.T, h *harness) costState {
	t.Helper()
	data, ok, err := h.store.ViewState(context.Background(), costStateKey)
	if err != nil || !ok {
		t.Fatalf("ViewState: ok %v err %v, want a saved state", ok, err)
	}
	var s costState
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	return s
}

func TestCostViewStateSurvivesARestart(t *testing.T) {
	h := costHarness(t, opts{}, 120, 30, true)
	m, _ := settle(t, h.m, key("4"))
	// 90d, grouped by model, weekly, running total, and one author picked.
	for _, k := range []string{"r", "g", "b", "t", "f", "space", "esc"} {
		m, _ = settle(t, m, key(k))
	}
	want := m.cost.state()
	if want.Range != "90d" || want.Group != string(costs.ByModel) || want.Bucket != string(costs.Week) || !want.Total || len(want.Filters["author"]) != 1 {
		t.Fatalf("setup: state %+v", want)
	}
	if got := savedCostState(t, h); !reflect.DeepEqual(got, want) {
		t.Errorf("saved %+v, want %+v", got, want)
	}

	// A day later, 90d still means the last 90 days.
	later := t0.Add(24 * time.Hour)
	m2 := restarted(t, h, later)
	if got := m2.cost.state(); !reflect.DeepEqual(got, want) {
		t.Errorf("after a restart: %+v, want %+v", got, want)
	}
	m2, _ = settle(t, m2, key("4"))
	if from, _ := presetRange(later, 90); !m2.cost.from.Equal(from) {
		t.Errorf("90d after a restart starts %v, want %v", m2.cost.from, from)
	}
}

func TestCostViewCustomRangeIsKeptByItsDates(t *testing.T) {
	h := costHarness(t, opts{}, 120, 30, true)
	m, _ := settle(t, h.m, key("4"))
	for _, k := range []string{"r", "r"} { // 90d, then custom
		m, _ = settle(t, m, key(k))
	}
	m, _ = settle(t, m, tea.PasteMsg{Content: "2026-11-01..2026-11-15"}, key("enter"))
	if s := savedCostState(t, h); s.Range != "custom" || s.From != "2026-11-01" || s.To != "2026-11-15" {
		t.Fatalf("saved %+v, want the custom range's days", s)
	}
	m2, _ := settle(t, restarted(t, h, t0.AddDate(0, 1, 0)), key("4"))
	if got := m2.cost.rangeLabel(); got != "2026-11-01..2026-11-15" {
		t.Errorf("custom range after a restart: %q", got)
	}
}

func TestCostViewStateIgnoresWhatItDoesntKnow(t *testing.T) {
	def := newCost()
	for _, data := range []string{
		`not json`,
		`{"range":"12d","group":"colour","bucket":"fortnight","filters":{"planet":["mars"]}}`,
		`{"range":"custom","from":"2026-11-15","to":"2026-11-01"}`,
	} {
		if got := def.withState([]byte(data)).state(); !reflect.DeepEqual(got, def.state()) {
			t.Errorf("%s: %+v, want the defaults %+v", data, got, def.state())
		}
	}
}

func TestCostViewChangeBeforeTheSavedStateWins(t *testing.T) {
	h := costHarness(t, opts{noLoad: true}, 120, 30, false)
	if err := h.store.SaveViewState(context.Background(), costStateKey, []byte(`{"range":"7d"}`)); err != nil {
		t.Fatal(err)
	}
	th, _ := theme.New(config.ThemeConfig{})
	m := New(Deps{Config: config.Defaults(), Theme: th, Store: h.store, Now: func() time.Time { return t0 }})
	late, _ := runCmd(t, m.loadCostState())
	m, _ = settle(t, m, key("4"), key("t"))
	m, _ = settle(t, m, late...)
	if s := m.cost.state(); s.Range != "30d" || !s.Total {
		t.Errorf("a saved state that arrived after t: %+v, want 30d with the total kept", s)
	}
}

func TestCostViewSavesOneAtATime(t *testing.T) {
	h := costHarness(t, opts{}, 120, 30, false)
	m, _ := settle(t, h.m, key("4"))
	m, _ = update(t, m, key("t")) // its write is left running
	m, second := update(t, m, key("g"))
	if !m.costSaving || m.costPending == nil {
		t.Fatalf("a change while a save runs: saving %v pending %q, want it waiting", m.costSaving, m.costPending)
	}
	if msgs, _ := runCmd(t, second); len(msgs) != 0 {
		t.Errorf("the waiting change wrote at once: %#v", msgs)
	}
	m, _ = settle(t, m, costStateSavedMsg{Err: errors.New("disk full")})
	if m.notice != "could not save the Cost view's filters: disk full" {
		t.Errorf("notice %q", m.notice)
	}
	if s := savedCostState(t, h); !s.Total || s.Group != string(costs.ByModel) {
		t.Errorf("saved %+v, want the waiting change written after the first save", s)
	}
}

func TestCostViewQuitWritesTheWaitingChange(t *testing.T) {
	h := costHarness(t, opts{}, 120, 30, false)
	m, _ := settle(t, h.m, key("4"))
	m, _ = update(t, m, key("t")) // its write is left running
	m, _ = update(t, m, key("b"))
	m.quit()
	if s := savedCostState(t, h); s.Bucket != string(costs.Week) {
		t.Errorf("saved %+v after quitting, want the waiting weekly bucket", s)
	}
}
