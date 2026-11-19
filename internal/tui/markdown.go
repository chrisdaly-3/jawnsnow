package tui

import (
	"fmt"
	"image/color"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"charm.land/glamour/v2"
	gansi "charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"github.com/alecthomas/chroma/v2"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/post"
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

// The cache's bounds. A full cache is cleared rather than evicted from:
// the drawer and the preview re-render what they show on the next frame.
const (
	mdTextMax      = 512
	mdRenderersMax = 16
)

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
	if len(c.text) >= mdTextMax {
		clear(c.text)
	}
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
		if len(c.renderers) >= mdRenderersMax {
			clear(c.renderers)
		}
		c.renderers[key] = r
	}
	// Markdown decodes character references, so "&#27;" would reach the
	// terminal as a raw ESC. Glamour decodes again, with
	// html.UnescapeString, from text it joins across inline markup, so a
	// reference split by emphasis re-forms where no scan of the source can
	// see it. Rendering with every "&" hidden decodes nothing by any route.
	out, err := r.Render(hideAmpersands(post.ReplaceControlRefs(src)))
	if err != nil {
		return plain()
	}
	// Glamour writes a link as an OSC 8 hyperlink around its URL, and has no
	// option to turn that off. Claude controls the text and the URL, so the
	// terminal must not get a clickable link; the URL stays visible as text.
	// safeANSI drops it along with anything else glamour should not have
	// passed through.
	out = strings.ReplaceAll(safeANSI(out), string(ampPlaceholder), "&")
	lines := strings.Split(strings.Trim(out, "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return lines
}

// ampPlaceholder stands in for "&" while glamour renders, so it sees no
// character reference to decode. U+E026 is in the private use area, one
// cell wide like "&", and no markdown syntax; a literal one in the source
// becomes U+FFFD first, so only a hidden "&" maps back.
const ampPlaceholder = '\ue026'

// hideAmpersands replaces every "&" outside code blocks with
// ampPlaceholder. Code blocks are shown as written, with no decoding, and
// their highlighter would mark the placeholder as an error.
func hideAmpersands(src string) string {
	src = strings.ReplaceAll(src, string(ampPlaceholder), "\ufffd")
	code := post.CodeBlockLines(src) // in source order
	var b strings.Builder
	k := 0
	for i := range len(src) {
		for k < len(code) && code[k][1] <= i {
			k++
		}
		if src[i] == '&' && !(k < len(code) && code[k][0] <= i) {
			b.WriteRune(ampPlaceholder)
			continue
		}
		b.WriteByte(src[i])
	}
	return b.String()
}

// safeANSI keeps only the escapes glamour itself writes: SGR sequences
// whose colors are xterm indices in 16-255 and whose other parameters are
// bold, italic, underline, strikethrough or their resets. Every other
// escape, OSC included, every other control character but newline and
// tab, and every bidi or invisible format character is dropped, so text
// that got through as an escape can't conceal, recolor, reorder or move
// anything.
func safeANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch {
		case s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[':
			j := i + 2
			for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
				j++
			}
			params, inter := s[i+2:j], j
			for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
				j++
			}
			if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7e {
				if s[j] == 'm' && j == inter && paletteSGR(params) {
					b.WriteString(s[i : j+1])
				}
				j++
			}
			i = j
		case s[i] == 0x1b && i+1 < len(s) && s[i+1] == ']':
			j := i + 2
			for j < len(s) && s[j] != 0x07 && s[j] != 0x1b {
				j++
			}
			switch {
			case j < len(s) && s[j] == 0x07:
				j++
			case j+1 < len(s) && s[j] == 0x1b && s[j+1] == '\\':
				j += 2
			}
			i = j
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == '\n' || r == '\t' || !unicode.IsControl(r) && !post.IsInvisible(r) {
				b.WriteString(s[i : i+size])
			}
			i += size
		}
	}
	return b.String()
}

// paletteSGR reports whether an SGR's parameters, in semicolon or colon
// form, are ones glamour writes with snow's styles.
func paletteSGR(params string) bool {
	ps := strings.Split(params, ";")
	for i := 0; i < len(ps); i++ {
		if sub := strings.Split(ps[i], ":"); len(sub) > 1 {
			if len(sub) != 3 || (sub[0] != "38" && sub[0] != "48") || sub[1] != "5" || !paletteIndex(sub[2]) {
				return false
			}
			continue
		}
		switch ps[i] {
		case "", "0", "1", "3", "4", "9", "22", "23", "24", "29", "39", "49":
		case "38", "48":
			if i+2 >= len(ps) || ps[i+1] != "5" || !paletteIndex(ps[i+2]) {
				return false
			}
			i += 2
		default:
			return false
		}
	}
	return true
}

func paletteIndex(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n >= 16 && n <= 255
}

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
