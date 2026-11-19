package post

import (
	"regexp"
	"strings"
)

var (
	// A tag needs a name followed by whitespace, ">" or "/>": "<https://x>"
	// is an autolink and "a < b" is prose.
	tagRe     = regexp.MustCompile(`</?[A-Za-z][A-Za-z0-9-]*(?:\s|/?>|$)`)
	mentionRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.+/-])@([A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)`)
	fenceRe   = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
)

// HiddenContent names what a rendered view hides or acts on: HTML comments,
// raw HTML, images and @mentions (which notify people). Fenced blocks and
// inline code spans are skipped. It is a scanner, not a markdown parser.
func HiddenContent(md string) []string {
	var comment, html, image bool
	var logins []string
	seen := map[string]bool{}

	var fence string // the opening run while inside a fenced block
	for _, line := range strings.Split(md, "\n") {
		if m := fenceRe.FindStringSubmatch(line); m != nil {
			run := m[1]
			if fence == "" {
				fence = run
				continue
			}
			if run[0] == fence[0] && len(run) >= len(fence) && strings.TrimSpace(line[strings.Index(line, run)+len(run):]) == "" {
				fence = ""
				continue
			}
		}
		if fence != "" {
			continue
		}
		text := stripCodeSpans(line)
		if strings.Contains(text, "<!--") {
			comment = true
		}
		if tagRe.MatchString(text) {
			html = true
		}
		if strings.Contains(text, "![") {
			image = true
		}
		for _, m := range mentionRe.FindAllStringSubmatch(text, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				logins = append(logins, "@"+m[1])
			}
		}
	}

	var out []string
	if comment {
		out = append(out, "HTML comment")
	}
	if html {
		out = append(out, "raw HTML")
	}
	if image {
		out = append(out, "image")
	}
	if len(logins) > 0 {
		out = append(out, "mentions "+strings.Join(logins, ", "))
	}
	return out
}

// stripCodeSpans blanks inline code: a run of N backticks closes at the next
// run of exactly N. An unmatched run is literal text.
func stripCodeSpans(line string) string {
	var b strings.Builder
	for i := 0; i < len(line); {
		if line[i] != '`' {
			b.WriteByte(line[i])
			i++
			continue
		}
		j := i
		for j < len(line) && line[j] == '`' {
			j++
		}
		n := j - i
		end := -1
		for k := j; k < len(line); {
			if line[k] != '`' {
				k++
				continue
			}
			s := k
			for k < len(line) && line[k] == '`' {
				k++
			}
			if k-s == n {
				end = k
				break
			}
		}
		if end < 0 {
			b.WriteString(line[i:j])
			i = j
			continue
		}
		b.WriteByte(' ')
		i = end
	}
	return b.String()
}
