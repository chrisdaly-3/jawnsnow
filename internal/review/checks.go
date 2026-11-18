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
var bannedPhrases = regexp.MustCompile(`(?i)\b(?:concrete|specifically|notably|essentially|crucially|it['’]s worth noting|security-relevant|in summary|LGTM|looks good|verified|I checked|confirmed at)\b,?[ \t]*`)

var (
	// codeSpan matches a fenced block (to its closing fence or the end) or
	// an inline code span. Banned phrases inside code are left alone.
	codeSpan = regexp.MustCompile("(?s)```.*?(?:```|$)|`[^`\n]*`")
	hexWord  = regexp.MustCompile(`(?i)\b[0-9a-f]{7,40}\b`)

	spaceBeforePunct = regexp.MustCompile(`[ \t]+([,.;:!?])`)
	commaBeforeEnd   = regexp.MustCompile(`,[ \t]*([,.;:!?])`)
	manySpaces       = regexp.MustCompile(`[ \t]{2,}`)
)

// CheckFindings applies Dispatch's checks to the reviewer's findings: it
// anchors each one to a changed hunk or marks it unanchored, strips banned
// phrases and commit SHAs, turns misplaced suggestion blocks into plain
// code blocks, and drops style-only comments on Markdown. Every kept finding
// is a new one. The notes are private lines for the drawer.
func CheckFindings(out Output, files workspace.ReviewFiles) (kept []store.Finding, notes []string) {
	var phrases, shas, demoted, docs int
	for _, f := range out.Findings {
		p := normalizePath(f.Path)
		if docPreference(p, f) {
			docs++
			continue
		}
		anchored, oneHunk := locate(files.Hunks[p], f.StartLine, f.Line)
		body, nPhrases, nSHAs := lintBody(f.Body)
		phrases += nPhrases
		shas += nSHAs
		if !(anchored && oneHunk) {
			var n int
			body, n = demoteSuggestions(body)
			demoted += n
		}
		kept = append(kept, store.Finding{
			Path: p, Line: f.Line, StartLine: f.StartLine,
			Category: f.Category, Blocking: f.Blocking, Body: body,
			Unanchored: !anchored, Ledger: "new",
		})
	}
	if docs > 0 {
		notes = append(notes, fmt.Sprintf("dropped %d style comment%s on docs", docs, plural(docs)))
	}
	if phrases > 0 {
		notes = append(notes, fmt.Sprintf("removed %d banned phrase%s", phrases, plural(phrases)))
	}
	if shas > 0 {
		notes = append(notes, fmt.Sprintf("replaced %d commit SHA%s", shas, plural(shas)))
	}
	if demoted > 0 {
		notes = append(notes, fmt.Sprintf("turned %d misplaced suggestion%s into plain code", demoted, plural(demoted)))
	}
	return kept, notes
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

// lintBody removes banned phrases (outside code) and replaces commit SHAs.
func lintBody(body string) (string, int, int) {
	var phrases, shas int
	var b strings.Builder
	last := 0
	for _, loc := range codeSpan.FindAllStringIndex(body, -1) {
		text, n := removePhrases(body[last:loc[0]], last == 0)
		phrases += n
		b.WriteString(text)
		b.WriteString(body[loc[0]:loc[1]])
		last = loc[1]
	}
	text, n := removePhrases(body[last:], last == 0)
	phrases += n
	b.WriteString(text)
	out := b.String()
	if phrases > 0 {
		out = strings.TrimSpace(out)
	}
	out = hexWord.ReplaceAllStringFunc(out, func(w string) string {
		if !isSHA(w) {
			return w
		}
		shas++
		return "<commit>"
	})
	return out, phrases, shas
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

// capMark stands where a removed phrase began a sentence, so the next
// letter can be capitalised once the tidying is done.
const capMark = "\x00"

// removePhrases drops banned phrases from a stretch of prose and tidies
// what they leave behind: doubled spaces, a comma before punctuation, and
// a sentence that now starts in lower case.
func removePhrases(s string, atStart bool) (string, int) {
	locs := bannedPhrases.FindAllStringIndex(s, -1)
	if len(locs) == 0 {
		return s, 0
	}
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		b.WriteString(s[last:loc[0]])
		if startsSentence(s[:loc[0]], atStart) {
			b.WriteString(capMark)
		}
		last = loc[1]
	}
	b.WriteString(s[last:])
	out := b.String()
	out = commaBeforeEnd.ReplaceAllString(out, "$1")
	out = spaceBeforePunct.ReplaceAllString(out, "$1")
	out = manySpaces.ReplaceAllString(out, " ")
	out = capitalizeMarked.ReplaceAllStringFunc(out, func(m string) string {
		return strings.ToUpper(strings.TrimPrefix(m, capMark))
	})
	return strings.ReplaceAll(out, capMark, ""), len(locs)
}

var capitalizeMarked = regexp.MustCompile(capMark + `\p{Ll}`)

// startsSentence reports whether prose before a match ends a sentence, or
// is empty at the start of the body.
func startsSentence(before string, atStart bool) bool {
	if strings.HasSuffix(before, "\n") {
		return true
	}
	t := strings.TrimRight(before, " \t")
	if t == "" {
		return atStart
	}
	return strings.HasSuffix(t, ".") || strings.HasSuffix(t, "!") || strings.HasSuffix(t, "?")
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
