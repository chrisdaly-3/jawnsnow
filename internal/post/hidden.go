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

var parserFor = goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser()

// HiddenContent names what a rendered view hides or acts on: HTML comments,
// other raw HTML, images, link reference definitions (which render as
// nothing) and @mentions (which notify people). It reads goldmark's syntax
// tree, so code spans, fences and indented code are skipped by construction
// and nesting in lists or quotes cannot fool it.
func HiddenContent(md string) []string {
	src := []byte(md)
	pc := parser.NewContext()
	root := parserFor.Parse(text.NewReader(src), parser.WithContext(pc))

	var comment, html, image bool
	var logins []string
	seen := map[string]bool{}

	raw := func(b []byte) {
		if bytes.HasPrefix(bytes.TrimSpace(b), []byte("<!--")) {
			comment = true
		} else {
			html = true
		}
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
		case *ast.Image:
			image = true
		case *ast.Text:
			seg := n.Segment
			val := seg.Value(src)
			for _, loc := range mentionRe.FindAllSubmatchIndex(val, -1) {
				prev := byte(0)
				if loc[0] > 0 {
					prev = val[loc[0]-1]
				} else if seg.Start > 0 {
					prev = src[seg.Start-1]
				}
				if isWordByte(prev) {
					continue
				}
				login := strings.TrimRight(string(val[loc[2]:loc[3]]), "-./")
				if !seen[login] {
					seen[login] = true
					logins = append(logins, "@"+login)
				}
			}
		}
		return ast.WalkContinue, nil
	})

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
