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

// parserFor models GitHub, which has no definition lists, so HiddenContent
// warns about what GitHub will show.
var parserFor = goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser()

// glamourParser parses as glamour's NewTermRenderer does: GFM, definition
// lists and heading IDs. CodeBlockLines must see the same block structure
// as the renderer, or text glamour renders (and decodes) could be taken for
// a code block it shows as written. Keep it in step with glamour.
var glamourParser = goldmark.New(
	goldmark.WithExtensions(extension.GFM, extension.DefinitionList),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
).Parser()

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
	s := &hiddenScan{src: src, seen: map[string]bool{}}
	_ = ast.Walk(root, s.visit)

	var out []string
	if encodedControl(src, s.literal) {
		out = append(out, "encoded control character")
	}
	if s.comment {
		out = append(out, "HTML comment")
	}
	if s.html {
		out = append(out, "raw HTML")
	}
	if s.image {
		out = append(out, "image")
	}
	if len(pc.References()) > 0 {
		out = append(out, "link reference (renders as nothing)")
	}
	if len(s.logins) > 0 {
		out = append(out, "mentions "+strings.Join(s.logins, ", "))
	}
	return out
}

// hiddenScan is what HiddenContent finds while it walks the syntax tree.
type hiddenScan struct {
	src                  []byte
	comment, html, image bool
	literal              []text.Segment // fenced and indented code lines
	logins               []string       // each mention once, in order
	seen                 map[string]bool
}

// visit is the ast.Walk callback: it records each node that hides or acts
// on something, and skips the children of code.
func (s *hiddenScan) visit(n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	switch n := n.(type) {
	case *ast.HTMLBlock:
		s.raw(n.Lines().Value(s.src))
	case *ast.RawHTML:
		s.raw(n.Segments.Value(s.src))
	case *ast.CodeSpan:
		return ast.WalkSkipChildren, nil
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		lines := n.Lines()
		for i := range lines.Len() {
			s.literal = append(s.literal, lines.At(i))
		}
		return ast.WalkSkipChildren, nil
	case *ast.Image:
		s.image = true
	case *ast.Text:
		s.addMentions(n.Segment.Value(s.src), s.prevByte(n))
	}
	return ast.WalkContinue, nil
}

// prevByte is the last byte of the text run just before n, or 0. Only a word
// character in the same run of text blocks a mention: after emphasis, a link
// or a line break an @ starts a word.
func (s *hiddenScan) prevByte(n *ast.Text) byte {
	t, ok := n.PreviousSibling().(*ast.Text)
	if !ok || t.SoftLineBreak() || t.HardLineBreak() {
		return 0
	}
	if v := t.Segment.Value(s.src); len(v) > 0 {
		return v[len(v)-1]
	}
	return 0
}

// raw records an HTML comment or other raw HTML in b, and any mentions in
// it. HTML is scanned as raw source: over-warning there is fine.
func (s *hiddenScan) raw(b []byte) {
	rest := htmlComment.ReplaceAll(b, nil)
	if len(rest) < len(b) {
		s.comment = true
		s.html = s.html || bytes.Contains(rest, []byte("<"))
	} else {
		s.html = true
	}
	s.addMentions(b, 0)
}

// addMentions records each @login in val not preceded by a word character;
// prev is the byte before val.
func (s *hiddenScan) addMentions(val []byte, prev byte) {
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
		if !s.seen[login] {
			s.seen[login] = true
			s.logins = append(s.logins, "@"+login)
		}
	}
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
// lines as glamour parses them, in order; glamour shows these as written.
func CodeBlockLines(md string) [][2]int {
	src := []byte(md)
	root := glamourParser.Parse(text.NewReader(src))
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
