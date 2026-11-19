package post

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// A login, optionally with a team: @org/team-name.
var mentionRe = regexp.MustCompile(`@([A-Za-z0-9][A-Za-z0-9-]*(?:/[A-Za-z0-9][A-Za-z0-9_.-]*)?)`)

// "@" spelled as an entity still notifies once rendered.
var atEntity = regexp.MustCompile(`&#0*64;|&#[xX]0*40;|&commat;`)

var htmlComment = regexp.MustCompile(`(?s)<!--.*?(?:-->|$)`)

var parserFor = goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser()

// HiddenContent names what a rendered view hides or acts on: character
// references that decode to control or invisible characters, HTML
// comments, other raw HTML, images, link reference definitions (which
// render as nothing) and @mentions (which notify people). It reads
// goldmark's syntax tree, so code spans, fences and indented code are
// skipped by construction and nesting in lists or quotes cannot fool it.
// References are the exception: the renderer decodes them inside code
// spans too, so only fenced and indented code, which it shows literally,
// is skipped for them.
func HiddenContent(md string) []string {
	src := []byte(md)
	pc := parser.NewContext()
	root := parserFor.Parse(text.NewReader(src), parser.WithContext(pc))

	var comment, html, image bool
	var literal []text.Segment // fenced and indented code lines
	var logins []string
	seen := map[string]bool{}

	addMentions := func(val []byte, prev byte) {
		val = atEntity.ReplaceAll(val, []byte("@"))
		for _, loc := range mentionRe.FindAllSubmatchIndex(val, -1) {
			p := prev
			if loc[0] > 0 {
				p = val[loc[0]-1]
			}
			if isWordByte(p) {
				continue
			}
			login := strings.TrimRight(string(val[loc[2]:loc[3]]), "-./")
			if !seen[login] {
				seen[login] = true
				logins = append(logins, "@"+login)
			}
		}
	}
	// HTML is scanned as raw source: over-warning there is fine.
	raw := func(b []byte) {
		rest := htmlComment.ReplaceAll(b, nil)
		if len(rest) < len(b) {
			comment = true
			html = html || bytes.Contains(rest, []byte("<"))
		} else {
			html = true
		}
		addMentions(b, 0)
	}
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.HTMLBlock:
			raw(n.Lines().Value(src))
		case *ast.RawHTML:
			raw(n.Segments.Value(src))
		case *ast.CodeSpan:
			return ast.WalkSkipChildren, nil
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			lines := n.Lines()
			for i := range lines.Len() {
				literal = append(literal, lines.At(i))
			}
			return ast.WalkSkipChildren, nil
		case *ast.Image:
			image = true
		case *ast.Text:
			// Only a word character in the same run of text blocks a mention:
			// after emphasis, a link or a line break an @ starts a word.
			var prev byte
			if t, ok := n.PreviousSibling().(*ast.Text); ok && !t.SoftLineBreak() && !t.HardLineBreak() {
				if v := t.Segment.Value(src); len(v) > 0 {
					prev = v[len(v)-1]
				}
			}
			addMentions(n.Segment.Value(src), prev)
		}
		return ast.WalkContinue, nil
	})

	var out []string
	if encodedControl(src, literal) {
		out = append(out, "encoded control character")
	}
	if comment {
		out = append(out, "HTML comment")
	}
	if html {
		out = append(out, "raw HTML")
	}
	if image {
		out = append(out, "image")
	}
	if len(pc.References()) > 0 {
		out = append(out, "link reference (renders as nothing)")
	}
	if len(logins) > 0 {
		out = append(out, "mentions "+strings.Join(logins, ", "))
	}
	return out
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// encodedControl reports whether src has a control reference outside the
// literal segments.
func encodedControl(src []byte, literal []text.Segment) bool {
	for _, loc := range charRef.FindAllIndex(src, -1) {
		if !controlRef(string(src[loc[0]:loc[1]])) {
			continue
		}
		inLiteral := false
		for _, seg := range literal {
			if loc[0] >= seg.Start && loc[1] <= seg.Stop {
				inLiteral = true
				break
			}
		}
		if !inLiteral {
			return true
		}
	}
	return false
}

// CodeBlockLines returns the byte ranges of md's fenced and indented code
// lines, which a renderer shows as written.
func CodeBlockLines(md string) [][2]int {
	src := []byte(md)
	root := parserFor.Parse(text.NewReader(src))
	var out [][2]int
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.(type) {
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			lines := n.Lines()
			for i := range lines.Len() {
				seg := lines.At(i)
				out = append(out, [2]int{seg.Start, seg.Stop})
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return out
}
