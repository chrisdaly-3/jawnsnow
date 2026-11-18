package github

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
)

type call struct {
	query string
	vars  map[string]any
}

// fakeDoer answers each call from respond and records what it was asked.
type fakeDoer struct {
	calls   []call
	respond func(n int, query string, vars map[string]any) []byte
}

func (f *fakeDoer) DoWithContext(_ context.Context, query string, vars map[string]any, resp any) error {
	n := len(f.calls)
	f.calls = append(f.calls, call{query, vars})
	return json.Unmarshal(f.respond(n, query, vars), resp)
}

func TestSearchIDsPaginatesTo100(t *testing.T) {
	pages := [][]byte{fixture(t, "search_page1.json"), fixture(t, "search_page2.json")}
	d := &fakeDoer{respond: func(n int, _ string, _ map[string]any) []byte { return pages[n] }}
	ids, rl, err := NewClient(d).SearchIDs(context.Background(), "is:pr")
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, len(ids), 53)
	assertEq(t, len(d.calls), 2)
	assertEq(t, d.calls[0].vars["q"].(string), "is:pr")
	if d.calls[0].vars["after"] != nil {
		t.Errorf("first call after = %v, want nil", d.calls[0].vars["after"])
	}
	assertEq(t, d.calls[1].vars["after"].(string), "CUR1")
	assertEq(t, rl.Remaining, 4989)
	assertEq(t, rl.Cost, 2) // summed over both calls
}

func TestDetailPaginatesChecks(t *testing.T) {
	d := &fakeDoer{respond: func(n int, _ string, _ map[string]any) []byte {
		if n == 0 {
			return fixture(t, "detail.json")
		}
		return fixture(t, "detail_checks_page2.json")
	}}
	snaps, _, err := NewClient(d).Details(context.Background(), []string{"PR_p0", "PR_p1"})
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, len(snaps), 2)
	assertEq(t, len(d.calls), 2) // only p0 has a next page
	assertEq(t, d.calls[1].vars["id"].(string), "PR_p0")
	assertEq(t, d.calls[1].vars["after"].(string), "CK1")
	p0 := snaps[0]
	assertEq(t, len(p0.Checks), 3)
	assertEq(t, p0.Checks[2], Check{"legacy/deploy", CheckFailure, true})
}

func TestDetailsBatchesOf20(t *testing.T) {
	d := &fakeDoer{respond: func(int, string, map[string]any) []byte { return []byte(`{}`) }}
	ids := make([]string, 45)
	for i := range ids {
		ids[i] = "PR_" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	if _, _, err := NewClient(d).Details(context.Background(), ids); err != nil {
		t.Fatal(err)
	}
	var sizes []int
	for _, c := range d.calls {
		sizes = append(sizes, len(c.vars))
	}
	if !slices.Equal(sizes, []int{20, 20, 5}) {
		t.Errorf("id var counts = %v, want [20 20 5]", sizes)
	}
	assertEq(t, d.calls[2].vars["id0"].(string), ids[40])
}
