package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

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
	mu      sync.Mutex
	calls   []call
	respond func(n int, query string, vars map[string]any) []byte
	fail    func(n int, query string, vars map[string]any) error
}

func (f *fakeDoer) DoWithContext(_ context.Context, query string, vars map[string]any, resp any) error {
	f.mu.Lock()
	n := len(f.calls)
	f.calls = append(f.calls, call{query, vars})
	f.mu.Unlock()
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

func TestSearchPaginatesTo100(t *testing.T) {
	pages := [][]byte{fixture(t, "search_page1.json"), fixture(t, "search_page2.json")}
	d := &fakeDoer{respond: func(n int, _ string, _ map[string]any) []byte { return pages[n] }}
	fps, rl, err := NewClient(d).Search(context.Background(), "is:pr")
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, len(fps), 53)
	assertEq(t, fps[0], Fingerprint{ID: "PR_000", UpdatedAt: time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC), HeadOID: "c3", RollupState: "PENDING"})
	assertEq(t, fps[1].RollupState, "") // no checks
	assertEq(t, fps[52].ID, "PR_052")
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

// echoBatch answers a Details batch with a bare PR for each ID variable, and a
// rate limit whose remaining budget is rem(id0).
func echoBatch(vars map[string]any, rem func(id0 string) int) []byte {
	out := map[string]any{}
	for i := 0; ; i++ {
		id, ok := vars[fmt.Sprintf("id%d", i)]
		if !ok {
			break
		}
		out[fmt.Sprintf("p%d", i)] = map[string]any{"id": id}
	}
	out["rateLimit"] = map[string]any{"remaining": rem(vars["id0"].(string)), "cost": 1, "resetAt": "2026-11-18T13:00:00Z"}
	b, _ := json.Marshal(out)
	return b
}

func manyIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("PR_%03d", i)
	}
	return ids
}

func TestDetailsBatchesOf10InOrder(t *testing.T) {
	ids := manyIDs(25)
	// The middle batch reports the lowest remaining budget.
	rem := func(id0 string) int { return map[string]int{"PR_000": 4000, "PR_010": 3900, "PR_020": 3950}[id0] }
	d := &fakeDoer{respond: func(_ int, _ string, vars map[string]any) []byte { return echoBatch(vars, rem) }}
	snaps, rl, err := NewClient(d).Details(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	sizes := map[string]int{}
	for _, c := range d.calls {
		sizes[c.vars["id0"].(string)] = len(c.vars)
	}
	if want := map[string]int{"PR_000": 10, "PR_010": 10, "PR_020": 5}; !maps.Equal(sizes, want) {
		t.Errorf("batch sizes by first ID = %v, want %v", sizes, want)
	}
	var got []string
	for _, s := range snaps {
		got = append(got, s.ID)
	}
	if !slices.Equal(got, ids) {
		t.Errorf("snapshot order = %v, want the request order", got)
	}
	assertEq(t, rl.Remaining, 3900)
	assertEq(t, rl.Cost, 3)
}

// gatedDoer holds every call until release closes, and records how many were
// in flight at once. full closes when limit calls are in flight.
type gatedDoer struct {
	mu             sync.Mutex
	inflight, peak int
	limit          int
	full, release  chan struct{}
}

func (g *gatedDoer) DoWithContext(ctx context.Context, _ string, vars map[string]any, resp any) error {
	g.mu.Lock()
	g.inflight++
	g.peak = max(g.peak, g.inflight)
	if g.inflight == g.limit {
		close(g.full)
	}
	g.mu.Unlock()
	select {
	case <-g.release:
	case <-ctx.Done():
	}
	g.mu.Lock()
	g.inflight--
	g.mu.Unlock()
	return json.Unmarshal(echoBatch(vars, func(string) int { return 1 }), resp)
}

func TestDetailsRunsAtMostThreeBatchesAtOnce(t *testing.T) {
	g := &gatedDoer{limit: 3, full: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, _, err := NewClient(g).Details(context.Background(), manyIDs(70))
		done <- err
	}()
	select {
	case <-g.full:
	case <-time.After(5 * time.Second):
		close(g.release)
		t.Fatal("Details never had 3 batches in flight")
	}
	time.Sleep(50 * time.Millisecond) // room for a fourth call, if one were allowed
	close(g.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	assertEq(t, g.peak, 3)
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

func TestFingerprintsBatchesNodesAndSkipsMissing(t *testing.T) {
	d := &fakeDoer{
		respond: func(int, string, map[string]any) []byte { return fixture(t, "fingerprints.json") },
		fail:    func(int, string, map[string]any) error { return gqlErr("NOT_FOUND", "nodes", 1.0) },
	}
	ids := make([]string, 150)
	for i := range ids {
		ids[i] = "PR_" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	fps, rl, err := NewClient(d).Fingerprints(context.Background(), ids)
	if err != nil {
		t.Fatalf("Fingerprints error = %v, want the missing node skipped", err)
	}
	var sizes []int
	for _, c := range d.calls {
		sizes = append(sizes, len(c.vars["ids"].([]string)))
	}
	if !slices.Equal(sizes, []int{100, 50}) {
		t.Errorf("ids per call = %v, want [100 50]", sizes)
	}
	// Each call answers with one PR, a null and a non-PR node.
	if len(fps) != 2 || fps[0].ID != "PR_a" || fps[0].RollupState != "SUCCESS" || fps[0].HeadOID != "a1" {
		t.Errorf("fingerprints = %+v, want PR_a from each call", fps)
	}
	assertEq(t, rl.Cost, 2)
}

func TestFingerprintsFailsOnOtherErrors(t *testing.T) {
	d := &fakeDoer{
		respond: func(int, string, map[string]any) []byte { return fixture(t, "fingerprints.json") },
		fail:    func(int, string, map[string]any) error { return gqlErr("RATE_LIMITED") },
	}
	if _, _, err := NewClient(d).Fingerprints(context.Background(), []string{"PR_a"}); err == nil {
		t.Fatal("Fingerprints succeeded despite a rate-limit error")
	}
}

func TestSnapshotFingerprint(t *testing.T) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(fixture(t, "detail.json"), &raw); err != nil {
		t.Fatal(err)
	}
	snap, err := parsePR(raw["p0"])
	if err != nil {
		t.Fatal(err)
	}
	want := Fingerprint{ID: "PR_p0", UpdatedAt: time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC), HeadOID: "c3", RollupState: "PENDING"}
	if got := snap.Fingerprint(); !got.Same(want) {
		t.Errorf("Fingerprint() = %+v, want %+v", got, want)
	}
	for name, change := range map[string]func(*Fingerprint){
		"updatedAt": func(f *Fingerprint) { f.UpdatedAt = f.UpdatedAt.Add(time.Second) },
		"rollup":    func(f *Fingerprint) { f.RollupState = "SUCCESS" },
		"head":      func(f *Fingerprint) { f.HeadOID = "c4" },
	} {
		moved := want
		change(&moved)
		if moved.Same(want) {
			t.Errorf("Same ignores a changed %s", name)
		}
	}
	// The same instant in another zone, as after a round trip through the store.
	if !want.Same(Fingerprint{ID: "PR_p0", UpdatedAt: want.UpdatedAt.In(time.FixedZone("x", 3600)), HeadOID: "c3", RollupState: "PENDING"}) {
		t.Error("Same compares time zones, not instants")
	}
}
