package github

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"
)

type call struct {
	query string
	vars  map[string]any
}

// fakeDoer answers each call from respond and records what it was asked.
// Like go-gh, it decodes the data before returning the call's error, so a
// GraphQL error can arrive alongside a partly filled response.
type fakeDoer struct {
	calls   []call
	respond func(n int, query string, vars map[string]any) []byte
	fail    func(n int, query string, vars map[string]any) error
}

func (f *fakeDoer) DoWithContext(_ context.Context, query string, vars map[string]any, resp any) error {
	n := len(f.calls)
	f.calls = append(f.calls, call{query, vars})
	if err := json.Unmarshal(f.respond(n, query, vars), resp); err != nil {
		return err
	}
	if f.fail != nil {
		return f.fail(n, query, vars)
	}
	return nil
}

func gqlErr(typ string, path ...any) *api.GraphQLError {
	return &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: typ, Message: typ, Path: path}}}
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

func TestDetailsSkipsUnreachablePRs(t *testing.T) {
	d := &fakeDoer{
		respond: func(int, string, map[string]any) []byte { return fixture(t, "detail_unreachable.json") },
		fail:    func(int, string, map[string]any) error { return gqlErr("NOT_FOUND", "p1") },
	}
	snaps, rl, err := NewClient(d).Details(context.Background(), []string{"PR_p1", "PR_gone", "ISSUE_1"})
	if err != nil {
		t.Fatalf("Details error = %v, want the reachable PR", err)
	}
	// p1 is null with a NOT_FOUND error; p2 is a node that isn't a PR.
	if len(snaps) != 1 || snaps[0].ID != "PR_p1" {
		t.Fatalf("snapshots = %+v, want only PR_p1", snaps)
	}
	assertEq(t, rl.Remaining, 4900)
}

func TestDetailsFailsOnOtherGraphQLErrors(t *testing.T) {
	for name, e := range map[string]*api.GraphQLError{
		"no path":        gqlErr("INTERNAL"),
		"other path":     gqlErr("FORBIDDEN", "rateLimit"),
		"rate limited":   gqlErr("RATE_LIMITED", "p1"),
		"one bad of two": {Errors: []api.GraphQLErrorItem{{Type: "NOT_FOUND", Path: []any{"p1"}}, {Type: "INTERNAL"}}},
	} {
		t.Run(name, func(t *testing.T) {
			d := &fakeDoer{
				respond: func(int, string, map[string]any) []byte { return fixture(t, "detail_unreachable.json") },
				fail:    func(int, string, map[string]any) error { return e },
			}
			_, _, err := NewClient(d).Details(context.Background(), []string{"PR_p1", "PR_gone", "ISSUE_1"})
			var got *api.GraphQLError
			if !errors.As(err, &got) || got != e {
				t.Fatalf("Details error = %v, want the GraphQL error", err)
			}
		})
	}
}

func TestDetailsFailsWhenChecksPageFails(t *testing.T) {
	d := &fakeDoer{
		respond: func(n int, _ string, _ map[string]any) []byte {
			if n == 0 {
				return fixture(t, "detail.json")
			}
			return []byte(`{}`)
		},
		fail: func(n int, _ string, _ map[string]any) error {
			if n > 0 {
				return gqlErr("INTERNAL")
			}
			return nil
		},
	}
	if _, _, err := NewClient(d).Details(context.Background(), []string{"PR_p0", "PR_p1"}); err == nil {
		t.Fatal("Details succeeded although a checks page failed")
	}
}
