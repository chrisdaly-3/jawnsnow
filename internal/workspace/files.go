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
// since.txt holds only what the PR itself changed since the last review: a
// path is in it only when the PR's own patch for it differs from the patch
// at the last review, so a merge of the base branch adds nothing. When the
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
		if len(f.hunks) > 0 {
			files.Hunks[p] = f.hunks
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

// since builds since.txt. Paths whose own patch changed are shown as their
// diff from the last reviewed commit; paths new to the PR as the PR's
// whole patch; paths the PR no longer touches as "# reverted: <path>".
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

	var edited, added []string
	for _, p := range changed {
		if !slices.Contains(lastChanged, p) {
			added = append(added, p)
			continue
		}
		last, now := lastFiles[p], prFiles[p]
		// A path the parser did not find is shown rather than guessed equal.
		if last == nil || now == nil || !slices.Equal(last.changes, now.changes) {
			edited = append(edited, p)
		}
	}

	var out bytes.Buffer
	for _, group := range []struct {
		from  string
		paths []string
	}{{in.LastReviewed, edited}, {in.MergeBase, added}} {
		if len(group.paths) == 0 {
			continue
		}
		raw, err := r.diff(ctx, nil, group.from, in.Head, group.paths...)
		if err != nil {
			return nil, err
		}
		numbered, _, err := parseDiff(raw)
		if err != nil {
			return nil, err
		}
		out.Write(numbered)
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

// fileDiff is one path's part of a diff.
type fileDiff struct {
	// changes are the +/- lines of its hunks, in order: the patch without
	// line numbers or context, which a base merge leaves alone. A binary
	// change is its new blob ID.
	changes []string
	hunks   [][2]int // new-file line ranges
}

// parseDiff prefixes every line of a file's new version in diff with its
// new-file line number, as "L<n> ", and splits the diff by path. Deleted
// lines and headers are unchanged.
func parseDiff(diff []byte) ([]byte, map[string]*fileDiff, error) {
	var out bytes.Buffer
	out.Grow(len(diff) + len(diff)/4)
	files := map[string]*fileDiff{}
	var cur *fileDiff
	var blob string // new blob ID from the current path's index line
	inHunk, n := false, 0
	for line := range bytes.Lines(diff) {
		l := strings.TrimSuffix(string(line), "\n")
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
			inHunk, blob = false, ""
		case strings.HasPrefix(l, "@@ "):
			if cur == nil {
				return nil, nil, fmt.Errorf("hunk before any file header: %q", l)
			}
			start, count, err := hunkNew(l)
			if err != nil {
				return nil, nil, err
			}
			if count > 0 {
				cur.hunks = append(cur.hunks, [2]int{start, start + count - 1})
			}
			inHunk, n = true, start
		case inHunk && (l == "" || l[0] == ' ' || l[0] == '+'):
			if l != "" && l[0] == '+' {
				cur.changes = append(cur.changes, l)
			}
			fmt.Fprintf(&out, "L%d ", n)
			n++
		case inHunk && l[0] == '-':
			cur.changes = append(cur.changes, l)
		case inHunk && l[0] == '\\':
			// "\ No newline at end of file" belongs to the line before.
		case strings.HasPrefix(l, "index ") && cur != nil:
			inHunk = false
			if _, ids, ok := strings.Cut(l, ".."); ok {
				blob, _, _ = strings.Cut(ids, " ")
			}
		case strings.HasPrefix(l, "Binary files ") && cur != nil:
			inHunk = false
			cur.changes = append(cur.changes, "binary "+blob)
		default:
			inHunk = false
		}
		out.Write(line)
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
