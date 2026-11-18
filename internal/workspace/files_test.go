package workspace

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func hasLine(s, line string) bool {
	return slices.Contains(strings.Split(s, "\n"), line)
}

func TestDiffIsLineNumbered(t *testing.T) {
	ctx := context.Background()
	h := newHistory(t)
	f := lines("line", 20)
	base := h.commit("base", map[string][]byte{"f.txt": text(f)})
	h.git("checkout", "-q", "-b", "pr")
	head := h.commit("pr change", map[string][]byte{
		"f.txt":   text(with(f, 12, "changed")),
		"bin.dat": {0, 1, 2, 0, 3},
	})

	dir := t.TempDir()
	files, err := WriteReviewFiles(ctx, h.repo(), dir, FilesInput{MergeBase: base, Head: head})
	if err != nil {
		t.Fatal(err)
	}
	if files.Dir != dir || files.Diff != filepath.Join(dir, "diff.txt") || files.Log != filepath.Join(dir, "log.txt") {
		t.Fatalf("paths: %+v", files)
	}
	if want := []string{"bin.dat", "f.txt"}; !reflect.DeepEqual(files.Changed, want) {
		t.Fatalf("Changed = %q, want %q", files.Changed, want)
	}

	diff := readFile(t, files.Diff)
	for _, want := range []string{
		"diff --git a/f.txt b/f.txt",
		"--- a/f.txt",
		"+++ b/f.txt",
		"@@ -9,7 +9,7 @@ line8",
		"L9  line9",
		"L11  line11",
		"-line12",
		"L12 +changed",
		"L13  line13",
		"L15  line15",
		"Binary files /dev/null and b/bin.dat differ",
	} {
		if !hasLine(diff, want) {
			t.Errorf("diff.txt has no line %q:\n%s", want, diff)
		}
	}

	log := readFile(t, files.Log)
	short := h.git("rev-parse", "--short", head)
	if log != short+" Test pr change\n" {
		t.Errorf("log.txt = %q", log)
	}

	for _, p := range []string{files.Diff, files.Log} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o644 {
			t.Errorf("%s mode %v, want 0644", p, st.Mode().Perm())
		}
	}
}

// A user's git config must not change the format the reviewer and the
// parser rely on.
func TestDiffIgnoresUserDiffConfig(t *testing.T) {
	ctx := context.Background()
	h := newHistory(t)
	f := lines("line", 20)
	base := h.commit("base", map[string][]byte{"f.txt": text(f)})
	h.git("checkout", "-q", "-b", "pr")
	head := h.commit("pr change", map[string][]byte{"f.txt": text(with(f, 12, "changed"))})

	cfg := filepath.Join(t.TempDir(), "gitconfig")
	write(t, cfg, "[diff]\n\tnoprefix = true\n\tmnemonicPrefix = true\n\texternal = /bin/false\n[color]\n\tui = always\n\tdiff = always\n")
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)

	files, err := WriteReviewFiles(ctx, h.repo(), t.TempDir(), FilesInput{MergeBase: base, Head: head})
	if err != nil {
		t.Fatal(err)
	}
	diff := readFile(t, files.Diff)
	if !hasLine(diff, "diff --git a/f.txt b/f.txt") || !hasLine(diff, "L12 +changed") {
		t.Fatalf("diff.txt:\n%s", diff)
	}
	if strings.Contains(diff, "\x1b[") {
		t.Fatalf("diff.txt has color codes:\n%q", diff)
	}
	if !reflect.DeepEqual(files.Hunks["f.txt"], [][2]int{{9, 15}}) {
		t.Fatalf("Hunks = %v", files.Hunks)
	}
}

func TestHunksRecordNewRanges(t *testing.T) {
	ctx := context.Background()
	h := newHistory(t)
	f := lines("f", 30)
	base := h.commit("base", map[string][]byte{
		"f.txt": text(f),
		"h.txt": []byte("gone\n"),
	})
	h.git("checkout", "-q", "-b", "pr")
	head := h.commit("pr", map[string][]byte{
		"f.txt":       text(with(with(f, 5, "five"), 25, "twenty-five")),
		"g.txt":       []byte("one\ntwo\n"),
		"h.txt":       nil,
		"dir/a b.txt": []byte("spaced\n"),
		"t\tab.txt":   []byte("tabbed\n"),
		"é.txt":       []byte("accent\n"),
		"q\"uote.txt": []byte("quoted\n"),
	})

	files, err := WriteReviewFiles(ctx, h.repo(), t.TempDir(), FilesInput{MergeBase: base, Head: head})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][][2]int{
		"f.txt":       {{2, 8}, {22, 28}},
		"g.txt":       {{1, 2}},
		"dir/a b.txt": {{1, 1}},
		"t\tab.txt":   {{1, 1}},
		"é.txt":       {{1, 1}},
		"q\"uote.txt": {{1, 1}},
	}
	if !reflect.DeepEqual(files.Hunks, want) {
		t.Fatalf("Hunks = %v\nwant    %v", files.Hunks, want)
	}
	wantChanged := []string{"dir/a b.txt", "f.txt", "g.txt", "h.txt", "q\"uote.txt", "t\tab.txt", "é.txt"}
	if !reflect.DeepEqual(files.Changed, wantChanged) {
		t.Fatalf("Changed = %q\nwant      %q", files.Changed, wantChanged)
	}
}

// prScenario is a PR reviewed once: main has f.txt, g.txt and x.txt, and
// the PR's commit A changes line 30 of f.txt and line 5 of g.txt.
type prScenario struct {
	h       history
	base, a string
	f, g, x []string
}

func newPRScenario(t *testing.T) prScenario {
	t.Helper()
	s := prScenario{h: newHistory(t), f: lines("f", 40), g: lines("g", 20), x: lines("x", 40)}
	s.base = s.h.commit("base", map[string][]byte{
		"f.txt": text(s.f), "g.txt": text(s.g), "x.txt": text(s.x),
	})
	s.h.git("checkout", "-q", "-b", "pr")
	s.f = with(s.f, 30, "pr-f30")
	s.g = with(s.g, 5, "pr-g5")
	s.a = s.h.commit("pr A", map[string][]byte{"f.txt": text(s.f), "g.txt": text(s.g)})
	return s
}

// mergeMain commits an unrelated change to main (three lines added at the
// top of f.txt, line 2 of x.txt changed, a new u.txt) and merges main into
// the PR. It returns the new head.
func (s *prScenario) mergeMain(t *testing.T) string {
	t.Helper()
	s.h.git("checkout", "-q", "main")
	top := []string{"top1\n", "top2\n", "top3\n"}
	s.h.commit("main change", map[string][]byte{
		"f.txt": text(append(top, lines("f", 40)...)),
		"x.txt": text(with(lines("x", 40), 2, "main-x2")),
		"u.txt": []byte("unrelated\n"),
	})
	s.h.git("checkout", "-q", "pr")
	s.h.git("merge", "-q", "--no-ff", "-m", "merge main", "main")
	s.f = append(top, s.f...)
	s.x = with(s.x, 2, "main-x2")
	return s.h.git("rev-parse", "HEAD")
}

// since writes the review files for a follow-up of review A at head and
// returns since.txt.
func (s prScenario) since(t *testing.T, head string) (ReviewFiles, string) {
	t.Helper()
	ctx := context.Background()
	mb, err := s.h.repo().MergeBase(ctx, "main", head)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files, err := WriteReviewFiles(ctx, s.h.repo(), dir, FilesInput{
		MergeBase: mb, Head: head, LastReviewed: s.a, LastMergeBase: s.base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if files.Since != filepath.Join(dir, "since.txt") {
		t.Fatalf("Since = %q", files.Since)
	}
	return files, readFile(t, files.Since)
}

func TestSinceExcludesBaseMerge(t *testing.T) {
	s := newPRScenario(t)
	head := s.mergeMain(t)

	files, since := s.since(t, head)
	if since != "" {
		t.Fatalf("since.txt = %q, want empty", since)
	}
	if want := []string{"f.txt", "g.txt"}; !reflect.DeepEqual(files.Changed, want) {
		t.Fatalf("Changed = %q, want %q", files.Changed, want)
	}
	// The PR's hunk in f.txt moved down three lines with the merge.
	if !reflect.DeepEqual(files.Hunks["f.txt"], [][2]int{{30, 36}}) {
		t.Fatalf("Hunks = %v", files.Hunks)
	}
}

func TestSinceIncludesOwnChange(t *testing.T) {
	s := newPRScenario(t)
	s.mergeMain(t)
	s.g = with(s.g, 15, "pr-g15")
	head := s.h.commit("pr B", map[string][]byte{"g.txt": text(s.g)})

	_, since := s.since(t, head)
	for _, want := range []string{"diff --git a/g.txt b/g.txt", "-g15", "L15 +pr-g15"} {
		if !hasLine(since, want) {
			t.Errorf("since.txt has no line %q:\n%s", want, since)
		}
	}
	for _, not := range []string{"f.txt", "x.txt", "u.txt", "pr-g5"} {
		if strings.Contains(since, not) {
			t.Errorf("since.txt mentions %q:\n%s", not, since)
		}
	}
}

// A path the PR first touches after the last review is shown with the
// PR's whole change to it, and without what the base merge did to it.
func TestSinceIncludesNewPathInFull(t *testing.T) {
	s := newPRScenario(t)
	s.mergeMain(t)
	s.x = with(s.x, 35, "pr-x35")
	head := s.h.commit("pr B", map[string][]byte{"x.txt": text(s.x)})

	_, since := s.since(t, head)
	for _, want := range []string{"diff --git a/x.txt b/x.txt", "-x35", "L35 +pr-x35"} {
		if !hasLine(since, want) {
			t.Errorf("since.txt has no line %q:\n%s", want, since)
		}
	}
	for _, not := range []string{"main-x2", "f.txt", "g.txt"} {
		if strings.Contains(since, not) {
			t.Errorf("since.txt mentions %q:\n%s", not, since)
		}
	}
}

func TestSinceMarksReverted(t *testing.T) {
	s := newPRScenario(t)
	head := s.h.commit("revert g", map[string][]byte{"g.txt": text(lines("g", 20))})

	_, since := s.since(t, head)
	if since != "# reverted: g.txt\n" {
		t.Fatalf("since.txt = %q", since)
	}
}

func TestFullPassHasNoSince(t *testing.T) {
	s := newPRScenario(t)
	dir := t.TempDir()
	files, err := WriteReviewFiles(context.Background(), s.h.repo(), dir, FilesInput{MergeBase: s.base, Head: s.a})
	if err != nil {
		t.Fatal(err)
	}
	if files.Since != "" {
		t.Fatalf("Since = %q", files.Since)
	}
	if _, err := os.Stat(filepath.Join(dir, "since.txt")); !os.IsNotExist(err) {
		t.Fatalf("since.txt exists: %v", err)
	}
}

// A force push can drop the last reviewed commit from the clone. There is
// then no since diff to write, and the review is a full pass.
func TestSinceMissingLastReviewedIsFullPass(t *testing.T) {
	s := newPRScenario(t)
	missing := strings.Repeat("ab", 20)
	files, err := WriteReviewFiles(context.Background(), s.h.repo(), t.TempDir(), FilesInput{
		MergeBase: s.base, Head: s.a, LastReviewed: missing, LastMergeBase: s.base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if files.Since != "" {
		t.Fatalf("Since = %q", files.Since)
	}
}

func TestWriteReviewFilesRejectsBadIDs(t *testing.T) {
	s := newPRScenario(t)
	for _, in := range []FilesInput{
		{MergeBase: "--output=/tmp/x", Head: s.a},
		{MergeBase: s.base, Head: "main"},
		{MergeBase: s.base, Head: s.a, LastReviewed: "-p", LastMergeBase: s.base},
		{MergeBase: s.base, Head: s.a, LastReviewed: s.a},
	} {
		if _, err := WriteReviewFiles(context.Background(), s.h.repo(), t.TempDir(), in); err == nil {
			t.Errorf("%+v: no error", in)
		}
	}
}

// A base merge that changes a file the PR also edits adds none of its
// hunks, and hunks already reviewed are not shown again.
func TestSinceExcludesBaseMergeInEditedFile(t *testing.T) {
	ctx := context.Background()
	h := newHistory(t)
	f := lines("f", 50)
	base := h.commit("base", map[string][]byte{"f.txt": text(f)})
	h.git("checkout", "-q", "-b", "pr")
	pr := with(f, 3, "pr-f3")
	a := h.commit("pr A", map[string][]byte{"f.txt": text(pr)})
	pr = with(pr, 20, "pr-f20")
	h.commit("pr B", map[string][]byte{"f.txt": text(pr)})
	h.git("checkout", "-q", "main")
	h.commit("main change", map[string][]byte{"f.txt": text(with(f, 40, "main-f40"))})
	h.git("checkout", "-q", "pr")
	h.git("merge", "-q", "--no-ff", "-m", "merge main", "main")
	head := h.git("rev-parse", "HEAD")
	mb, err := h.repo().MergeBase(ctx, "main", head)
	if err != nil {
		t.Fatal(err)
	}

	files, err := WriteReviewFiles(ctx, h.repo(), t.TempDir(), FilesInput{
		MergeBase: mb, Head: head, LastReviewed: a, LastMergeBase: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	since := readFile(t, files.Since)
	for _, want := range []string{
		"diff --git a/f.txt b/f.txt", "--- a/f.txt", "+++ b/f.txt",
		"@@ -17,7 +17,7 @@ f16", "L17  f17", "-f20", "L20 +pr-f20", "L23  f23",
	} {
		if !hasLine(since, want) {
			t.Errorf("since.txt has no line %q:\n%s", want, since)
		}
	}
	for _, not := range []string{"pr-f3", "f40", "main-f40"} {
		if strings.Contains(since, not) {
			t.Errorf("since.txt mentions %q:\n%s", not, since)
		}
	}
	if n := strings.Count(since, "\n@@ "); n != 1 {
		t.Errorf("since.txt has %d hunks, want 1:\n%s", n, since)
	}
	// The numbers are Head's.
	if got := strings.Split(h.git("show", head+":f.txt"), "\n")[19]; got != "pr-f20" {
		t.Fatalf("line 20 of f.txt at head = %q", got)
	}
}

func TestNoNewlineAtEOF(t *testing.T) {
	h := newHistory(t)
	base := h.commit("base", map[string][]byte{"f.txt": []byte("a\nb\n")})
	h.git("checkout", "-q", "-b", "pr")
	head := h.commit("pr", map[string][]byte{"f.txt": []byte("a\nb\nc")})

	files, err := WriteReviewFiles(context.Background(), h.repo(), t.TempDir(), FilesInput{MergeBase: base, Head: head})
	if err != nil {
		t.Fatal(err)
	}
	diff := readFile(t, files.Diff)
	if !strings.HasSuffix(diff, "@@ -1,2 +1,3 @@\nL1  a\nL2  b\nL3 +c\n\\ No newline at end of file\n") {
		t.Fatalf("diff.txt:\n%s", diff)
	}
	if !reflect.DeepEqual(files.Hunks, map[string][][2]int{"f.txt": {{1, 3}}}) {
		t.Fatalf("Hunks = %v", files.Hunks)
	}
}

func TestEditAtFirstLine(t *testing.T) {
	h := newHistory(t)
	f := lines("f", 10)
	base := h.commit("base", map[string][]byte{"f.txt": text(f)})
	h.git("checkout", "-q", "-b", "pr")
	head := h.commit("pr", map[string][]byte{"f.txt": text(with(f, 1, "first"))})

	files, err := WriteReviewFiles(context.Background(), h.repo(), t.TempDir(), FilesInput{MergeBase: base, Head: head})
	if err != nil {
		t.Fatal(err)
	}
	diff := readFile(t, files.Diff)
	for _, want := range []string{"@@ -1,4 +1,4 @@", "-f1", "L1 +first", "L4  f4"} {
		if !hasLine(diff, want) {
			t.Errorf("diff.txt has no line %q:\n%s", want, diff)
		}
	}
	if !reflect.DeepEqual(files.Hunks, map[string][][2]int{"f.txt": {{1, 4}}}) {
		t.Fatalf("Hunks = %v", files.Hunks)
	}
}
