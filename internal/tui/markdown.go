package tui

import (
	"fmt"
	"image/color"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"charm.land/glamour/v2"
	gansi "charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"github.com/alecthomas/chroma/v2"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/theme"
)

// ---- markdown ----

// markdownCache renders markdown with glamour and remembers the result. A
// glamour renderer is slow to build and the drawer redraws on every spinner
// frame, so both renderers and rendered text are kept. It is a pointer in
// the model because the model is copied on every update; only the
// Bubble Tea goroutine touches it.
type markdownCache struct {
	renderers map[mdKey]*glamour.TermRenderer
	text      map[mdText][]string
}

type mdKey struct {
	width int
	style string // "dark" or "light"
	plain bool
}

type mdText struct {
	mdKey
	src string
}

func newMarkdownCache() *markdownCache {
	return &markdownCache{renderers: map[mdKey]*glamour.TermRenderer{}, text: map[mdText][]string{}}
}

// render turns src, which must already be clean, into lines of at most
// width cells.
func (c *markdownCache) render(src string, width int, th theme.Theme) []string {
	width = max(width, 10)
	key := mdKey{width: width, style: th.Markdown(), plain: th.NoColor()}
	if lines, ok := c.text[mdText{key, src}]; ok {
		return lines
	}
	lines := c.renderUncached(src, key)
	c.text[mdText{key, src}] = lines
	return lines
}

func (c *markdownCache) renderUncached(src string, key mdKey) []string {
	plain := func() []string { return strings.Split(ansi.Wrap(src, key.width, ""), "\n") }
	r, ok := c.renderers[key]
	if !ok {
		var err error
		r, err = glamour.NewTermRenderer(glamour.WithStyles(glamourStyle(key)), glamour.WithWordWrap(key.width))
		if err != nil {
			return plain()
		}
		c.renderers[key] = r
	}
	out, err := r.Render(src)
	if err != nil {
		return plain()
	}
	// Glamour writes a link as an OSC 8 hyperlink around its URL, and has no
	// option to turn that off. Claude controls the text and the URL, so the
	// terminal must not get a clickable link; the URL stays visible as text.
	out = osc8.ReplaceAllString(out, "")
	lines := strings.Split(strings.Trim(out, "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return lines
}

// osc8 matches the start and end of an OSC 8 hyperlink.
var osc8 = regexp.MustCompile("\x1b\\]8;[^\x07\x1b]*(\x07|\x1b\\\\)")

// glamourStyle is the markdown style: glow's own dark or light style, with
// every color rewritten to an xterm-256 index. Glamour's styles color with
// hex values, which a 256-color terminal maps onto the basic 16, the one
// range snow's colors never use.
func glamourStyle(k mdKey) gansi.StyleConfig {
	s := styles.NoTTYStyleConfig
	if !k.plain {
		s = styles.DarkStyleConfig
		if k.style == "light" {
			s = styles.LightStyleConfig
		}
		s = indexColors(s)
		// Glamour registers Chroma styles under one shared name and keeps the
		// first it sees, so dark and light would collide, and Chroma reads
		// colors as hex, not as an index. A style registered here, by name,
		// avoids both.
		s.CodeBlock.Theme = chromaThemeName(k.style, s.CodeBlock.Chroma)
		s.CodeBlock.Chroma = nil
	}
	zero := uint(0)
	s.Document.BlockPrefix, s.Document.BlockSuffix, s.Document.Margin = "", "", &zero
	return s
}

var chromaMu sync.Mutex

// chromaThemeName registers c as a Chroma style, with each color snapped to
// an xterm index in 16-255, and returns its name. Chroma's 256-color
// formatter maps an exact palette color to its own index, so a color given as
// the hex of index n is printed as index n.
func chromaThemeName(style string, c *gansi.Chroma) string {
	name := "snow-" + style
	chromaMu.Lock()
	defer chromaMu.Unlock()
	if _, ok := chromastyles.Registry[name]; ok || c == nil {
		return name
	}
	hex := func(s string) string {
		r, g, b, _ := ansi.IndexedColor(mustAtoi(toIndex(s))).RGBA()
		return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
	}
	entry := func(p gansi.StylePrimitive) string {
		var parts []string
		if p.Color != nil {
			parts = append(parts, hex(*p.Color))
		}
		if p.BackgroundColor != nil {
			parts = append(parts, "bg:"+hex(*p.BackgroundColor))
		}
		for _, f := range []struct {
			on   *bool
			word string
		}{{p.Italic, "italic"}, {p.Bold, "bold"}, {p.Underline, "underline"}} {
			if f.on != nil && *f.on {
				parts = append(parts, f.word)
			}
		}
		return strings.Join(parts, " ")
	}
	chromastyles.Register(chroma.MustNewStyle(name, chroma.StyleEntries{
		chroma.Text:                entry(c.Text),
		chroma.Error:               entry(c.Error),
		chroma.Comment:             entry(c.Comment),
		chroma.CommentPreproc:      entry(c.CommentPreproc),
		chroma.Keyword:             entry(c.Keyword),
		chroma.KeywordReserved:     entry(c.KeywordReserved),
		chroma.KeywordNamespace:    entry(c.KeywordNamespace),
		chroma.KeywordType:         entry(c.KeywordType),
		chroma.Operator:            entry(c.Operator),
		chroma.Punctuation:         entry(c.Punctuation),
		chroma.Name:                entry(c.Name),
		chroma.NameBuiltin:         entry(c.NameBuiltin),
		chroma.NameTag:             entry(c.NameTag),
		chroma.NameAttribute:       entry(c.NameAttribute),
		chroma.NameClass:           entry(c.NameClass),
		chroma.NameConstant:        entry(c.NameConstant),
		chroma.NameDecorator:       entry(c.NameDecorator),
		chroma.NameException:       entry(c.NameException),
		chroma.NameFunction:        entry(c.NameFunction),
		chroma.NameOther:           entry(c.NameOther),
		chroma.Literal:             entry(c.Literal),
		chroma.LiteralNumber:       entry(c.LiteralNumber),
		chroma.LiteralDate:         entry(c.LiteralDate),
		chroma.LiteralString:       entry(c.LiteralString),
		chroma.LiteralStringEscape: entry(c.LiteralStringEscape),
		chroma.GenericDeleted:      entry(c.GenericDeleted),
		chroma.GenericEmph:         entry(c.GenericEmph),
		chroma.GenericInserted:     entry(c.GenericInserted),
		chroma.GenericStrong:       entry(c.GenericStrong),
		chroma.GenericSubheading:   entry(c.GenericSubheading),
		chroma.Background:          entry(c.Background),
	}))
	return name
}

func mustAtoi(s string) int { i, _ := strconv.Atoi(s); return i }

// indexColors returns a copy of s with every Color and BackgroundColor, the
// Chroma ones included, as an xterm index in 16-255. Glamour's configs share
// their *string fields with the package-level styles, so each new color is a
// fresh string and nothing is written through the old pointers.
func indexColors(s gansi.StyleConfig) gansi.StyleConfig {
	v := reflect.ValueOf(&s).Elem()
	convertColors(v)
	return s
}

func convertColors(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return
		}
		// Copy what the pointer targets before changing it, so the shared
		// original is left alone.
		cp := reflect.New(v.Type().Elem())
		cp.Elem().Set(v.Elem())
		v.Set(cp)
		convertColors(v.Elem())
	case reflect.Struct:
		for i := range v.NumField() {
			f := v.Type().Field(i)
			fv := v.Field(i)
			if (f.Name == "Color" || f.Name == "BackgroundColor") && fv.Kind() == reflect.Pointer && !fv.IsNil() {
				c := toIndex(fv.Elem().String())
				fv.Set(reflect.ValueOf(&c))
				continue
			}
			if f.IsExported() {
				convertColors(fv)
			}
		}
	}
}

// toIndex converts a glamour color, a hex value or an xterm index, to an
// index in 16-255.
func toIndex(c string) string {
	if i, err := strconv.Atoi(c); err == nil && i >= 16 && i <= 255 {
		return c
	}
	if len(c) == 7 && c[0] == '#' {
		if rgb, err := strconv.ParseUint(c[1:], 16, 32); err == nil {
			return strconv.Itoa(int(ansi.Convert256(color.RGBA{R: uint8(rgb >> 16), G: uint8(rgb >> 8), B: uint8(rgb), A: 255})))
		}
	}
	// An index below 16 or something unparsable: grey keeps it legible on
	// either background.
	return "244"
}
