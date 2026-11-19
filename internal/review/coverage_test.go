package review

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/workspace"
)

// covEnv is a worktree with one changed Go package and a review dir.
type covEnv struct {
	wt, dir string
	files   workspace.ReviewFiles
}

func newCovEnv(t *testing.T, changed ...string) covEnv {
	t.Helper()
	root := t.TempDir()
	wt := filepath.Join(root, "wt")
	dir := filepath.Join(root, "review")
	for _, f := range append(slices.Clone(changed), "pkg/a_test.go", "pkg/ev.go", "README.md", "pkg/README.md", "docs/guide.md", "other/x.go") {
		p := filepath.Join(wt, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return covEnv{wt: wt, dir: dir, files: workspace.ReviewFiles{
		Dir: dir, Diff: filepath.Join(dir, "diff.txt"), Changed: changed,
	}}
}

func (e covEnv) abs(p string) string { return filepath.Join(e.wt, p) }

// touch creates the files in the worktree and returns their paths.
func (e covEnv) touch(paths ...string) []string {
	for _, f := range paths {
		p := e.abs(f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			panic(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			panic(err)
		}
	}
	return paths
}

// completeRun reads everything a full review needs for pkg/a.go.
func (e covEnv) completeRun() RunResult {
	return RunResult{
		Reads:    []string{e.files.Diff, e.abs("pkg/a.go"), e.abs("pkg/README.md"), e.abs("pkg/a_test.go"), e.abs("pkg/ev.go")},
		Searched: []string{e.wt},
	}
}

func completeOut() Output {
	return Output{
		Understanding: "it does a thing",
		Assumptions:   []store.Assumption{{Claim: "c", Evidence: "pkg/ev.go:3"}},
	}
}

func TestGapsFullScope(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(e covEnv, run *RunResult, out *Output, files *workspace.ReviewFiles)
		want   string // substring of the single expected gap; "" for none
	}{
		{"complete", func(covEnv, *RunResult, *Output, *workspace.ReviewFiles) {}, ""},
		{"diff not read", func(e covEnv, r *RunResult, _ *Output, _ *workspace.ReviewFiles) {
			r.Reads = slices.DeleteFunc(r.Reads, func(p string) bool { return p == e.files.Diff })
		}, "read the diff file " + "DIFF"},
		{"changed code file not read", func(e covEnv, r *RunResult, _ *Output, _ *workspace.ReviewFiles) {
			r.Reads = slices.DeleteFunc(r.Reads, func(p string) bool { return p == e.abs("pkg/a.go") })
		}, "read the changed file pkg/a.go"},
		{"data file need not be read", func(e covEnv, _ *RunResult, _ *Output, f *workspace.ReviewFiles) {
			f.Changed = append(f.Changed, "go.sum", "testdata/in.txt", "notes.md", "pkg/data.json")
		}, ""},
		{"deleted changed file is not required", func(_ covEnv, _ *RunResult, _ *Output, f *workspace.ReviewFiles) {
			f.Changed = append(f.Changed, "pkg/gone.go")
		}, ""},
		{"no docs read", func(e covEnv, r *RunResult, _ *Output, _ *workspace.ReviewFiles) {
			r.Reads = slices.DeleteFunc(r.Reads, func(p string) bool { return p == e.abs("pkg/README.md") })
		}, "read a README, doc.go, docs/ or CLAUDE.md"},
		{"docs of another component do not count", func(e covEnv, r *RunResult, _ *Output, _ *workspace.ReviewFiles) {
			r.Reads = slices.DeleteFunc(r.Reads, func(p string) bool { return p == e.abs("pkg/README.md") })
			r.Reads = append(r.Reads, e.abs("other/README.md"))
		}, "read a README, doc.go, docs/ or CLAUDE.md"},
		{"root README counts", func(e covEnv, r *RunResult, _ *Output, _ *workspace.ReviewFiles) {
			r.Reads = slices.DeleteFunc(r.Reads, func(p string) bool { return p == e.abs("pkg/README.md") })
			r.Reads = append(r.Reads, e.abs("README.md"))
		}, ""},
		{"docs dir counts", func(e covEnv, r *RunResult, _ *Output, _ *workspace.ReviewFiles) {
			r.Reads = slices.DeleteFunc(r.Reads, func(p string) bool { return p == e.abs("pkg/README.md") })
			r.Reads = append(r.Reads, e.abs("docs/guide.md"))
		}, ""},
		{"review dir README is not repo docs", func(e covEnv, r *RunResult, _ *Output, _ *workspace.ReviewFiles) {
			r.Reads = slices.DeleteFunc(r.Reads, func(p string) bool { return p == e.abs("pkg/README.md") })
			r.Reads = append(r.Reads, filepath.Join(e.dir, "README.md"))
		}, "read a README, doc.go, docs/ or CLAUDE.md"},
		{"no search at all", func(_ covEnv, r *RunResult, _ *Output, _ *workspace.ReviewFiles) {
			r.Searched = nil
		}, "Grep or Glob outside"},
		{"search of working directory counts", func(_ covEnv, r *RunResult, _ *Output, _ *workspace.ReviewFiles) {
			r.Searched = []string{""}
		}, ""},
		{"search inside the changed directory", func(e covEnv, r *RunResult, _ *Output, _ *workspace.ReviewFiles) {
			r.Searched = []string{e.abs("pkg")}
		}, "Grep or Glob outside"},
		{"search below the changed directory", func(e covEnv, r *RunResult, _ *Output, _ *workspace.ReviewFiles) {
			r.Searched = []string{e.abs("pkg/sub")}
		}, "Grep or Glob outside"},
		{"search of a sibling directory counts", func(e covEnv, r *RunResult, _ *Output, _ *workspace.ReviewFiles) {
			r.Searched = []string{e.abs("other")}
		}, ""},
		{"relative search of a sibling counts", func(_ covEnv, r *RunResult, _ *Output, _ *workspace.ReviewFiles) {
			r.Searched = []string{"other"}
		}, ""},
		{"no test read", func(e covEnv, r *RunResult, _ *Output, _ *workspace.ReviewFiles) {
			r.Reads = slices.DeleteFunc(r.Reads, func(p string) bool { return p == e.abs("pkg/a_test.go") })
		}, "read an existing test file"},
		{"no assumptions", func(_ covEnv, _ *RunResult, o *Output, _ *workspace.ReviewFiles) {
			o.Assumptions = nil
		}, "list assumptions"},
		{"assumption file not read", func(e covEnv, r *RunResult, o *Output, _ *workspace.ReviewFiles) {
			o.Assumptions = append(o.Assumptions, store.Assumption{Claim: "c2", Evidence: "`other/x.go:10-20`"})
		}, "read other/x.go"},
		{"assumption file read via absolute path", func(e covEnv, r *RunResult, o *Output, _ *workspace.ReviewFiles) {
			o.Assumptions = append(o.Assumptions, store.Assumption{Claim: "c2", Evidence: e.abs("other/x.go") + ":4"})
			r.Reads = append(r.Reads, e.abs("other/x.go"))
		}, ""},
		{"assumption cites nothing", func(_ covEnv, _ *RunResult, o *Output, _ *workspace.ReviewFiles) {
			o.Assumptions = append(o.Assumptions, store.Assumption{Claim: "c2", Evidence: "see above"})
		}, ""},
		{"assumption cites the same file twice", func(e covEnv, r *RunResult, o *Output, _ *workspace.ReviewFiles) {
			o.Assumptions = append(o.Assumptions, store.Assumption{Claim: "c2", Evidence: "other/x.go:1074, other/x.go"})
			r.Reads = append(r.Reads, e.abs("other/x.go"))
		}, ""},
		{"assumption cites two files, one read", func(e covEnv, r *RunResult, o *Output, _ *workspace.ReviewFiles) {
			o.Assumptions = append(o.Assumptions, store.Assumption{Claim: "c2", Evidence: "other/x.go:10 and pkg/ev.go:20"})
		}, ""},
		{"assumption cites two files, neither read", func(e covEnv, r *RunResult, o *Output, _ *workspace.ReviewFiles) {
			o.Assumptions = append(o.Assumptions, store.Assumption{Claim: "c2", Evidence: "other/x.go:10 and other/y.go:20"})
		}, "read other/x.go, the evidence for assumption 2"},
		{"slashed prose is not a path", func(_ covEnv, _ *RunResult, o *Output, _ *workspace.ReviewFiles) {
			o.Assumptions = append(o.Assumptions, store.Assumption{Claim: "c2", Evidence: "and/or read/write"})
		}, ""},
		{"assumption cites only the diff", func(e covEnv, _ *RunResult, o *Output, _ *workspace.ReviewFiles) {
			o.Assumptions = append(o.Assumptions, store.Assumption{Claim: "c2", Evidence: e.files.Diff + ":12"})
		}, "cite repository code as evidence for assumption 2"},
		{"bare name that does not exist is prose", func(e covEnv, r *RunResult, o *Output, _ *workspace.ReviewFiles) {
			o.Assumptions = append(o.Assumptions, store.Assumption{Claim: "c2", Evidence: "x.go"})
			r.Reads = append(r.Reads, e.abs("other/x.go"))
		}, ""},
		{"bare name matches a read by basename", func(e covEnv, r *RunResult, o *Output, _ *workspace.ReviewFiles) {
			e.touch("x.go")
			o.Assumptions = append(o.Assumptions, store.Assumption{Claim: "c2", Evidence: "x.go:3"})
			r.Reads = append(r.Reads, e.abs("other/x.go"))
		}, ""},
		{"bare name not read", func(e covEnv, r *RunResult, o *Output, _ *workspace.ReviewFiles) {
			e.touch("x.go")
			o.Assumptions = append(o.Assumptions, store.Assumption{Claim: "c2", Evidence: "x.go:3"})
		}, "read x.go, the evidence for assumption 2"},
		{"go.mod is code", func(e covEnv, _ *RunResult, _ *Output, f *workspace.ReviewFiles) {
			f.Changed = append(f.Changed, e.touch("go.mod")...)
		}, "read the changed file go.mod"},
		{"generated and vendored files need not be read", func(e covEnv, _ *RunResult, _ *Output, f *workspace.ReviewFiles) {
			f.Changed = append(f.Changed, e.touch("pkg/api.pb.go", "pkg/zz_generated.deepcopy.go", "pkg/x_generated.go",
				"pkg/y.gen.go", "vendor/m/m.go", "third_party/t/t.go", "pkg/vendor/v.go", "dist/a.tgz", "Cargo.lock")...)
		}, ""},
		{"empty understanding", func(_ covEnv, _ *RunResult, o *Output, _ *workspace.ReviewFiles) {
			o.Understanding = "  \n"
		}, "fill in understanding"},
		{"no code changed needs no code checks", func(e covEnv, r *RunResult, o *Output, f *workspace.ReviewFiles) {
			f.Changed = []string{"notes.md", "go.sum"}
			r.Reads = []string{e.files.Diff, e.abs("README.md")}
			r.Searched = nil
			*o = Output{}
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newCovEnv(t, "pkg/a.go")
			run, out, files := e.completeRun(), completeOut(), e.files
			tt.mutate(e, &run, &out, &files)
			got, _ := ReviewGaps(PassFull, "full", files, e.wt, run, out)
			want := strings.ReplaceAll(tt.want, "DIFF", e.files.Diff)
			switch {
			case want == "" && len(got) != 0:
				t.Fatalf("gaps = %q, want none", got)
			case want != "" && (len(got) != 1 || !strings.Contains(got[0], want)):
				t.Fatalf("gaps = %q, want one containing %q", got, want)
			}
		})
	}
}

func TestEvidencePaths(t *testing.T) {
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "x.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		ev   string
		want []string
	}{
		{"a/x.go:1074, a/x.go", []string{"a/x.go", "a/x.go"}},
		{"a/x.go:10 and b/y.go:20", []string{"a/x.go", "b/y.go"}},
		{"`a/x.go:10-20`", []string{"a/x.go"}},
		{"a/x.go#L10", []string{"a/x.go"}},
		{"a/x.go#L10-L20", []string{"a/x.go"}},
		{"a/x.go:10:5", []string{"a/x.go"}},
		{"(a/x.go line 12).", []string{"a/x.go"}},
		{`"a/x.go": the caller`, []string{"a/x.go"}},
		{"see a/x.go.", []string{"a/x.go"}},
		{"x.go:3 does it", []string{"x.go"}},
		{"see above", nil},
		{"and/or read/write", nil},
		{"https://github.com/o/n/blob/main/a/x.go", nil},
		{"y.go is not here", nil},
	}
	for _, tt := range tests {
		if got := evidencePaths(tt.ev, wt); !slices.Equal(got, tt.want) {
			t.Errorf("evidencePaths(%q) = %q, want %q", tt.ev, got, tt.want)
		}
	}
}

func TestGapsIncrementalPass(t *testing.T) {
	e := newCovEnv(t, "pkg/a.go")
	e.files.Since = filepath.Join(e.dir, "since.txt")
	run := RunResult{Reads: []string{e.files.Since}}
	gaps, _ := ReviewGaps(PassIncremental, "full", e.files, e.wt, run, Output{Understanding: "u"})
	for _, g := range gaps {
		if strings.Contains(g, "changed file") || strings.Contains(g, "README") || strings.Contains(g, "list assumptions") {
			t.Errorf("incremental pass reading since.txt has gap %q", g)
		}
	}
	// The diff, understanding and evidence rules still apply.
	out := Output{Assumptions: []store.Assumption{{Claim: "c", Evidence: "other/x.go:3"}}}
	gaps, _ = ReviewGaps(PassIncremental, "full", e.files, e.wt, RunResult{}, out)
	for _, want := range []string{"read the diff file", "fill in understanding", "read other/x.go, the evidence for assumption 1"} {
		if !slices.ContainsFunc(gaps, func(g string) bool { return strings.Contains(g, want) }) {
			t.Errorf("incremental gaps %q lack %q", gaps, want)
		}
	}
	// diff.txt satisfies the per-file rule too.
	gaps, _ = ReviewGaps(PassIncremental, "full", e.files, e.wt, RunResult{Reads: []string{e.files.Diff}}, Output{Understanding: "u"})
	if slices.ContainsFunc(gaps, func(g string) bool { return strings.Contains(g, "changed file") }) {
		t.Errorf("incremental pass reading diff.txt has gaps %q", gaps)
	}
}

func TestGapsCapPerFileChecks(t *testing.T) {
	changed := make([]string, 0, 45)
	for i := range 45 {
		changed = append(changed, fmt.Sprintf("pkg/f%02d.go", i))
	}
	slices.Reverse(changed)
	e := newCovEnv(t, changed...)
	run := e.completeRun()
	for _, f := range changed[5:] { // read pkg/f00.go..f39.go, the first 40 sorted
		run.Reads = append(run.Reads, e.abs(f))
	}
	gaps, note := ReviewGaps(PassFull, "full", e.files, e.wt, run, completeOut())
	if len(gaps) != 0 {
		t.Errorf("gaps = %q, want none: only the first 40 changed files are checked", gaps)
	}
	if note != "(checked 40 of 45 changed files)" {
		t.Errorf("note = %q", note)
	}
	small := newCovEnv(t, "pkg/a.go")
	if _, note := ReviewGaps(PassFull, "full", small.files, small.wt, small.completeRun(), completeOut()); note != "" {
		t.Errorf("uncapped note = %q, want none", note)
	}
}

func TestGapsReportEveryMissingRule(t *testing.T) {
	e := newCovEnv(t, "pkg/a.go")
	got, _ := ReviewGaps(PassFull, "full", e.files, e.wt, RunResult{}, Output{})
	if len(got) != 7 {
		t.Fatalf("want 7 gaps (diff, file, docs, search, tests, assumptions, understanding), got %d: %q", len(got), got)
	}
}

func TestGapsSinceCountsAsDiff(t *testing.T) {
	e := newCovEnv(t, "pkg/a.go")
	e.files.Since = filepath.Join(e.dir, "since.txt")
	run := e.completeRun()
	run.Reads[0] = e.files.Since
	if got, _ := ReviewGaps(PassFull, "full", e.files, e.wt, run, completeOut()); len(got) != 0 {
		t.Fatalf("gaps = %q", got)
	}
}

func TestGapsPROnlyScope(t *testing.T) {
	e := newCovEnv(t, "pkg/a.go")
	if got, _ := ReviewGaps(PassFull, "pr-only", e.files, e.wt, RunResult{Reads: []string{e.files.Diff}}, Output{}); len(got) != 0 {
		t.Fatalf("diff read: gaps = %q", got)
	}
	got, _ := ReviewGaps(PassFull, "pr-only", e.files, e.wt, RunResult{}, Output{})
	if len(got) != 1 || !strings.Contains(got[0], "read the diff file "+e.files.Diff) {
		t.Fatalf("gaps = %q", got)
	}
}

func TestGapsAreCapped(t *testing.T) {
	changed := make([]string, 0, 30)
	for i := range 30 {
		changed = append(changed, "pkg/f"+string(rune('a'+i%26))+string(rune('a'+i/26))+".go")
	}
	e := newCovEnv(t, changed...)
	got, _ := ReviewGaps(PassFull, "full", e.files, e.wt, RunResult{}, Output{})
	if len(got) != 11 || !strings.HasPrefix(got[10], "and ") {
		t.Fatalf("got %d gaps: %q", len(got), got)
	}
}

func TestVerifiedExaminedFilters(t *testing.T) {
	e := newCovEnv(t, "pkg/a.go")
	ex := Examined{
		Docs:   []string{"README.md", "docs/guide.md"},
		Code:   []string{"pkg/a.go:12", e.abs("other/x.go")},
		Usages: []string{"./other/../other/x.go", "`other/x.go#L4-L9`"},
		Tests:  []string{"pkg/a_test.go"},
	}
	run := RunResult{Reads: []string{
		e.abs("README.md"), e.abs("pkg/a.go"), e.abs("other/x.go"),
	}}
	got := VerifiedExamined(ex, run, e.wt)
	want := Examined{
		Docs:   []string{"README.md"},
		Code:   []string{"pkg/a.go:12", e.abs("other/x.go")},
		Usages: []string{"./other/../other/x.go", "`other/x.go#L4-L9`"},
		Tests:  []string{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("VerifiedExamined =\n%+v\nwant\n%+v", got, want)
	}
}

func TestVerifiedExaminedKeepsReviewDirFiles(t *testing.T) {
	e := newCovEnv(t, "pkg/a.go")
	got := VerifiedExamined(Examined{Docs: []string{e.files.Diff}}, RunResult{Reads: []string{e.files.Diff}}, e.wt)
	if len(got.Docs) != 1 {
		t.Fatalf("got %+v", got)
	}
}
