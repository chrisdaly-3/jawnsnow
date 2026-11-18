package review

import (
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
		}, "read see above"},
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
			got := ReviewGaps("full", files, e.wt, run, out)
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

func TestGapsReportEveryMissingRule(t *testing.T) {
	e := newCovEnv(t, "pkg/a.go")
	got := ReviewGaps("full", e.files, e.wt, RunResult{}, Output{})
	if len(got) != 7 {
		t.Fatalf("want 7 gaps (diff, file, docs, search, tests, assumptions, understanding), got %d: %q", len(got), got)
	}
}

func TestGapsSinceCountsAsDiff(t *testing.T) {
	e := newCovEnv(t, "pkg/a.go")
	e.files.Since = filepath.Join(e.dir, "since.txt")
	run := e.completeRun()
	run.Reads[0] = e.files.Since
	if got := ReviewGaps("full", e.files, e.wt, run, completeOut()); len(got) != 0 {
		t.Fatalf("gaps = %q", got)
	}
}

func TestGapsPROnlyScope(t *testing.T) {
	e := newCovEnv(t, "pkg/a.go")
	if got := ReviewGaps("pr-only", e.files, e.wt, RunResult{Reads: []string{e.files.Diff}}, Output{}); len(got) != 0 {
		t.Fatalf("diff read: gaps = %q", got)
	}
	got := ReviewGaps("pr-only", e.files, e.wt, RunResult{}, Output{})
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
	got := ReviewGaps("full", e.files, e.wt, RunResult{}, Output{})
	if len(got) != 11 || !strings.HasPrefix(got[10], "and ") {
		t.Fatalf("got %d gaps: %q", len(got), got)
	}
}

func TestVerifiedExaminedFilters(t *testing.T) {
	e := newCovEnv(t, "pkg/a.go")
	ex := Examined{
		Docs:   []string{"README.md", "docs/guide.md"},
		Code:   []string{"pkg/a.go", e.abs("other/x.go")},
		Usages: []string{"./other/../other/x.go"},
		Tests:  []string{"pkg/a_test.go"},
	}
	run := RunResult{Reads: []string{
		e.abs("README.md"), e.abs("pkg/a.go"), e.abs("other/x.go"),
	}}
	got := VerifiedExamined(ex, run, e.wt)
	want := Examined{
		Docs:   []string{"README.md"},
		Code:   []string{"pkg/a.go", e.abs("other/x.go")},
		Usages: []string{"./other/../other/x.go"},
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
