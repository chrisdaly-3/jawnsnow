package review

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
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

// ReviewGaps lists what the reviewer skipped of the reading it is required
// to do before judging, one short line per gap, fit to go into the
// continuation prompt. Scope "pr-only" requires only the diff. Paths from
// the reviewer are compared, never opened.
func ReviewGaps(scope string, files workspace.ReviewFiles, worktree string, run RunResult, out Output) []string {
	read := newReadSet(run.Reads, worktree)
	var gaps []string
	if !read.has(files.Diff) && (files.Since == "" || !read.has(files.Since)) {
		gaps = append(gaps, "read the diff file "+files.Diff)
	}
	if scope == "pr-only" {
		return capGaps(gaps)
	}

	var code []string
	for _, f := range files.Changed {
		if isData(f) {
			continue
		}
		if _, err := os.Stat(filepath.Join(worktree, f)); err != nil {
			continue // deleted by the PR
		}
		code = append(code, f)
	}
	for _, f := range code {
		if !read.has(f) {
			gaps = append(gaps, "read the changed file "+f)
		}
	}
	if len(files.Changed) > 0 && !readDocsFor(run.Reads, worktree, files.Changed) {
		gaps = append(gaps, "read a README, doc.go, docs/ or CLAUDE.md for the changed code")
	}
	if len(code) > 0 {
		if !searchedOutside(run.Searched, worktree, files.Dir, files.Changed) {
			gaps = append(gaps, "run a Grep or Glob outside the changed directories")
		}
		if !slices.ContainsFunc(run.Reads, isTestFile) {
			gaps = append(gaps, "read an existing test file")
		}
		if len(out.Assumptions) == 0 {
			gaps = append(gaps, "list assumptions, and read the code that shows each holds")
		}
		if strings.TrimSpace(out.Understanding) == "" {
			gaps = append(gaps, "fill in understanding")
		}
	}
	for i, a := range out.Assumptions {
		p := evidencePath(a.Evidence)
		switch {
		case p == "" || within(read.abs(p), files.Dir):
			gaps = append(gaps, fmt.Sprintf("cite repository code as evidence for assumption %d", i+1))
		case !read.has(p):
			gaps = append(gaps, fmt.Sprintf("read %s, the evidence for assumption %d", p, i+1))
		}
	}
	return capGaps(gaps)
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

// evidencePath is the file an assumption's evidence names, without its
// backticks or :line / :start-end suffix.
func evidencePath(evidence string) string {
	p := strings.Trim(strings.TrimSpace(evidence), "`")
	if i := strings.LastIndexByte(p, ':'); i > 0 && isLineSpec(p[i+1:]) {
		p = p[:i]
	}
	return p
}

func isLineSpec(s string) bool {
	from, to, isRange := strings.Cut(s, "-")
	if _, err := strconv.Atoi(from); err != nil {
		return false
	}
	if isRange {
		_, err := strconv.Atoi(to)
		return err == nil
	}
	return true
}

// VerifiedExamined keeps only the examined paths that appear in run.Reads.
// The list is shown to the user, so it must not claim reads that didn't
// happen. Relative paths are joined to the worktree.
func VerifiedExamined(ex Examined, run RunResult, worktree string) Examined {
	read := newReadSet(run.Reads, worktree)
	keep := func(paths []string) []string {
		return slices.DeleteFunc(slices.Clone(paths), func(p string) bool { return !read.has(p) })
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
