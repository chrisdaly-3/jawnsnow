package github

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestReviewContextParses(t *testing.T) {
	page2 := fixture(t, "context_files_page2.json")
	d := &fakeDoer{respond: func(n int, _ string, _ map[string]any) []byte {
		if n == 0 {
			return fixture(t, "context.json")
		}
		return page2
	}}
	pc, _, err := NewClient(d).ReviewContext(context.Background(), "PR_1")
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, pc.Body, "Fixes the thing.")
	assertEq(t, len(pc.Comments), 2)
	assertEq(t, pc.Comments[0], TextComment{Author: "alice", Body: "Looks good", At: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)})
	assertEq(t, pc.Comments[1].Author, "") // null author
	assertEq(t, len(pc.Threads), 2)
	th := pc.Threads[0]
	assertEq(t, th.ID, "PRT_1")
	assertEq(t, th.Path, "a/file0.go")
	assertEq(t, th.Line, 12)
	assertEq(t, th.Resolved, true)
	assertEq(t, len(th.Comments), 2)
	assertEq(t, th.Comments[1].Body, "done")
	assertEq(t, pc.Threads[1].Line, 0) // outdated thread: null line
	assertEq(t, pc.Threads[1].Resolved, false)
}

func TestReviewContextPaginatesFiles(t *testing.T) {
	page2 := fixture(t, "context_files_page2.json")
	d := &fakeDoer{respond: func(n int, _ string, _ map[string]any) []byte {
		if n == 0 {
			return fixture(t, "context.json")
		}
		return page2
	}}
	pc, rl, err := NewClient(d).ReviewContext(context.Background(), "PR_1")
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, len(pc.Files), 103)
	assertEq(t, pc.Files[0], "a/file0.go")
	assertEq(t, pc.Files[102], "b/file2.go")
	assertEq(t, len(d.calls), 2)
	assertEq(t, d.calls[1].vars["after"], any("CUR1"))
	assertEq(t, d.calls[1].vars["id"], any("PR_1"))
	assertEq(t, rl.Cost, 2)
	// Later pages fetch files only.
	if strings.Contains(d.calls[1].query, "reviewThreads") {
		t.Error("files page re-fetched threads")
	}
}

func TestReviewContextStopsAtFivePages(t *testing.T) {
	d := &fakeDoer{respond: func(int, string, map[string]any) []byte { return fixture(t, "context.json") }}
	pc, _, err := NewClient(d).ReviewContext(context.Background(), "PR_1")
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, len(d.calls), 5)
	assertEq(t, len(pc.Files), 500)
}

func TestReviewContextIsReadOnly(t *testing.T) {
	for _, q := range []string{reviewContextQuery, reviewFilesPageQuery} {
		if strings.Contains(strings.ToLower(q), "mutation") {
			t.Errorf("query contains a mutation: %s", q)
		}
		if !strings.HasPrefix(q, "query ") {
			t.Errorf("not a query: %s", q)
		}
	}
}

func TestReviewContextRateLimitErrorIsRecognisable(t *testing.T) {
	d := &fakeDoer{
		respond: func(int, string, map[string]any) []byte { return []byte(`{}`) },
		fail:    func(int, string, map[string]any) error { return gqlErr("RATE_LIMITED") },
	}
	_, _, err := NewClient(d).ReviewContext(context.Background(), "PR_1")
	if !IsRateLimited(err) {
		t.Fatalf("err = %v, want rate limited", err)
	}
}
