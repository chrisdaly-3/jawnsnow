package review

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/chrisdaly-3/jawnsnow/internal/workspace"
)

// maxGaps bounds how many gaps one review reports.
const maxGaps = 10

// dataExts are extensions of changed files that are fully visible in the
// diff and need no reading in full.
var dataExts = []string{
	".md", ".json", ".yaml", ".yml", ".toml", ".lock", ".sum", ".txt", ".csv",
	".svg", ".png", ".jpg", ".gif",
}

// dataFiles are lock files by name, including ones whose extension is
// not in dataExts.
var dataFiles = []string{"go.sum", "package-lock.json", ".terraform.lock.hcl"}

func isData(p string) bool {
	p = filepath.ToSlash(p)
	return slices.Contains(dataExts, strings.ToLower(path.Ext(p))) ||
		slices.Contains(dataFiles, path.Base(p)) ||
		strings.HasPrefix(p, "testdata/") || strings.Contains(p, "/testdata/")
}

func isTestFile(p string) bool {
	base := path.Base(filepath.ToSlash(p))
	return strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, "_test.py") ||
		strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py") ||
		strings.Contains(base, ".test.") || strings.Contains(base, ".spec.")
}

// maxPerFileChecks is the most changed code files a pass must each read. A
// larger PR is judged on the first ones, sorted, and its other checks.
const maxPerFileChecks = 40

// isGenerated reports whether a changed file is generated, vendored or an
// archive: nobody reviews it by reading it.
func isGenerated(p string) bool {
	p = filepath.ToSlash(p)
	base := path.Base(p)
	switch {
	case strings.HasSuffix(base, ".pb.go"), strings.HasSuffix(base, "_generated.go"),
		strings.HasPrefix(base, "zz_generated"), strings.HasSuffix(base, ".gen.go"):
		return true
	case strings.HasPrefix(p, "vendor/"), strings.Contains(p, "/vendor/"),
		strings.HasPrefix(p, "third_party/"), strings.Contains(p, "/third_party/"):
		return true
	}
	switch strings.ToLower(path.Ext(base)) {
	case ".zip", ".tar", ".gz", ".tgz":
		return true
	}
	return false
}

// ReviewGaps lists what the reviewer skipped of the reading it is required
// to do before judging, one short line per gap, fit to go into the
// continuation prompt, and a note when the per-file rule checked only some
// of the changed files. Scope "pr-only" requires only the diff. An
// incremental pass builds on the full review that read the docs and listed
// the assumptions, so reading the diff since then (or the whole diff)
// counts as reading each changed file. Paths from the reviewer are
// compared, never opened.
func ReviewGaps(pass PassKind, scope string, files workspace.ReviewFiles, worktree string, run RunResult, out Output) ([]string, string) {
	read := newReadSet(run.Reads, worktree)
	readDiff := read.has(files.Diff) || files.Since != "" && read.has(files.Since)
	var gaps []string
	if !readDiff {
		gaps = append(gaps, "read the diff file "+files.Diff)
	}
	if scope == "pr-only" {
		return capGaps(gaps), ""
	}
	full := pass != PassIncremental

	var code []string
	for _, f := range files.Changed {
		if isData(f) || isGenerated(f) {
			continue
		}
		if _, err := os.Stat(filepath.Join(worktree, f)); err != nil {
			continue // deleted by the PR
		}
		code = append(code, f)
	}
	var note string
	if full || !readDiff {
		checked := slices.Sorted(slices.Values(code))
		if len(checked) > maxPerFileChecks {
			note = fmt.Sprintf("(checked %d of %d changed files)", maxPerFileChecks, len(checked))
			checked = checked[:maxPerFileChecks]
		}
		for _, f := range checked {
			if !read.has(f) {
				gaps = append(gaps, "read the changed file "+f)
			}
		}
	}
	if full && len(files.Changed) > 0 && !readDocsFor(run.Reads, worktree, files.Changed) {
		gaps = append(gaps, "read a README, doc.go, docs/ or CLAUDE.md for the changed code")
	}
	if len(code) > 0 {
		if !searchedOutside(run.Searched, worktree, files.Dir, files.Changed) {
			gaps = append(gaps, "run a Grep or Glob outside the changed directories")
		}
		if !slices.ContainsFunc(run.Reads, isTestFile) {
			gaps = append(gaps, "read an existing test file")
		}
		if full && len(out.Assumptions) == 0 {
			gaps = append(gaps, "list assumptions, and read the code that shows each holds")
		}
		if strings.TrimSpace(out.Understanding) == "" {
			gaps = append(gaps, "fill in understanding")
		}
	}
	for i, a := range out.Assumptions {
		if g := evidenceGap(a.Evidence, i+1, read, files.Dir, run.Reads); g != "" {
			gaps = append(gaps, g)
		}
	}
	return capGaps(gaps), note
}

// evidenceGap is the gap for assumption n, or "" when its evidence is
// prose or any repository file it cites was read. Citing only the review
// directory's files (the diff) doesn't show the code outside the diff.
func evidenceGap(evidence string, n int, read readSet, reviewDir string, reads []string) string {
	paths := evidencePaths(evidence, read.worktree)
	var repo []string
	for _, p := range paths {
		if !within(read.abs(p), reviewDir) {
			repo = append(repo, p)
		}
	}
	switch {
	case len(paths) == 0:
		return ""
	case len(repo) == 0:
		return fmt.Sprintf("cite repository code as evidence for assumption %d", n)
	}
	for _, p := range repo {
		if read.has(p) {
			return ""
		}
		if !strings.Contains(p, "/") && slices.ContainsFunc(reads, func(r string) bool { return filepath.Base(r) == p }) {
			return ""
		}
	}
	return fmt.Sprintf("read %s, the evidence for assumption %d", repo[0], n)
}

func capGaps(gaps []string) []string {
	if len(gaps) > maxGaps {
		return append(gaps[:maxGaps:maxGaps], fmt.Sprintf("and %d more", len(gaps)-maxGaps))
	}
	return gaps
}

// readDocsFor reports whether a README, doc.go, CLAUDE.md or a file under a
// docs/ directory of the worktree was read whose component holds a changed
// file: it sits in the directory of a changed file or one above it.
func readDocsFor(reads []string, worktree string, changed []string) bool {
	for _, r := range reads {
		rel, err := filepath.Rel(worktree, r)
		if err != nil || !filepath.IsLocal(rel) {
			continue
		}
		dir, ok := docDir(filepath.ToSlash(rel))
		if !ok {
			continue
		}
		for _, c := range changed {
			if within(filepath.Join(worktree, filepath.Dir(c)), filepath.Join(worktree, dir)) {
				return true
			}
		}
	}
	return false
}

// docDir is the directory a documentation file documents, or false when rel
// (slash-separated, relative to the worktree) is not documentation.
func docDir(rel string) (string, bool) {
	base := strings.ToLower(path.Base(rel))
	if strings.HasPrefix(base, "readme") || base == "doc.go" || base == "claude.md" {
		return path.Dir(rel), true
	}
	parts := strings.Split(rel, "/")
	for i, p := range parts[:len(parts)-1] {
		if p == "docs" {
			return path.Join(parts[:i]...), true
		}
	}
	return "", false
}

// searchedOutside reports whether any Grep or Glob reached beyond the
// changed files' directories: the working directory (path ""), or a
// directory that is not a changed file's directory or below one. The review
// directory is the reviewer's own and doesn't count. A changed file at the
// worktree root has no directory of its own, so the root is outside.
func searchedOutside(searched []string, worktree, reviewDir string, changed []string) bool {
	var dirs []string
	for _, f := range changed {
		if d := filepath.Dir(f); d != "." {
			dirs = append(dirs, filepath.Join(worktree, d))
		}
	}
	for _, s := range searched {
		if s == "" {
			return true
		}
		p := s
		if !filepath.IsAbs(p) {
			p = filepath.Join(worktree, p)
		}
		p = filepath.Clean(p)
		if within(p, reviewDir) {
			continue
		}
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			continue
		}
		if !slices.ContainsFunc(dirs, func(d string) bool { return within(p, d) }) {
			return true
		}
	}
	return false
}

// within reports whether p is dir or inside it.
func within(p, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, p)
	return err == nil && filepath.IsLocal(rel)
}

// lineSuffix matches a line reference after a path: :N, :N-M, :N:M, #LN
// or #LN-LM.
var lineSuffix = regexp.MustCompile(`(?::\d+(?:[-:]\d+)?|#L\d+(?:-L?\d+)?)$`)

// cleanPathWord strips the quotes, brackets and punctuation around a word
// and any line reference after it.
func cleanPathWord(w string) string {
	const open, closing = "\"'`([{<", "\"'`)]}>,;:.!?"
	w = strings.TrimLeft(w, open)
	w = strings.TrimRight(w, closing)
	w = lineSuffix.ReplaceAllString(w, "")
	return strings.TrimRight(w, closing)
}

// evidencePaths are the files an assumption's evidence names, in order,
// without line references. A word is a path when it has a "/" and its last
// segment an extension, or when it names an existing file, relative to the
// worktree or absolute. URLs and prose give none.
func evidencePaths(evidence, worktree string) []string {
	var out []string
	for _, w := range strings.Fields(evidence) {
		if strings.Contains(w, "://") {
			continue
		}
		p := cleanPathWord(w)
		if p == "" {
			continue
		}
		if strings.Contains(p, "/") && path.Ext(path.Base(p)) != "" {
			out = append(out, p)
			continue
		}
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(worktree, p)
		}
		if fi, err := os.Stat(abs); err == nil && fi.Mode().IsRegular() {
			out = append(out, p)
		}
	}
	return out
}

// VerifiedExamined keeps only the examined paths that appear in run.Reads,
// ignoring a line reference after a path. The list is shown to the user, so
// it must not claim reads that didn't happen. Relative paths are joined to
// the worktree.
func VerifiedExamined(ex Examined, run RunResult, worktree string) Examined {
	read := newReadSet(run.Reads, worktree)
	keep := func(paths []string) []string {
		return slices.DeleteFunc(slices.Clone(paths), func(p string) bool { return !read.has(cleanPathWord(p)) })
	}
	return Examined{
		Docs:   keep(ex.Docs),
		Code:   keep(ex.Code),
		Usages: keep(ex.Usages),
		Tests:  keep(ex.Tests),
	}
}

// readSet holds the cleaned absolute paths a run read.
type readSet struct {
	worktree string
	paths    map[string]struct{}
}

func newReadSet(reads []string, worktree string) readSet {
	s := readSet{worktree: worktree, paths: make(map[string]struct{}, len(reads))}
	for _, p := range reads {
		s.paths[s.abs(p)] = struct{}{}
	}
	return s
}

func (s readSet) abs(p string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.worktree, p)
	}
	return filepath.Clean(p)
}

func (s readSet) has(p string) bool {
	_, ok := s.paths[s.abs(p)]
	return ok
}
