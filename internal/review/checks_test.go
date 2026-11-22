package review

import (
	"slices"
	"strings"
	"testing"

	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/workspace"
)

func TestCheckFindings(t *testing.T) {
	t.Parallel()
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
		wantLint   []string
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
			name:     "banned phrase is reported, body unchanged",
			in:       OutFinding{Path: "a.go", Line: 12, Body: "Return the concrete type, not the interface.", Category: "bug"},
			want:     store.Finding{Path: "a.go", Line: 12, Body: "Return the concrete type, not the interface.", Category: "bug", Ledger: "new"},
			wantLint: []string{`finding 1: uses "concrete"`},
		},
		{
			name:     "banned word that carries meaning is not removed",
			in:       OutFinding{Path: "a.go", Line: 12, Body: "The signature is verified before use.", Category: "bug"},
			want:     store.Finding{Path: "a.go", Line: 12, Body: "The signature is verified before use.", Category: "bug", Ledger: "new"},
			wantLint: []string{`finding 1: uses "verified"`},
		},
		{
			name:     "curly apostrophe and case, several hits",
			in:       OutFinding{Path: "a.go", Line: 12, Body: "Bad, CRUCIALLY. It’s worth noting this. LGTM", Category: "bug"},
			want:     store.Finding{Path: "a.go", Line: 12, Body: "Bad, CRUCIALLY. It’s worth noting this. LGTM", Category: "bug", Ledger: "new"},
			wantLint: []string{`finding 1: uses "crucially"`, `finding 1: uses "it’s worth noting"`, `finding 1: uses "lgtm"`},
		},
		{
			name:     "quoted phrase in prose is a hit, text unchanged",
			in:       OutFinding{Path: "a.go", Line: 12, Body: `The message says "looks good" when it fails.`, Category: "bug"},
			want:     store.Finding{Path: "a.go", Line: 12, Body: `The message says "looks good" when it fails.`, Category: "bug", Ledger: "new"},
			wantLint: []string{`finding 1: uses "looks good"`},
		},
		{
			name: "banned word inside a larger word is not a hit",
			in:   OutFinding{Path: "a.go", Line: 12, Body: "Use concretely typed values.", Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 12, Body: "Use concretely typed values.", Category: "bug", Ledger: "new"},
		},
		{
			name: "banned word in inline code is not a hit",
			in:   OutFinding{Path: "a.go", Line: 12, Body: "Rename `verified` to ok.", Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 12, Body: "Rename `verified` to ok.", Category: "bug", Ledger: "new"},
		},
		{
			name: "banned word in a fence is not a hit",
			in:   OutFinding{Path: "a.go", Line: 12, Body: "Rename:\n```go\nverified := true\n```\n~~~\nlooks good\n~~~", Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 12, Body: "Rename:\n```go\nverified := true\n```\n~~~\nlooks good\n~~~", Category: "bug", Ledger: "new"},
		},
		{
			name: "banned word in an indented block is not a hit",
			in:   OutFinding{Path: "a.go", Line: 12, Body: "Rename:\n\n    verified := true\n", Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 12, Body: "Rename:\n\n    verified := true\n", Category: "bug", Ledger: "new"},
		},
		{
			name:     "prose commit SHA becomes `<commit>` in backticks",
			in:       OutFinding{Path: "a.go", Line: 12, Body: "Broke in 1a2b3c4 and in 0123456789abcdef0123456789abcdef01234567.", Category: "bug"},
			want:     store.Finding{Path: "a.go", Line: 12, Body: "Broke in `<commit>` and in `<commit>`.", Category: "bug", Ledger: "new"},
			wantNote: "replaced 2 commit SHAs",
		},
		{
			name: "SHA in inline code is kept",
			in:   OutFinding{Path: "a.go", Line: 12, Body: "Run `git show 1a2b3c4`.", Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 12, Body: "Run `git show 1a2b3c4`.", Category: "bug", Ledger: "new"},
		},
		{
			name: "SHA in a suggestion block is kept",
			in:   OutFinding{Path: "a.go", Line: 12, Body: "Fix:\n```suggestion\nconst rev = \"1a2b3c4\"\n```", Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 12, Body: "Fix:\n```suggestion\nconst rev = \"1a2b3c4\"\n```", Category: "bug", Ledger: "new"},
		},
		{
			name: "SHA in a ~~~ fence is kept",
			in:   OutFinding{Path: "a.go", Line: 12, Body: "Fix:\n~~~\nrev 1a2b3c4\n~~~", Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 12, Body: "Fix:\n~~~\nrev 1a2b3c4\n~~~", Category: "bug", Ledger: "new"},
		},
		{
			name: "SHA in an indented block is kept",
			in:   OutFinding{Path: "a.go", Line: 12, Body: "Fix:\n\n    rev 1a2b3c4\n", Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 12, Body: "Fix:\n\n    rev 1a2b3c4\n", Category: "bug", Ledger: "new"},
		},
		{
			name: "word after # is a colour, not a SHA",
			in:   OutFinding{Path: "a.go", Line: 12, Body: "Use #1a2b3c4d here.", Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 12, Body: "Use #1a2b3c4d here.", Category: "bug", Ledger: "new"},
		},
		{
			name: "English hex words and plain numbers are not SHAs",
			in:   OutFinding{Path: "a.go", Line: 12, Body: "Use deadbeef or 1234567 or abcdef, not 1a2b3c.", Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 12, Body: "Use deadbeef or 1234567 or abcdef, not 1a2b3c.", Category: "bug", Ledger: "new"},
		},
		{
			name: "StartLine after Line is stored as 0",
			in:   OutFinding{Path: "a.go", StartLine: 15, Line: 12, Body: "Odd.", Category: "bug"},
			want: store.Finding{Path: "a.go", Line: 12, Body: "Odd.", Category: "bug", Ledger: "new"},
		},
		{
			name: "suggestion kept inside one hunk",
			in:   OutFinding{Path: "a.go", StartLine: 11, Line: 13, Body: sugg, Category: "bug"},
			want: store.Finding{Path: "a.go", StartLine: 11, Line: 13, Body: sugg, Category: "bug", Ledger: "new"},
		},
		{
			name:     "suggestion on an unanchored finding becomes a plain block",
			in:       OutFinding{Path: "a.go", Line: 30, Body: sugg, Category: "bug"},
			want:     store.Finding{Path: "a.go", Line: 30, Body: strings.Replace(sugg, "```suggestion", "```", 1), Category: "bug", Unanchored: true, Ledger: "new"},
			wantNote: "misplaced suggestion",
		},
		{
			name:     "suggestion spanning two hunks becomes a plain block but stays anchored",
			in:       OutFinding{Path: "a.go", StartLine: 15, Line: 41, Body: sugg, Category: "bug"},
			want:     store.Finding{Path: "a.go", StartLine: 15, Line: 41, Body: strings.Replace(sugg, "```suggestion", "```", 1), Category: "bug", Ledger: "new"},
			wantNote: "misplaced suggestion",
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
			kept, notes, lint := CheckFindings(Output{Findings: []OutFinding{tc.in}}, files)
			if !slices.Equal(lint, tc.wantLint) {
				t.Fatalf("lint %q, want %q", lint, tc.wantLint)
			}
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
			if tc.wantNote == "" && len(notes) != 0 {
				t.Fatalf("unexpected notes %q", notes)
			}
		})
	}
}

func TestCheckFindingsKeepsOrder(t *testing.T) {
	t.Parallel()
	files := workspace.ReviewFiles{Hunks: map[string][][2]int{"a.go": {{1, 9}}}}
	kept, _, _ := CheckFindings(Output{Findings: []OutFinding{
		{Path: "a.go", Line: 1, Body: "one", Category: "bug"},
		{Path: "a.md", Line: 1, Body: "two", Category: "design"},
		{Path: "a.go", Line: 2, Body: "three", Category: "bug"},
	}}, files)
	if len(kept) != 2 || kept[0].Body != "one" || kept[1].Body != "three" {
		t.Fatalf("kept %+v", kept)
	}
}

func TestCheckFindingsLintIndexIsPositionInOutput(t *testing.T) {
	t.Parallel()
	files := workspace.ReviewFiles{Hunks: map[string][][2]int{"a.go": {{1, 9}}}}
	_, _, lint := CheckFindings(Output{Findings: []OutFinding{
		{Path: "a.md", Line: 1, Body: "dropped", Category: "design"},
		{Path: "a.go", Line: 1, Body: "Notably wrong.", Category: "bug"},
	}}, files)
	if want := []string{`finding 2: uses "notably"`}; !slices.Equal(lint, want) {
		t.Fatalf("lint %q, want %q", lint, want)
	}
}
