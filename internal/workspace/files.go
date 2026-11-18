package workspace

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// FilesInput names the commits a review covers. LastReviewed and
// LastMergeBase are both empty for a full pass, or both set for a
// follow-up of the review at LastReviewed.
type FilesInput struct {
	MergeBase, Head             string
	LastReviewed, LastMergeBase string
}

// ReviewFiles are the files a reviewer reads, and what WriteReviewFiles
// learned from the PR's diff.
type ReviewFiles struct {
	Dir, Diff, Log, Since string   // absolute paths; Since "" for a full pass
	Changed               []string // paths changed merge-base..head
	// Hunks holds each changed path's hunks as new-file line ranges
	// [first, last], for anchoring comments. A path with no new-file lines
	// (deleted, binary, mode-only) has no entry.
	Hunks map[string][][2]int
}

var objectID = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// WriteReviewFiles writes diff.txt (the PR's diff, line-numbered), log.txt
// (its commits) and, for a follow-up, since.txt into dir, which must exist.
//
// since.txt holds only what the PR itself changed since the last review:
// the hunks of the PR's own patch that were not in its patch at the last
// review, so a merge of the base branch adds nothing. When the
// last reviewed commits are no longer in the repository (a force push
// dropped them), there is nothing to compare against and the result is a
// full pass.
func WriteReviewFiles(ctx context.Context, r Repo, dir string, in FilesInput) (ReviewFiles, error) {
	ids := []string{in.MergeBase, in.Head}
	followUp := in.LastReviewed != "" || in.LastMergeBase != ""
	if followUp {
		ids = append(ids, in.LastReviewed, in.LastMergeBase)
	}
	for _, id := range ids {
		if !objectID.MatchString(id) {
			return ReviewFiles{}, fmt.Errorf("invalid commit ID %q", id)
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ReviewFiles{}, err
	}
	files := ReviewFiles{Dir: abs, Hunks: map[string][][2]int{}}

	raw, err := r.diff(ctx, nil, in.MergeBase, in.Head)
	if err != nil {
		return ReviewFiles{}, err
	}
	numbered, prFiles, err := parseDiff(raw)
	if err != nil {
		return ReviewFiles{}, err
	}
	for p, f := range prFiles {
		if len(f.ranges) > 0 {
			files.Hunks[p] = f.ranges
		}
	}
	if files.Changed, err = r.changedPaths(ctx, in.MergeBase, in.Head); err != nil {
		return ReviewFiles{}, err
	}
	if files.Diff, err = writeFile(abs, "diff.txt", numbered); err != nil {
		return ReviewFiles{}, err
	}

	log, err := Git(ctx, r.GitDir, slices.Concat(noHooks, []string{
		"log", "--no-color", "--no-show-signature", "--format=%h %an %s", in.MergeBase + ".." + in.Head, "--",
	})...)
	if err != nil {
		return ReviewFiles{}, err
	}
	if files.Log, err = writeFile(abs, "log.txt", log); err != nil {
		return ReviewFiles{}, err
	}

	if !followUp || !r.hasCommit(ctx, in.LastReviewed) || !r.hasCommit(ctx, in.LastMergeBase) {
		return files, nil
	}
	since, err := r.since(ctx, in, files.Changed, prFiles)
	if err != nil {
		return ReviewFiles{}, err
	}
	if files.Since, err = writeFile(abs, "since.txt", since); err != nil {
		return ReviewFiles{}, err
	}
	return files, nil
}

// since builds since.txt from the PR's own patch, prFiles: for a path the
// last review saw, the hunks whose +/- lines were not a hunk of the patch
// then; for a path new to the PR, its whole patch; for a path the PR no
// longer touches, "# reverted: <path>". A base merge moves the PR's hunks
// but does not change their +/- lines, and its own changes are in neither
// patch, so it adds nothing.
func (r Repo) since(ctx context.Context, in FilesInput, changed []string, prFiles map[string]*fileDiff) ([]byte, error) {
	raw, err := r.diff(ctx, nil, in.LastMergeBase, in.LastReviewed)
	if err != nil {
		return nil, err
	}
	_, lastFiles, err := parseDiff(raw)
	if err != nil {
		return nil, err
	}
	lastChanged, err := r.changedPaths(ctx, in.LastMergeBase, in.LastReviewed)
	if err != nil {
		return nil, err
	}

	var out bytes.Buffer
	for _, p := range changed {
		now := prFiles[p]
		if now == nil {
			return nil, fmt.Errorf("changed path %q is missing from the diff", p)
		}
		reviewed := map[string]int{} // hunk key -> times seen last review
		if slices.Contains(lastChanged, p) {
			last := lastFiles[p]
			if last == nil {
				return nil, fmt.Errorf("changed path %q is missing from the last reviewed diff", p)
			}
			for _, s := range last.sections {
				for _, h := range s.hunks {
					reviewed[h.key()]++
				}
			}
		}
		for _, s := range now.sections {
			var kept []*hunk
			for _, h := range s.hunks {
				if k := h.key(); reviewed[k] > 0 {
					reviewed[k]--
					continue
				}
				kept = append(kept, h)
			}
			// A path new to the PR keeps a section with no hunks, such as a
			// new empty file.
			if len(kept) == 0 && slices.Contains(lastChanged, p) {
				continue
			}
			out.Write(s.header)
			for _, h := range kept {
				out.Write(h.text)
			}
		}
	}
	for _, p := range lastChanged {
		if !slices.Contains(changed, p) {
			fmt.Fprintf(&out, "# reverted: %s\n", quotePath(p))
		}
	}
	return out.Bytes(), nil
}

// diffFormat pins the settings in a user's git config that would change
// the diff's format, which the reviewer and parseDiff rely on. Context and
// algorithm match GitHub's diff, so Hunks are lines GitHub accepts
// comments on.
var diffFormat = []string{
	"-c", "diff.noprefix=false",
	"-c", "diff.mnemonicPrefix=false",
	"-c", "diff.suppressBlankEmpty=false",
	"-c", "core.quotePath=true",
	"--literal-pathspecs",
	"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", "--no-relative",
	"--unified=3", "--inter-hunk-context=0", "--diff-algorithm=myers",
}

// diff runs `git diff [extra] from to -- paths` with the format pinned.
func (r Repo) diff(ctx context.Context, extra []string, from, to string, paths ...string) ([]byte, error) {
	return Git(ctx, r.GitDir, slices.Concat(noHooks, diffFormat, extra, []string{from, to, "--"}, paths)...)
}

// changedPaths lists the paths that differ between from and to, sorted.
func (r Repo) changedPaths(ctx context.Context, from, to string) ([]string, error) {
	out, err := r.diff(ctx, []string{"--name-only", "-z"}, from, to)
	if err != nil {
		return nil, err
	}
	var paths []string
	for p := range strings.SplitSeq(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// hasCommit reports whether id is a commit in r.
func (r Repo) hasCommit(ctx context.Context, id string) bool {
	_, err := Git(ctx, r.GitDir, "cat-file", "-e", id+"^{commit}")
	return err == nil
}

// fileDiff is one path's part of a diff: one section, or two when the path
// changed type (a file became a symlink).
type fileDiff struct {
	sections []*section
	ranges   [][2]int // new-file line ranges of its hunks
}

// section is one `diff --git` block of a diff, numbered.
type section struct {
	header []byte // lines from "diff --git" to the first hunk
	hunks  []*hunk
}

// hunk is one "@@" block of a section, numbered, or the "Binary files …
// differ" line of a binary change.
type hunk struct {
	text []byte
	// changes are its +/- lines in order: the hunk without line numbers or
	// context. A binary change is its new blob ID.
	changes []string
}

func (h *hunk) key() string { return strings.Join(h.changes, "\n") }

// parseDiff prefixes every line of a file's new version in diff with its
// new-file line number, as "L<n> ", and splits the diff by path. Deleted
// lines and headers are unchanged.
func parseDiff(diff []byte) ([]byte, map[string]*fileDiff, error) {
	var out bytes.Buffer
	out.Grow(len(diff) + len(diff)/4)
	files := map[string]*fileDiff{}
	var (
		cur    *fileDiff
		sec    *section
		h      *hunk
		blob   string // new blob ID from the section's index line
		inHunk bool
		n      int
	)
	for line := range bytes.Lines(diff) {
		l := strings.TrimSuffix(string(line), "\n")
		prefix := ""
		switch {
		case strings.HasPrefix(l, "diff --git "):
			p, err := headerPath(l)
			if err != nil {
				return nil, nil, err
			}
			if cur = files[p]; cur == nil {
				cur = &fileDiff{}
				files[p] = cur
			}
			sec = &section{}
			cur.sections = append(cur.sections, sec)
			h, blob, inHunk = nil, "", false
		case strings.HasPrefix(l, "@@ "):
			if sec == nil {
				return nil, nil, fmt.Errorf("hunk before any file header: %q", l)
			}
			start, count, err := hunkNew(l)
			if err != nil {
				return nil, nil, err
			}
			if count > 0 {
				cur.ranges = append(cur.ranges, [2]int{start, start + count - 1})
			}
			h = &hunk{}
			sec.hunks = append(sec.hunks, h)
			inHunk, n = true, start
		case inHunk && (l == "" || l[0] == ' ' || l[0] == '+'):
			if l != "" && l[0] == '+' {
				h.changes = append(h.changes, l)
			}
			prefix = "L" + strconv.Itoa(n) + " "
			n++
		case inHunk && l[0] == '-':
			h.changes = append(h.changes, l)
		case inHunk && l[0] == '\\':
			// "\ No newline at end of file" belongs to the line before.
		case strings.HasPrefix(l, "index ") && sec != nil:
			inHunk = false
			if _, ids, ok := strings.Cut(l, ".."); ok {
				blob, _, _ = strings.Cut(ids, " ")
			}
		case strings.HasPrefix(l, "Binary files ") && sec != nil:
			inHunk = false
			h = &hunk{changes: []string{"binary " + blob}}
			sec.hunks = append(sec.hunks, h)
		default:
			inHunk = false
		}
		out.WriteString(prefix)
		out.Write(line)
		switch {
		case h != nil:
			h.text = append(append(h.text, prefix...), line...)
		case sec != nil:
			sec.header = append(append(sec.header, prefix...), line...)
		}
	}
	return out.Bytes(), files, nil
}

// headerPath returns the path in a `diff --git a/<p> b/<p>` header, which
// git quotes C-style when p has unusual characters. With renames off both
// halves name the same path, so the first half is the a/ side.
func headerPath(header string) (string, error) {
	rest := strings.TrimPrefix(header, "diff --git ")
	if len(rest)%2 == 0 {
		return "", fmt.Errorf("unexpected diff header %q", header)
	}
	a := rest[:len(rest)/2]
	if strings.HasPrefix(a, `"`) {
		uq, err := strconv.Unquote(a)
		if err != nil {
			return "", fmt.Errorf("unexpected diff header %q: %w", header, err)
		}
		a = uq
	}
	p, ok := strings.CutPrefix(a, "a/")
	if !ok || p == "" {
		return "", fmt.Errorf("unexpected diff header %q", header)
	}
	return p, nil
}

// hunkNew returns the new-file start and line count of a hunk header such
// as "@@ -12,7 +14,9 @@ func f()". An omitted count is 1.
func hunkNew(header string) (start, count int, err error) {
	bad := fmt.Errorf("unexpected hunk header %q", header)
	fields := strings.Fields(header)
	if len(fields) < 4 || !strings.HasPrefix(fields[2], "+") {
		return 0, 0, bad
	}
	s, c, hasCount := strings.Cut(fields[2][1:], ",")
	if start, err = strconv.Atoi(s); err != nil {
		return 0, 0, bad
	}
	count = 1
	if hasCount {
		if count, err = strconv.Atoi(c); err != nil {
			return 0, 0, bad
		}
	}
	return start, count, nil
}

// quotePath quotes p when it holds characters that would break a line of
// since.txt or be misread, as git does in diff headers.
func quotePath(p string) string {
	if !utf8.ValidString(p) || strings.ContainsFunc(p, func(c rune) bool {
		return c < 0x20 || c == 0x7f || c == '"' || c == '\\'
	}) {
		return strconv.Quote(p)
	}
	return p
}

func writeFile(dir, name string, data []byte) (string, error) {
	p := filepath.Join(dir, name)
	return p, os.WriteFile(p, data, 0o644)
}
