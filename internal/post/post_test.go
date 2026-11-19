package post

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"github.com/chrisdaly-3/jawnsnow/internal/store"
)

func review(findings ...store.Finding) store.Review {
	return store.Review{ID: "r1", PRID: "PR_node", HeadOID: "0123456789abcdef", Verdict: "comment", Findings: findings}
}

func anchored(id int64, path string, line int, body string) store.Finding {
	return store.Finding{ID: id, Path: path, Line: line, Body: body, Ledger: "new"}
}

func unanchored(id int64, path, body string) store.Finding {
	return store.Finding{ID: id, Path: path, Body: body, Unanchored: true, Ledger: "new"}
}

func TestDefaultSelectionFromVerdict(t *testing.T) {
	for verdict, want := range map[string]Event{
		"approve":         "APPROVE",
		"request_changes": "REQUEST_CHANGES",
		"comment":         "COMMENT",
		"":                "COMMENT",
		"whatever":        "COMMENT",
	} {
		r := review()
		r.Verdict = verdict
		r.VerdictReason = "private reason"
		r.Understanding = "private understanding"
		sel := DefaultSelection(r)
		if sel.Event != want {
			t.Errorf("verdict %q: event = %q, want %q", verdict, sel.Event, want)
		}
		if sel.Body != "" {
			t.Errorf("verdict %q: body = %q, want empty", verdict, sel.Body)
		}
	}
}

func TestDefaultSelectionSkipsAddressedAndPosted(t *testing.T) {
	fresh := anchored(1, "a.go", 1, "x")
	addressed := anchored(2, "a.go", 2, "x")
	addressed.Ledger = "addressed"
	answered := anchored(3, "a.go", 3, "x")
	answered.Ledger = "answered"
	posted := anchored(4, "a.go", 4, "x")
	posted.ThreadID = "PRRT_1"
	outstanding := anchored(5, "a.go", 5, "x")
	outstanding.Ledger = "outstanding"
	oldNote := unanchored(6, "b.go", "old note")
	oldNote.Ledger = "addressed"
	newNote := unanchored(7, "c.go", "new note")

	sel := DefaultSelection(review(fresh, addressed, answered, posted, outstanding, oldNote, newNote))
	for id, wantIncluded := range map[int64]bool{1: true, 2: false, 3: false, 4: false, 5: true, 7: true} {
		if got := sel.Include[id] || !hasKey(sel.Include, id); got != wantIncluded {
			t.Errorf("finding %d: included = %v, want %v", id, got, wantIncluded)
		}
	}
	if sel.Include[6] || !hasKey(sel.Include, 6) {
		t.Error("addressed unanchored finding should be excluded")
	}
	if sel.Body != "" {
		t.Errorf("default body must hold only the user's own summary: %q", sel.Body)
	}
	in, _, err := Build(review(fresh, oldNote, newNote), "PR", sel)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(in.Body, "old note") || !strings.Contains(in.Body, "new note") {
		t.Errorf("body should list only the included unanchored finding: %q", in.Body)
	}
}

func hasKey(m map[int64]bool, k int64) bool { _, ok := m[k]; return ok }

func TestBuildAnchoredAndUnanchored(t *testing.T) {
	r := review(
		anchored(1, "a.go", 10, "single"),
		store.Finding{ID: 2, Path: "b.go", Line: 20, StartLine: 15, Body: "range"},
		store.Finding{ID: 3, Path: "c.go", Line: 5, StartLine: 5, Body: "same line"},
		unanchored(4, "d.go", "line one\nline two"),
	)
	sel := Selection{Event: "COMMENT", Body: "Summary text"}
	in, p, err := Build(r, "PR_node", sel)
	if err != nil {
		t.Fatal(err)
	}
	if in.PRID != "PR_node" || in.Event != "COMMENT" || in.CommitOID != r.HeadOID {
		t.Errorf("header wrong: %+v", in)
	}
	wantBody := "Summary text\n\n**Other notes**\n\n- `d.go`: line one\n  line two"
	if in.Body != wantBody {
		t.Errorf("body = %q, want %q", in.Body, wantBody)
	}
	if len(in.Threads) != 3 {
		t.Fatalf("threads = %d", len(in.Threads))
	}
	if in.Threads[0].StartLine != 0 || in.Threads[1].StartLine != 15 || in.Threads[1].Line != 20 || in.Threads[2].StartLine != 0 {
		t.Errorf("start lines wrong: %+v", in.Threads)
	}
	wantLabels := []string{"Summary", "a.go:10", "b.go:15-20", "c.go:5"}
	var labels []string
	for _, part := range p.Parts {
		labels = append(labels, part.Label)
	}
	if !reflect.DeepEqual(labels, wantLabels) {
		t.Errorf("labels = %v, want %v", labels, wantLabels)
	}
	if p.Commit != "0123456" || p.Event != "COMMENT" {
		t.Errorf("preview header = %+v", p)
	}
}

func TestBuildAddsOtherNotesOnce(t *testing.T) {
	r := review(unanchored(1, "d.go", "note"), anchored(2, "a.go", 1, "x"))
	sel := DefaultSelection(r)
	in, _, err := Build(r, "PR", sel)
	if err != nil {
		t.Fatal(err)
	}
	if want := "**Other notes**\n\n- `d.go`: note"; in.Body != want {
		t.Errorf("body = %q, want %q", in.Body, want)
	}
}

func TestUncheckedNoteIsNotPosted(t *testing.T) {
	r := review(unanchored(1, "a.go", "note one"), unanchored(2, "b.go", "note two"), unanchored(3, "c.go", "note three"))
	sel := DefaultSelection(r)
	sel.Include[2] = false
	in, _, err := Build(r, "PR", sel)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(in.Body, "note two") || !strings.Contains(in.Body, "note one") || !strings.Contains(in.Body, "note three") {
		t.Errorf("body = %q", in.Body)
	}
	if n := strings.Count(in.Body, "**Other notes**"); n != 1 {
		t.Errorf("Other notes count = %d", n)
	}
}

func TestNotePathWithBacktick(t *testing.T) {
	r := review(unanchored(1, "a`b.go", "n"), unanchored(2, "c``d.go", "n"))
	in, _, err := Build(r, "PR", Selection{Event: "COMMENT"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- `` a`b.go ``: n", "- ``` c``d.go ```: n"} {
		if !strings.Contains(in.Body, want) {
			t.Errorf("missing %q in %q", want, in.Body)
		}
	}
}

func TestRequestChangesBlankAfterSanitize(t *testing.T) {
	r := review(unanchored(1, "a.go", "n"))
	_, _, err := Build(r, "PR", Selection{Event: "REQUEST_CHANGES", Body: "\u200b\ufeff"})
	if err == nil {
		t.Error("want error: notes do not count as the summary")
	}
}

func TestHiddenContentResistsEvasion(t *testing.T) {
	for _, tc := range []struct {
		name, md string
		want     []string
	}{
		{"fence in bullet", "- note\n  ```\n<!-- hidden -->\n@victim", []string{"HTML comment", "mentions @victim"}},
		{"fence in ordered item", "1. x\n   ```\n<!-- hidden -->\n@victim", []string{"HTML comment", "mentions @victim"}},
		{"backtick in info string", "```x`\n<!-- hidden -->\n```", []string{"HTML comment"}},
		{"escaped backtick comment", "\\` <!-- secret --> `", []string{"HTML comment"}},
		{"escaped backtick tag", "\\`<b>x</b>`", []string{"raw HTML"}},
		{"mention after hyphen", "x-@alice", []string{"mentions @alice"}},
		{"mention after dot", "y.@bob", []string{"mentions @bob"}},
		{"mention after slash", "z/@carol", []string{"mentions @carol"}},
		{"team mention", "ping @org/team-a", []string{"mentions @org/team-a"}},
		{"link reference", "[//]: # (hidden note)", []string{"link reference (renders as nothing)"}},
		{"processing instruction", "<?x hidden ?>", []string{"raw HTML"}},
		{"doctype", "<!DOCTYPE html>", []string{"raw HTML"}},
		{"email", "user@example.com", nil},
		{"underscore emphasis", "_x_@victim", []string{"mentions @victim"}},
		{"double underscore emphasis", "__x__@victim", []string{"mentions @victim"}},
		{"star emphasis", "*x*@erin", []string{"mentions @erin"}},
		{"strikethrough", "~~x~~@dave", []string{"mentions @dave"}},
		{"escaped underscore blocks", "a\\_@alice", nil},
		{"plus before mention", "w+@dan", []string{"mentions @dan"}},
		{"line start after soft break", "a\n@bob", []string{"mentions @bob"}},
		{"mention in html block", "<div>\n@victim\n</div>", []string{"raw HTML", "mentions @victim"}},
		{"mention after comment", "<!-- x --> @victim", []string{"HTML comment", "mentions @victim"}},
		{"comment inside tags", "<div><!-- x --></div>", []string{"HTML comment", "raw HTML"}},
		{"decimal entity", "&#64;ivan", []string{"mentions @ivan"}},
		{"hex entity", "&#x40;jo", []string{"mentions @jo"}},
		{"upper hex entity", "&#X40;kay", []string{"mentions @kay"}},
		{"named entity", "&commat;lee", []string{"mentions @lee"}},
		{"code span mention", "`@alice`", nil},
		{"indented code", "para\n\n    <!-- c -->\n    @alice", nil},
	} {
		if got := HiddenContent(tc.md); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSanitizeDropsInvisibles(t *testing.T) {
	if got := Sanitize("a\u202eb\u200bc\u2066d\ufeffe\u2060f"); got != "abcdef" {
		t.Errorf("got %q", got)
	}
}

func TestBuildExcludesUnchecked(t *testing.T) {
	r := review(anchored(1, "a.go", 1, "keep"), anchored(2, "b.go", 2, "drop"), unanchored(3, "c.go", "dropped note"))
	sel := Selection{Event: "COMMENT", Include: map[int64]bool{2: false, 3: false}}
	in, _, err := Build(r, "PR", sel)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Threads) != 1 || in.Threads[0].Path != "a.go" {
		t.Errorf("threads = %+v", in.Threads)
	}
	if in.Body != "" {
		t.Errorf("body = %q", in.Body)
	}
}

func TestBuildRequestChangesNeedsBody(t *testing.T) {
	r := review(anchored(1, "a.go", 1, "x"))
	_, _, err := Build(r, "PR", Selection{Event: "REQUEST_CHANGES", Body: " \n "})
	if err == nil || err.Error() != "Request changes needs a summary — press e to write one" {
		t.Errorf("err = %v", err)
	}
	if _, _, err := Build(r, "PR", Selection{Event: "REQUEST_CHANGES", Body: "why"}); err != nil {
		t.Errorf("with body: %v", err)
	}
}

func TestBuildNothingToPost(t *testing.T) {
	r := review(anchored(1, "a.go", 1, "x"))
	_, _, err := Build(r, "PR", Selection{Event: "COMMENT", Include: map[int64]bool{1: false}})
	if err == nil {
		t.Error("want an error for an empty COMMENT")
	}
	if _, _, err := Build(r, "PR", Selection{Event: "APPROVE", Include: map[int64]bool{1: false}}); err != nil {
		t.Errorf("a bare approval is allowed: %v", err)
	}
}

func TestBuildRejectsBadLine(t *testing.T) {
	r := review(anchored(1, "a.go", 0, "x"))
	if _, _, err := Build(r, "PR", Selection{Event: "COMMENT"}); err == nil {
		t.Error("want an error for line 0")
	}
	// An excluded or unanchored finding with no line is fine.
	r = review(anchored(1, "a.go", 0, "x"), unanchored(2, "b.go", "n"))
	if _, _, err := Build(r, "PR", Selection{Event: "COMMENT", Include: map[int64]bool{1: false}}); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestPreviewEqualsPayload(t *testing.T) {
	r := review(
		anchored(1, "a.go", 1, "red \x1b[31mtext\x1b[0m bell\x07 ok\ttab\nnext"),
		unanchored(2, "b\x1b[1m.go", "note \x1b]0;title\x07here"),
	)
	in, p, err := Build(r, "PR", Selection{Event: "COMMENT", Body: "sum\x07mary"})
	if err != nil {
		t.Fatal(err)
	}
	if in.Threads[0].Body != "red text bell ok\ttab\nnext" {
		t.Errorf("thread body = %q", in.Threads[0].Body)
	}
	if strings.ContainsAny(in.Body, "\x1b\x07") || !strings.Contains(in.Body, "- `b.go`: note here") {
		t.Errorf("body = %q", in.Body)
	}
	if p.Parts[0].Markdown != in.Body {
		t.Errorf("summary part differs from body")
	}
	for i, th := range in.Threads {
		if p.Parts[i+1].Markdown != th.Body {
			t.Errorf("part %d differs from thread body", i+1)
		}
	}
}

func TestBuildUsesReviewedCommit(t *testing.T) {
	r := review(anchored(1, "a.go", 1, "x"))
	in, _, err := Build(r, "PR", Selection{Event: "COMMENT"})
	if err != nil {
		t.Fatal(err)
	}
	if in.CommitOID != r.HeadOID {
		t.Errorf("CommitOID = %q", in.CommitOID)
	}
}

func TestBuildKeepsMarkdown(t *testing.T) {
	body := "**bold** and `code`\n\n```go\nfmt.Println(\"<b>\")\n```\n\n```suggestion\nreplacement\n```\n"
	in, _, err := Build(review(anchored(1, "a.go", 3, body)), "PR", Selection{Event: "COMMENT"})
	if err != nil {
		t.Fatal(err)
	}
	if in.Threads[0].Body != body {
		t.Errorf("body changed: %q", in.Threads[0].Body)
	}
}

func TestOtherNotesIsOneMarkdownList(t *testing.T) {
	r := review(unanchored(1, "a.go", "first"), unanchored(2, "b.go", "second\nthird line"))
	in, _, err := Build(r, "PR", Selection{Event: "COMMENT"})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := goldmark.New(goldmark.WithExtensions(extension.GFM)).Convert([]byte(in.Body), &buf); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	if n := strings.Count(html, "<ul>"); n != 1 {
		t.Errorf("<ul> count = %d in %s", n, html)
	}
	if n := strings.Count(html, "<li>"); n != 2 {
		t.Errorf("<li> count = %d in %s", n, html)
	}
	li := html[strings.LastIndex(html, "<li>"):]
	if !strings.Contains(li[:strings.Index(li, "</li>")], "third line") {
		t.Errorf("second line escaped its item: %s", html)
	}
}

func TestHiddenContent(t *testing.T) {
	for _, tc := range []struct {
		name, md string
		want     []string
	}{
		{"comment", "<!-- x -->", []string{"HTML comment"}},
		{"html", "<details>", []string{"raw HTML"}},
		{"closing", "</div>", []string{"raw HTML"}},
		{"image", "![a](u)", []string{"image"}},
		{"mentions", "cc @alice and @bob and @alice", []string{"mentions @alice, @bob"}},
		{"all in order", "@z ![a](u) <b>x</b> <!-- c -->", []string{"HTML comment", "raw HTML", "image", "mentions @z"}},
		{"code span", "`@alice` and ``<b>``", nil},
		{"fence", "text\n```\n<b>\n@alice\n```\nmore", nil},
		{"tilde fence", "~~~go\n<!-- c -->\n~~~", nil},
		{"after fence", "```\n<b>\n```\n<i>", []string{"raw HTML"}},
		{"email", "mail a@b.com", nil},
		{"autolink", "<https://example.com>", nil},
		{"comparison", "if a < b and c > d", nil},
		{"plain", "just **markdown** with [a link](u)", nil},
	} {
		if got := HiddenContent(tc.md); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPreviewHiddenIsPrefixed(t *testing.T) {
	r := review(anchored(1, "a.go", 2, "ping @bob"))
	_, p, err := Build(r, "PR", Selection{Event: "COMMENT", Body: "<!-- hid -->"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Summary: HTML comment", "a.go:2: mentions @bob"}
	if !reflect.DeepEqual(p.Hidden, want) {
		t.Errorf("hidden = %v, want %v", p.Hidden, want)
	}
}

func TestSanitize(t *testing.T) {
	got := Sanitize("a\x1b[1;31mb\x1b[0m\x1b]8;;http://x\x07c\x1b]0;t\x1b\\d\x00e\rf\ng\th\x1b")
	if got != "abcdef\ng\th" {
		t.Errorf("got %q", got)
	}
}
