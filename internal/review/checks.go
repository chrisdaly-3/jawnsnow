package review

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/chrisdaly-3/jawnsnow/internal/store"
	"github.com/chrisdaly-3/jawnsnow/internal/workspace"
)

// bannedPhrases is the list in prompt/reviewer.md, as regexp. The
// apostrophe in "it's" may be straight or curly.
var bannedPhrases = regexp.MustCompile(`(?i)\b(?:concrete|specifically|notably|essentially|crucially|it['’]s worth noting|security-relevant|in summary|LGTM|looks good|verified|I checked|confirmed at)\b`)

var (
	inlineCode = regexp.MustCompile("``[^\n]*?``|`[^`\n]*`")
	fenceOpen  = regexp.MustCompile("^ {0,3}(```+|~~~+)")
	hexWord    = regexp.MustCompile(`(?i)\b[0-9a-f]{7,40}\b`)
)

// CheckFindings applies Dispatch's checks to the reviewer's findings: it
// anchors each one to a changed hunk or marks it unanchored, replaces commit
// SHAs in prose, turns misplaced suggestion blocks into plain code blocks,
// and drops style-only comments on Markdown. Every kept finding is a new
// one. The notes are private lines for the drawer.
//
// A check never rewrites the meaning of a body. Banned phrases are only
// reported: each hit becomes a lint line, `finding <n>: uses "<phrase>"`,
// with n the 1-based index in out.Findings, for the caller to send back to
// the reviewer to reword. Code is never inspected for them.
func CheckFindings(out Output, files workspace.ReviewFiles) (kept []store.Finding, notes []string, lint []string) {
	var shas, demoted, docs int
	for i, f := range out.Findings {
		p := normalizePath(f.Path)
		if docPreference(p, f) {
			docs++
			continue
		}
		anchored, oneHunk := locate(files.Hunks[p], f.StartLine, f.Line)
		body := f.Body
		mapProse(body, func(prose string) string {
			for _, m := range bannedPhrases.FindAllString(prose, -1) {
				lint = append(lint, fmt.Sprintf("finding %d: uses %q", i+1, strings.ToLower(m)))
			}
			return prose
		})
		body = mapProse(body, func(prose string) string {
			n, out := replaceSHAs(prose)
			shas += n
			return out
		})
		if !(anchored && oneHunk) {
			var n int
			body, n = demoteSuggestions(body)
			demoted += n
		}
		start := f.StartLine
		if start > f.Line {
			start = 0
		}
		kept = append(kept, store.Finding{
			Path: p, Line: f.Line, StartLine: start,
			Category: f.Category, Blocking: f.Blocking, Body: body,
			Unanchored: !anchored, Ledger: "new",
		})
	}
	if docs > 0 {
		notes = append(notes, fmt.Sprintf("dropped %d style comment%s on docs", docs, plural(docs)))
	}
	if shas > 0 {
		notes = append(notes, fmt.Sprintf("replaced %d commit SHA%s", shas, plural(shas)))
	}
	if demoted > 0 {
		notes = append(notes, fmt.Sprintf("turned %d misplaced suggestion%s into plain code", demoted, plural(demoted)))
	}
	return kept, notes, lint
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// normalizePath makes a reviewer's path comparable with the keys of
// ReviewFiles.Hunks: forward slashes, no "./" prefix, no "a/../b" detours.
func normalizePath(p string) string {
	p = strings.TrimSpace(filepath.ToSlash(p))
	if p == "" {
		return p
	}
	return path.Clean(p)
}

// locate reports whether a finding's lines sit in the changed hunks. It is
// anchored when both ends of start..line are inside some hunk, and oneHunk
// when they are inside the same one. A start of 0, or after line, means a
// single line.
func locate(hunks [][2]int, start, line int) (anchored, oneHunk bool) {
	if start <= 0 || start > line {
		start = line
	}
	if line < 1 {
		return false, false
	}
	in := func(n int) int {
		for i, h := range hunks {
			if n >= h[0] && n <= h[1] {
				return i
			}
		}
		return -1
	}
	a, b := in(start), in(line)
	return a >= 0 && b >= 0, a >= 0 && a == b
}

var markdownExts = map[string]bool{".md": true, ".markdown": true, ".mdx": true}

// docPreference reports whether f is the reviewer's taste in prose: a
// non-blocking design comment on a Markdown file that names no broken link
// or reference.
func docPreference(p string, f OutFinding) bool {
	if f.Blocking || f.Category != "design" || !markdownExts[strings.ToLower(path.Ext(p))] {
		return false
	}
	return !namesBrokenReference(f.Body)
}

var brokenRef = regexp.MustCompile(`(?i)\b(?:broken|dead|dangling|stale|missing)\s+(?:\w+\s+)?(?:link|reference|anchor|xref)s?\b|\b(?:link|reference|anchor)s?\b[^.\n]{0,40}\b(?:is|are)\s+(?:broken|dead|wrong|missing)\b`)

func namesBrokenReference(body string) bool { return brokenRef.MatchString(body) }

// mapProse applies fn to the prose of a Markdown body and leaves code
// alone: fenced blocks (``` and ~~~, which includes ```suggestion), indented
// code blocks and inline code spans.
func mapProse(body string, fn func(string) string) string {
	lines := strings.Split(body, "\n")
	var fence string // the opening fence's run while inside a fenced block
	prevBlank, prevIndented := true, false
	for i, l := range lines {
		indented := false
		switch {
		case fence != "":
			t := strings.TrimLeft(l, " ")
			if len(l)-len(t) <= 3 && strings.HasPrefix(t, fence) && strings.Trim(t, fence[:1]) == "" {
				fence = ""
			}
		case fenceOpen.MatchString(l):
			fence = fenceOpen.FindStringSubmatch(l)[1]
		case (strings.HasPrefix(l, "    ") || strings.HasPrefix(l, "\t")) && (prevBlank || prevIndented):
			indented = true
		default:
			lines[i] = mapInline(l, fn)
		}
		prevBlank = strings.TrimSpace(l) == ""
		prevIndented = indented
	}
	return strings.Join(lines, "\n")
}

// mapInline applies fn to the parts of a line outside inline code spans.
func mapInline(line string, fn func(string) string) string {
	var b strings.Builder
	last := 0
	for _, loc := range inlineCode.FindAllStringIndex(line, -1) {
		b.WriteString(fn(line[last:loc[0]]))
		b.WriteString(line[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(fn(line[last:]))
	return b.String()
}

// replaceSHAs replaces each commit ID in prose with `<commit>`, in
// backticks because GitHub would swallow a bare <commit> as an HTML tag. A
// word right after "#" is a colour or an issue, not a commit.
func replaceSHAs(s string) (int, string) {
	var b strings.Builder
	n, last := 0, 0
	for _, loc := range hexWord.FindAllStringIndex(s, -1) {
		if !isSHA(s[loc[0]:loc[1]]) || (loc[0] > 0 && s[loc[0]-1] == '#') {
			continue
		}
		b.WriteString(s[last:loc[0]])
		b.WriteString("`<commit>`")
		last = loc[1]
		n++
	}
	b.WriteString(s[last:])
	return n, b.String()
}

// isSHA reports whether w, a whole word of 7 to 40 hex characters, looks
// like a commit ID: it holds both a digit and a letter, so "deadbeef" and
// "1234567" are left alone.
func isSHA(w string) bool {
	var digit, letter bool
	for _, r := range w {
		if unicode.IsDigit(r) {
			digit = true
		} else {
			letter = true
		}
	}
	return digit && letter
}

// demoteSuggestions turns each ```suggestion fence into a plain fence.
func demoteSuggestions(body string) (string, int) {
	lines := strings.Split(body, "\n")
	n := 0
	for i, l := range lines {
		t := strings.TrimLeft(l, " \t")
		if strings.HasPrefix(t, "```suggestion") {
			indent := l[:len(l)-len(t)]
			lines[i] = indent + "```" + strings.TrimPrefix(t, "```suggestion")
			n++
		}
	}
	return strings.Join(lines, "\n"), n
}
