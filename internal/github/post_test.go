package github

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"
)

func okPoster(t *testing.T) (Poster, *fakeDoer) {
	d := &fakeDoer{respond: func(int, string, map[string]any) []byte { return fixture(t, "post_ok.json") }}
	return NewPoster(d), d
}

func TestPostReviewSendsExactInput(t *testing.T) {
	p, d := okPoster(t)
	res, err := p.PostReview(context.Background(), PostInput{
		PRID: "PR_1", CommitOID: "abc123", Event: "REQUEST_CHANGES", Body: "## Summary\n\"quoted\"",
		Threads: []PostThread{{Path: "a.go", Line: 12, Body: "nit"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, len(d.calls), 1)
	if !strings.HasPrefix(d.calls[0].query, "mutation") {
		t.Errorf("not a mutation: %s", d.calls[0].query)
	}
	if strings.Contains(d.calls[0].query, "abc123") || strings.Contains(d.calls[0].query, "quoted") {
		t.Error("a value was interpolated into the query")
	}
	in := d.calls[0].vars["input"].(map[string]any)
	assertEq(t, in["pullRequestId"], any("PR_1"))
	assertEq(t, in["commitOID"], any("abc123"))
	assertEq(t, in["event"], any("REQUEST_CHANGES"))
	assertEq(t, in["body"], any("## Summary\n\"quoted\""))
	th := in["threads"].([]map[string]any)
	assertEq(t, len(th), 1)
	assertEq(t, th[0]["path"], any("a.go"))
	assertEq(t, th[0]["line"], any(12))
	assertEq(t, th[0]["side"], any("RIGHT"))
	assertEq(t, th[0]["body"], any("nit"))
	for _, k := range []string{"startLine", "startSide"} {
		if _, ok := th[0][k]; ok {
			t.Errorf("single-line thread has %s", k)
		}
	}
	assertEq(t, res.ReviewID, "PRR_1")
	assertEq(t, res.URL, "https://github.com/o/r/pull/1#pullrequestreview-1")
	assertEq(t, len(res.Comments), 2)
	assertEq(t, res.Comments[0], PostedComment{ID: "PRRC_1", Path: "a.go", Line: 12, OriginalLine: 12})
	// A comment on an older commit can have no line at head, but keeps the
	// line it was posted on.
	assertEq(t, res.Comments[1], PostedComment{ID: "PRRC_2", Path: "b.go", Line: 0, OriginalLine: 7})
}

func TestPostReviewRangeThread(t *testing.T) {
	p, d := okPoster(t)
	_, err := p.PostReview(context.Background(), PostInput{PRID: "PR_1", CommitOID: "c", Event: "COMMENT",
		Threads: []PostThread{{Path: "a.go", Line: 20, StartLine: 15, Body: "range"}}})
	if err != nil {
		t.Fatal(err)
	}
	th := d.calls[0].vars["input"].(map[string]any)["threads"].([]map[string]any)[0]
	assertEq(t, th["startLine"], any(15))
	assertEq(t, th["startSide"], any("RIGHT"))
	assertEq(t, th["line"], any(20))
	assertEq(t, th["side"], any("RIGHT"))
}

func TestPostReviewError(t *testing.T) {
	d := &fakeDoer{
		respond: func(int, string, map[string]any) []byte { return fixture(t, "post_error.json") },
		fail: func(int, string, map[string]any) error {
			return &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: "RATE_LIMITED", Message: "API rate limit exceeded for user"}}}
		},
	}
	_, err := NewPoster(d).PostReview(context.Background(), PostInput{PRID: "PR_1", CommitOID: "c", Event: "COMMENT"})
	if err == nil || !strings.Contains(err.Error(), "API rate limit exceeded for user") {
		t.Fatalf("err = %v", err)
	}
	if !IsRateLimited(err) {
		t.Error("rate limit not recognised")
	}
	assertEq(t, len(d.calls), 1) // never retried

	// A response with no review and no error is still a failure.
	_, err = NewPoster(&fakeDoer{respond: func(int, string, map[string]any) []byte { return fixture(t, "post_error.json") }}).
		PostReview(context.Background(), PostInput{PRID: "PR_1", CommitOID: "c", Event: "COMMENT"})
	if err == nil {
		t.Error("empty response accepted")
	}
}

func TestThreadIDsMapsFirstComment(t *testing.T) {
	d := &fakeDoer{respond: func(int, string, map[string]any) []byte { return fixture(t, "threads.json") }}
	// The fixture says a previous page exists, but PRRC_2 is its only
	// requested comment, so one call is enough.
	got, err := NewPoster(d).ThreadIDs(context.Background(), "PR_1", []string{"PRRC_2"})
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, len(got), 1)
	assertEq(t, got["PRRC_2"], "PRT_2")
	assertEq(t, len(d.calls), 1)
	assertEq(t, d.calls[0].vars["pr"], any("PR_1"))
	if strings.HasPrefix(d.calls[0].query, "mutation") {
		t.Error("thread lookup is a mutation")
	}
}

func TestThreadIDsPages(t *testing.T) {
	pages := [][]byte{fixture(t, "threads.json"), fixture(t, "threads_page2.json")}
	d := &fakeDoer{respond: func(n int, _ string, _ map[string]any) []byte { return pages[n] }}
	got, err := NewPoster(d).ThreadIDs(context.Background(), "PR_1", []string{"PRRC_1", "PRRC_2", "PRRC_MISSING"})
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, len(got), 2)
	assertEq(t, got["PRRC_1"], "PRT_1")
	assertEq(t, got["PRRC_2"], "PRT_2")
	assertEq(t, len(d.calls), 2)
	assertEq(t, d.calls[0].vars["before"], nil)
	assertEq(t, d.calls[1].vars["before"], any("CUR_BACK"))
}

func TestThreadIDsStopsAtFivePages(t *testing.T) {
	d := &fakeDoer{respond: func(int, string, map[string]any) []byte { return fixture(t, "threads.json") }}
	got, err := NewPoster(d).ThreadIDs(context.Background(), "PR_1", []string{"PRRC_MISSING"})
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, len(got), 0)
	assertEq(t, len(d.calls), 5)
}

func TestClientStaysReadOnly(t *testing.T) {
	for _, f := range []string{"client.go", "query.go", "context.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(string(src)), "mutation") {
			t.Errorf("%s contains a mutation", f)
		}
	}
}

func TestPostReviewRejectsBadInput(t *testing.T) {
	for name, in := range map[string]PostInput{
		"dismiss":      {PRID: "PR_1", CommitOID: "c", Event: "DISMISS"},
		"empty event":  {PRID: "PR_1", CommitOID: "c", Event: ""},
		"lowercase":    {PRID: "PR_1", CommitOID: "c", Event: "approve"},
		"empty commit": {PRID: "PR_1", CommitOID: "", Event: "COMMENT"},
	} {
		t.Run(name, func(t *testing.T) {
			p, d := okPoster(t)
			if _, err := p.PostReview(context.Background(), in); err == nil {
				t.Error("bad input accepted")
			}
			assertEq(t, len(d.calls), 0)
		})
	}
}

func TestPostReviewKeepsEveryThread(t *testing.T) {
	p, d := okPoster(t)
	_, err := p.PostReview(context.Background(), PostInput{PRID: "PR_1", CommitOID: "c", Event: "COMMENT", Threads: []PostThread{
		{Path: "a.go", Line: 3, Body: "one"},
		{Path: "b.go", Line: 20, StartLine: 15, Body: "two"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	th := d.calls[0].vars["input"].(map[string]any)["threads"].([]map[string]any)
	assertEq(t, len(th), 2)
	assertEq(t, th[0]["path"], any("a.go"))
	assertEq(t, th[0]["line"], any(3))
	assertEq(t, th[0]["side"], any("RIGHT"))
	assertEq(t, th[0]["body"], any("one"))
	if _, ok := th[0]["startLine"]; ok {
		t.Error("single-line thread has startLine")
	}
	assertEq(t, th[1]["path"], any("b.go"))
	assertEq(t, th[1]["line"], any(20))
	assertEq(t, th[1]["side"], any("RIGHT"))
	assertEq(t, th[1]["startLine"], any(15))
	assertEq(t, th[1]["startSide"], any("RIGHT"))
	assertEq(t, th[1]["body"], any("two"))
}

func TestQueryTextMatchesVariables(t *testing.T) {
	for _, s := range []string{"$input: AddPullRequestReviewInput!", "addPullRequestReview(input: $input)",
		"nodes { id path line originalLine }"} {
		if !strings.Contains(addReviewMutation, s) {
			t.Errorf("mutation lacks %q", s)
		}
	}
	for _, s := range []string{"$pr: ID!", "$before: String", "node(id: $pr)", "before: $before"} {
		if !strings.Contains(threadIDsQuery, s) {
			t.Errorf("thread query lacks %q", s)
		}
	}
}
