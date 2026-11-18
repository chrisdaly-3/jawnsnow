package review

import (
	"strings"
	"testing"

	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/workspace"
)

func TestCheckFindings(t *testing.T) {
	files := workspace.ReviewFiles{Hunks: map[string][][2]int{
		"a.go":      {{10, 20}, {40, 50}},
		"docs/x.md": {{1, 30}},
	}}
	sugg := "Use this:\n```suggestion\nx := 1\n```\n"
	tests := []struct {
		name       string
		in         OutFinding
		want       store.Finding // zero means dropped
		dropped    bool
		wantNote   string
		bodyAbsent []string
	}{
		{
			name: "clean finding passes through unchanged",
			in:   OutFinding{Path: "a.go", Line: 12, Blocking: true, Body: "This leaks the file handle.", Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 12, Blocking: true, Body: "This leaks the file handle.", Category: "bug", Ledger: "new"},
		},
		{
			name: "line outside every hunk is unanchored",
			in:   OutFinding{Path: "a.go", Line: 30, Body: "Late check.", Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 30, Body: "Late check.", Category: "bug", Unanchored: true, Ledger: "new"},
		},
		{
			name: "unknown path is unanchored",
			in:   OutFinding{Path: "nope.go", Line: 12, Body: "Hm.", Category: "bug"},
			want: store.Finding{Path: "nope.go", Line: 12, Body: "Hm.", Category: "bug", Unanchored: true, Ledger: "new"},
		},
		{
			name: "range with start outside hunks is unanchored",
			in:   OutFinding{Path: "a.go", StartLine: 5, Line: 12, Body: "Range.", Category: "bug"},
			want: store.Finding{Path: "a.go", StartLine: 5, Line: 12, Body: "Range.", Category: "bug", Unanchored: true, Ledger: "new"},
		},
		{
			name: "path ./a.go is normalised",
			in:   OutFinding{Path: "./a.go", Line: 41, Body: "Fine.", Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 41, Body: "Fine.", Category: "bug", Ledger: "new"},
		},
		{
			name:     "banned word mid sentence is removed without a double space",
			in:       OutFinding{Path: "a.go", Line: 12, Body: "This is specifically wrong.", Category: "bug"},
			want:     store.Finding{Path: "a.go", Line: 12, Body: "This is wrong.", Category: "bug", Ledger: "new"},
			wantNote: "banned",
		},
		{
			name: "banned phrase at the start leaves no dangling comma and recapitalises",
			in:   OutFinding{Path: "a.go", Line: 12, Body: "Notably, the loop never ends.", Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 12, Body: "The loop never ends.", Category: "bug", Ledger: "new"},
		},
		{
			name: "banned phrase between commas",
			in:   OutFinding{Path: "a.go", Line: 12, Body: "It fails, essentially, on empty input.", Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 12, Body: "It fails, on empty input.", Category: "bug", Ledger: "new"},
		},
		{
			name: "banned phrase before a period, any case, curly apostrophe",
			in:   OutFinding{Path: "a.go", Line: 12, Body: "Bad, CRUCIALLY. It’s worth noting this. LGTM", Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 12, Body: "Bad. This.", Category: "bug", Ledger: "new"},
		},
		{
			name: "multi word phrases",
			in:   OutFinding{Path: "a.go", Line: 12, Body: "Looks good here, but I checked it and confirmed at a.go:3 that it is verified.", Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 12, Body: "Here, but it and a.go:3 that it is.", Category: "bug", Ledger: "new"},
		},
		{
			name: "banned word inside a larger word is kept",
			in:   OutFinding{Path: "a.go", Line: 12, Body: "Use concretely typed values.", Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 12, Body: "Use concretely typed values.", Category: "bug", Ledger: "new"},
		},
		{
			name: "banned word inside code is kept",
			in:   OutFinding{Path: "a.go", Line: 12, Body: "Rename `verified` to ok:\n```go\nverified := true\n```", Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 12, Body: "Rename `verified` to ok:\n```go\nverified := true\n```", Category: "bug", Ledger: "new"},
		},
		{
			name: "commit SHA is replaced",
			in:   OutFinding{Path: "a.go", Line: 12, Body: "Broke in 1a2b3c4 and in 0123456789abcdef0123456789abcdef01234567.", Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 12, Body: "Broke in <commit> and in <commit>.", Category: "bug", Ledger: "new"},
		},
		{
			name: "English hex words and plain numbers are not SHAs",
			in:   OutFinding{Path: "a.go", Line: 12, Body: "Use deadbeef or 1234567 or abcdef, not 1a2b3c.", Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 12, Body: "Use deadbeef or 1234567 or abcdef, not 1a2b3c.", Category: "bug", Ledger: "new"},
		},
		{
			name: "suggestion kept inside one hunk",
			in:   OutFinding{Path: "a.go", StartLine: 11, Line: 13, Body: sugg, Category: "bug"},
			want: store.Finding{Path: "a.go", StartLine: 11, Line: 13, Body: sugg, Category: "bug", Ledger: "new"},
		},
		{
			name: "suggestion on an unanchored finding becomes a plain block",
			in:   OutFinding{Path: "a.go", Line: 30, Body: sugg, Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 30, Body: strings.Replace(sugg, "```suggestion", "```", 1), Category: "bug", Unanchored: true, Ledger: "new"},
		},
		{
			name: "suggestion spanning two hunks becomes a plain block but stays anchored",
			in:   OutFinding{Path: "a.go", StartLine: 15, Line: 41, Body: sugg, Category: "bug"},
			want: store.Finding{Path: "a.go", StartLine: 15, Line: 41, Body: strings.Replace(sugg, "```suggestion", "```", 1), Category: "bug", Ledger: "new"},
		},
		{
			name:     "non-blocking design comment on markdown is dropped",
			in:       OutFinding{Path: "docs/x.md", Line: 3, Body: "Reword this heading.", Category: "design"},
			dropped:  true,
			wantNote: "dropped 1 style comment on docs",
		},
		{
			name: "blocking design comment on markdown is kept",
			in:   OutFinding{Path: "docs/x.md", Line: 3, Blocking: true, Body: "Contradicts the spec.", Category: "design"},
			want: store.Finding{Path: "docs/x.md", Line: 3, Blocking: true, Body: "Contradicts the spec.", Category: "design", Ledger: "new"},
		},
		{
			name: "design comment on markdown naming a broken link is kept",
			in:   OutFinding{Path: "docs/x.md", Line: 3, Body: "The link to setup.md is a broken link.", Category: "design"},
			want: store.Finding{Path: "docs/x.md", Line: 3, Body: "The link to setup.md is a broken link.", Category: "design", Ledger: "new"},
		},
		{
			name: "design comment on code is kept",
			in:   OutFinding{Path: "a.go", Line: 12, Body: "Rename this.", Category: "design"},
			want: store.Finding{Path: "a.go", Line: 12, Body: "Rename this.", Category: "design", Ledger: "new"},
		},
		{
			name: "non-design comment on markdown is kept",
			in:   OutFinding{Path: "docs/x.md", Line: 3, Body: "The command is wrong.", Category: "bug"},
			want: store.Finding{Path: "docs/x.md", Line: 3, Body: "The command is wrong.", Category: "bug", Ledger: "new"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kept, notes := CheckFindings(Output{Findings: []OutFinding{tc.in}}, files)
			if tc.dropped {
				if len(kept) != 0 {
					t.Fatalf("kept %+v, want dropped", kept)
				}
			} else {
				if len(kept) != 1 {
					t.Fatalf("kept %d findings, want 1", len(kept))
				}
				if kept[0] != tc.want {
					t.Fatalf("got  %+v\nwant %+v", kept[0], tc.want)
				}
			}
			if tc.wantNote != "" && !strings.Contains(strings.Join(notes, "\n"), tc.wantNote) {
				t.Fatalf("notes %q lack %q", notes, tc.wantNote)
			}
			if tc.wantNote == "" && tc.in.Body == tc.want.Body && len(notes) != 0 {
				t.Fatalf("unexpected notes %q", notes)
			}
		})
	}
}

func TestCheckFindingsKeepsOrder(t *testing.T) {
	files := workspace.ReviewFiles{Hunks: map[string][][2]int{"a.go": {{1, 9}}}}
	kept, _ := CheckFindings(Output{Findings: []OutFinding{
		{Path: "a.go", Line: 1, Body: "one", Category: "bug"},
		{Path: "a.md", Line: 1, Body: "two", Category: "design"},
		{Path: "a.go", Line: 2, Body: "three", Category: "bug"},
	}}, files)
	if len(kept) != 2 || kept[0].Body != "one" || kept[1].Body != "three" {
		t.Fatalf("kept %+v", kept)
	}
}
