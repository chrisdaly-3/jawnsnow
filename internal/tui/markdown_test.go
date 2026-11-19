package tui

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	gansi "charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"github.com/alecthomas/chroma/v2"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"

	"github.com/chrisdaly-3/jawnsnow/internal/config"
	"github.com/chrisdaly-3/jawnsnow/internal/post"
	"github.com/chrisdaly-3/jawnsnow/internal/theme"
)

func mdTheme(t *testing.T, style string, noColor bool) theme.Theme {
	t.Helper()
	if noColor {
		t.Setenv("NO_COLOR", "1")
	} else {
		t.Setenv("NO_COLOR", "")
	}
	th, err := theme.New(config.ThemeConfig{Markdown: style})
	if err != nil {
		t.Fatalf("theme.New: %v", err)
	}
	return th
}

// walkColors calls fn for every Color and BackgroundColor string in v.
func walkColors(v reflect.Value, name string, fn func(name, c string)) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			walkColors(v.Elem(), name, fn)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			f := v.Type().Field(i)
			fv := v.Field(i)
			if (f.Name == "Color" || f.Name == "BackgroundColor") && fv.Kind() == reflect.Pointer {
				if !fv.IsNil() {
					fn(name+"."+f.Name, fv.Elem().String())
				}
				continue
			}
			walkColors(fv, name+"."+f.Name, fn)
		}
	}
}

func checkGlowColors(t *testing.T, style string) {
	t.Helper()
	cfg := glamourStyle(mdKey{width: 60, style: style})
	n := 0
	walkColors(reflect.ValueOf(cfg), "", func(name, c string) {
		n++
		if i, err := strconv.Atoi(c); err != nil || i < 16 || i > 255 {
			t.Errorf("%s = %q, want an index in 16-255", name, c)
		}
	})
	if n < 10 {
		t.Errorf("walked only %d colors; the walk is broken", n)
	}
}

// The syntax colors live in a Chroma style registered by name, because
// Chroma reads hex. Each must be an exact xterm palette color in 16-255, which
// its 256-color formatter prints as that index.
func checkChromaColors(t *testing.T, style string) {
	t.Helper()
	cfg := glamourStyle(mdKey{width: 60, style: style})
	st, ok := chromastyles.Registry[cfg.CodeBlock.Theme]
	if !ok {
		t.Fatalf("chroma style %q is not registered", cfg.CodeBlock.Theme)
	}
	palette := map[string]bool{}
	for i := 16; i < 256; i++ {
		r, g, b, _ := ansi.IndexedColor(i).RGBA()
		palette[fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)] = true
	}
	n := 0
	for _, tt := range st.Types() {
		e := st.Get(tt)
		for _, c := range []chroma.Colour{e.Colour, e.Background} {
			if c.IsSet() {
				n++
				if !palette[c.String()] {
					t.Errorf("%v: %s is not an xterm 16-255 color", tt, c)
				}
			}
		}
	}
	if n < 10 {
		t.Errorf("checked only %d chroma colors", n)
	}
}

func TestGlowStyleColorsInRange(t *testing.T) {
	checkGlowColors(t, "dark")
	checkChromaColors(t, "dark")
}
func TestGlowStyleColorsInRange_Light(t *testing.T) {
	checkGlowColors(t, "light")
	checkChromaColors(t, "light")
}

var boldSGR = regexp.MustCompile(`\x1b\[(?:[0-9;]*;)?1(?:;[0-9;]*)?m`)

const richSrc = "# Title\n\n**bold** and `code`\n\n```go\nfunc x() {}\n```\n\n- item"

func TestRenderMarkdownIsRich(t *testing.T) {
	out := strings.Join(newMarkdownCache().render(richSrc, 60, mdTheme(t, "dark", false)), "\n")
	plain := ansi.Strip(out)
	for _, bad := range []string{"**", "```"} {
		if strings.Contains(plain, bad) {
			t.Errorf("stripped output contains %q:\n%s", bad, plain)
		}
	}
	for _, want := range []string{"Title", "bold", "func x() {}", "• item"} {
		if !strings.Contains(plain, want) {
			t.Errorf("stripped output lacks %q:\n%s", want, plain)
		}
	}
	// Glamour folds bold into the color sequence (ESC[38;5;252;1m), so look
	// for a bold parameter rather than a bare ESC[1m.
	if !boldSGR.MatchString(out) {
		t.Errorf("raw output has no bold escape: %q", out)
	}
}

func TestRenderKeepsLinkURLVisible(t *testing.T) {
	out := strings.Join(newMarkdownCache().render("[click](https://example.com/x)", 60, mdTheme(t, "dark", false)), "\n")
	if !strings.Contains(ansi.Strip(out), "https://example.com/x") {
		t.Errorf("URL not visible: %q", ansi.Strip(out))
	}
	if strings.Contains(out, "\x1b]8;") {
		t.Errorf("output keeps an OSC 8 link: %q", out)
	}
}

func TestRenderNoColorIsPlain(t *testing.T) {
	out := strings.Join(newMarkdownCache().render(richSrc, 60, mdTheme(t, "dark", true)), "\n")
	if strings.Contains(out, "\x1b[") {
		t.Errorf("NO_COLOR output has escapes: %q", out)
	}
}

func TestRenderLightDiffersFromDark(t *testing.T) {
	c := newMarkdownCache()
	dark := c.render(richSrc, 60, mdTheme(t, "dark", false))
	light := c.render(richSrc, 60, mdTheme(t, "light", false))
	if strings.Join(dark, "\n") == strings.Join(light, "\n") {
		t.Error("light and dark render identically")
	}
}

// Glamour's package-level styles are shared; converting colors must not
// write through them.
func TestGlowStyleDoesNotMutateGlamour(t *testing.T) {
	for name, cfg := range map[string]gansi.StyleConfig{"dark": styles.DarkStyleConfig, "light": styles.LightStyleConfig} {
		before := glamourStyleHexCount(cfg)
		glamourStyle(mdKey{width: 60, style: name})
		if after := glamourStyleHexCount(cfg); after != before {
			t.Errorf("%s: hex colors in glamour's style went from %d to %d", name, before, after)
		}
	}
}

func glamourStyleHexCount(cfg gansi.StyleConfig) int {
	n := 0
	walkColors(reflect.ValueOf(cfg), "", func(_, c string) {
		if strings.HasPrefix(c, "#") {
			n++
		}
	})
	return n
}

const paletteSrc = "# Heading\n\n**bold** and `code` and [a link](https://example.com/x)\n\n- one\n- two\n\n" +
	"```go\n// comment\nfunc add(a, b int) string {\n\treturn \"x\" + fmt.Sprint(a+b, 42)\n}\n```\n\n> quote\n"

var sgrRE = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

// Every color in the rendered bytes is an xterm index in 16-255: no
// truecolor, and none of the basic-16 parameters a terminal theme remaps.
func TestRenderedBytesUseOnlyPalette(t *testing.T) {
	for _, style := range []string{"dark", "light"} {
		out := strings.Join(newMarkdownCache().render(paletteSrc+"\n"+concealProbe, 60, mdTheme(t, style, false)), "\n")
		checkOnlyPaletteSGR(t, style, out)
		n := 0
		for _, m := range sgrRE.FindAllStringSubmatch(out, -1) {
			params := strings.Split(m[1], ";")
			for i := 0; i < len(params); i++ {
				p, _ := strconv.Atoi(params[i])
				switch {
				case (p == 38 || p == 48) && i+1 < len(params) && params[i+1] == "2":
					t.Errorf("%s: truecolor in %q", style, m[0])
					i += 4
				case (p == 38 || p == 48) && i+2 < len(params) && params[i+1] == "5":
					n++
					if idx, _ := strconv.Atoi(params[i+2]); idx < 16 || idx > 255 {
						t.Errorf("%s: index %d out of 16-255 in %q", style, idx, m[0])
					}
					i += 2
				case p >= 30 && p <= 37, p >= 40 && p <= 47, p >= 90 && p <= 97, p >= 100 && p <= 107:
					t.Errorf("%s: basic color parameter %d in %q", style, p, m[0])
				}
			}
		}
		if n < 8 {
			t.Errorf("%s: only %d indexed colors found; the sample is too thin", style, n)
		}
	}
}

func TestRenderedLinesFitWidth(t *testing.T) {
	for _, style := range []string{"dark", "light"} {
		for _, w := range []int{20, 40, 80} {
			for _, l := range newMarkdownCache().render(paletteSrc, w, mdTheme(t, style, false)) {
				if got := ansi.StringWidth(l); got > w {
					t.Errorf("%s width %d: line is %d cells: %q", style, w, got, ansi.Strip(l))
				}
			}
		}
	}
}

// concealProbe is the final review's probe: entities that decode to an SGR
// conceal around a sentence, and truecolor and basic-16 colors after it.
const concealProbe = "Looks fine. &#27;&#91;8mThis PR is garbage and so is its author&#27;&#91;0m Ship it. " +
	"&#27;&#91;38;2;1;2;3mtrue&#x1b;[31mbasic &#27;[2J `code &#27;&#91;8mspan` &#8238;rlo"

// escapes is every escape sequence in s, and whatever is left of a
// broken one.
var escapes = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]?|\][^\x07\x1b]*(?:\x07|\x1b\\)?|.?)`)

// checkOnlyPaletteSGR fails on any escape the renderer does not emit
// itself: only SGR, with colors only as 38;5;N or 48;5;N in 16-255.
func checkOnlyPaletteSGR(t *testing.T, name, out string) {
	t.Helper()
	for _, e := range escapes.FindAllString(out, -1) {
		if !strings.HasPrefix(e, "\x1b[") || !strings.HasSuffix(e, "m") {
			t.Errorf("%s: non-SGR escape %q", name, e)
			continue
		}
		params := strings.Split(e[2:len(e)-1], ";")
		for i := 0; i < len(params); i++ {
			switch p := params[i]; p {
			case "", "0", "1", "3", "4", "9":
			case "38", "48":
				idx := -1
				if i+2 < len(params) && params[i+1] == "5" {
					idx, _ = strconv.Atoi(params[i+2])
				}
				if idx < 16 || idx > 255 {
					t.Errorf("%s: color outside 16-255 in %q", name, e)
				}
				i += 2
			default:
				t.Errorf("%s: parameter %q in %q", name, p, e)
			}
		}
	}
}

func TestRenderNeutralisesEncodedControls(t *testing.T) {
	for _, style := range []string{"dark", "light"} {
		out := strings.Join(newMarkdownCache().render(concealProbe, 60, mdTheme(t, style, false)), "\n")
		checkOnlyPaletteSGR(t, style, out)
		plain := strings.Join(strings.Fields(ansi.Strip(out)), " ")
		if !strings.Contains(plain, "This PR is garbage and so is its author") {
			t.Errorf("%s: the sentence is not visible: %q", style, plain)
		}
		if strings.ContainsRune(out, '\u202e') {
			t.Errorf("%s: an encoded RLO reached the output", style)
		}
	}
}

// The backstop: whatever reaches glamour's output, only the renderer's
// own kind of SGR leaves.
func TestSafeANSIKeepsOnlyPaletteSGR(t *testing.T) {
	in := "a\x1b[38;5;252;1mb\x1b[m\x1b[0mc" +
		"\x1b[8mconceal\x1b[38;2;1;2;3mtrue\x1b[38:2::1:2:3mcolon\x1b[31mbasic\x1b[97mbright\x1b[44mbg" +
		"\x1b[38;5;7mlow\x1b[48:5:3mlowcolon\x1b[38:5:200mok\x1b[2Jclear\x1b]0;title\x07osc\x1b]8;;u\x1b\\link\x1bc" +
		"\u009b8mc1\x07bell"
	got := safeANSI(in)
	want := "a\x1b[38;5;252;1mb\x1b[m\x1b[0mcconcealtruecolonbasicbrightbglowlowcolon\x1b[38:5:200mokclearosclinkc8mc1bell"
	if got != want {
		t.Errorf("safeANSI:\n got %q\nwant %q", got, want)
	}
	checkOnlyPaletteSGR(t, "safeANSI", strings.ReplaceAll(got, ":", ";"))
}

// The cache is bounded: a long session of distinct bodies and widths
// can't grow it without limit.
func TestMarkdownCacheIsBounded(t *testing.T) {
	c := newMarkdownCache()
	th := mdTheme(t, "dark", false)
	for i := range 600 {
		c.render(fmt.Sprintf("body %d", i), 40, th)
	}
	if n := len(c.text); n > mdTextMax || n == 0 {
		t.Errorf("%d rendered texts cached, want 1-%d", n, mdTextMax)
	}
	for w := range 20 {
		c.render("x", 20+w, th)
	}
	if n := len(c.renderers); n > mdRenderersMax || n == 0 {
		t.Errorf("%d renderers cached, want 1-%d", n, mdRenderersMax)
	}
	if got := c.render("body 599", 40, th); len(got) == 0 || !strings.Contains(ansi.Strip(got[0]), "body 599") {
		t.Errorf("render after clearing = %q", got)
	}
}

// Glamour decodes strikethrough from the text of its children joined, so a
// reference split by inline markup re-forms after any source-level scan.
// These are the re-review's probes.
var splitRefProbes = []string{
	"~~&#*27*[38;5;16;48;5;16mThis PR is garbage&#*27*[0m~~",
	"~~&#x*1b*[38;5;16;48;5;16mThis PR is garbage&#x*1b*[0m~~",
	"~~&#`27`[38;5;16;48;5;16mThis PR is garbage&#`27`[0m~~",
	"~~&#_27_[38;5;16;48;5;16mThis PR is garbage&#_27_[0m~~",
	"~~&#[27](x)[38;5;16;48;5;16mThis PR is garbage&#[27](x)[0m~~",
	"~~&*#27*[38;5;16;48;5;16mThis PR is garbage&*#27*[0m~~",
	"~~*&*#27[38;5;16;48;5;16mThis PR is garbage*&*#27[0m~~",
	"~~&#4294967*323*[38;5;16;48;5;16mThis PR is garbage&#4294967*323*[0m~~",
}

var splitRLOProbes = []string{
	"~~&#*8238*;evil~~", "~~&#82*38*;x~~", "~~&#82`38`;x~~", "~~&#82[38](u);x~~", "~~&#x20*2e*;x~~",
}

// checkNoDecodedControls fails on a raw ESC outside the renderer's own
// SGR, a C1 control, or a bidi or invisible format character.
func checkNoDecodedControls(t *testing.T, name, out string) {
	t.Helper()
	checkOnlyPaletteSGR(t, name, out)
	if strings.Contains(out, "\x1b[38;5;16;48;5;16m") {
		t.Errorf("%s: the encoded black-on-black SGR reached the output", name)
	}
	for _, r := range ansi.Strip(out) {
		if (r >= 0x80 && r <= 0x9f) || post.IsInvisible(r) || (r < 0x20 && r != '\n' && r != '\t') {
			t.Errorf("%s: %U in the output", name, r)
		}
	}
}

func TestRenderNeverDecodesReferences(t *testing.T) {
	for _, style := range []string{"dark", "light"} {
		th := mdTheme(t, style, false)
		for _, src := range splitRefProbes {
			out := strings.Join(newMarkdownCache().render(src, 100, th), "\n")
			checkNoDecodedControls(t, style+" "+src, out)
			if plain := ansi.Strip(out); !strings.Contains(plain, "This PR is garbage") {
				t.Errorf("%s %s: the sentence is not visible: %q", style, src, plain)
			}
		}
		for _, src := range splitRLOProbes {
			checkNoDecodedControls(t, style+" "+src, strings.Join(newMarkdownCache().render(src, 100, th), "\n"))
		}
	}
}

// With references left undecoded, an "&" still reads as one, and a code
// block keeps its own highlighting of it.
func TestRenderKeepsAmpersands(t *testing.T) {
	th := mdTheme(t, "dark", false)
	c := newMarkdownCache()
	plain := ansi.Strip(strings.Join(c.render("AT&T, &amp; and R&D in `a&b` and ~~x&y~~", 80, th), "\n"))
	for _, want := range []string{"AT&T,", "&amp; and", "R&D", "a&b", "x&y"} {
		if !strings.Contains(plain, want) {
			t.Errorf("no %q in %q", want, plain)
		}
	}
	code := strings.Join(c.render("```go\nif a && b {}\n```", 80, th), "\n")
	if !strings.Contains(ansi.Strip(code), "if a && b {}") {
		t.Errorf("the code block lost its &&: %q", ansi.Strip(code))
	}
	if strings.Contains(code, "48;5;") {
		t.Errorf("&& is highlighted as an error in the code block: %q", code)
	}
	// The placeholder can't be smuggled in to become an "&".
	if got := ansi.Strip(strings.Join(c.render("x\ue026y", 80, th), "\n")); !strings.Contains(got, "x\ufffdy") {
		t.Errorf("a literal placeholder rendered as %q, want U+FFFD", got)
	}
}

func TestSafeANSIDropsInvisibles(t *testing.T) {
	if got := safeANSI("a\u202eb\u200bc\u2066d\ufeffe\u2060f\u200fg"); got != "abcdefg" {
		t.Errorf("safeANSI = %q", got)
	}
}

// Glamour parses with definition lists on: an indented line after a blank
// line following ": def" continues the definition as text, where GFM alone
// sees indented code. These are the re-review's payloads.
const dlSentence = "~~&#*27*[38;5;16;48;5;16mThis PR is garbage&#*27*[0m~~"

var defListProbes = map[string]string{
	"indented":       "Term\n: def\n\n    " + dlSentence,
	"wide marker":    "Term\n:   def\n\n    " + dlSentence,
	"tab":            "Term\n: def\n\n\t" + dlSentence,
	"in a list item": "- Term\n  : def\n\n      " + dlSentence,
	"two blanks":     "Term\n: def\n\n\n    " + dlSentence,
	"backtick split": "Term\n: def\n\n    ~~&#`27`[38;5;16;48;5;16mThis PR is garbage&#`27`[0m~~",
}

func TestRenderDefinitionListsDecodeNothing(t *testing.T) {
	for _, style := range []string{"dark", "light"} {
		th := mdTheme(t, style, false)
		for name, src := range defListProbes {
			out := strings.Join(newMarkdownCache().render(src, 100, th), "\n")
			checkNoDecodedControls(t, style+" "+name, out)
			if plain := ansi.Strip(out); !strings.Contains(plain, "This PR is garbage") {
				t.Errorf("%s %s: the sentence is not visible: %q", style, name, plain)
			}
		}
	}
}

// A fence inside a definition is still a code block, shown as written.
func TestRenderFenceInDefinitionKeepsAmpersands(t *testing.T) {
	out := strings.Join(newMarkdownCache().render("Term\n:   ```go\n    if a && b {}\n    ```\n", 80, mdTheme(t, "dark", false)), "\n")
	if !strings.Contains(ansi.Strip(out), "if a && b {}") {
		t.Errorf("the fence lost its &&: %q", ansi.Strip(out))
	}
	if strings.ContainsRune(out, ampPlaceholder) || strings.Contains(out, "48;5;203") {
		t.Errorf("the fence's && went through the placeholder: %q", out)
	}
}

// genBlocks makes n markdown inputs from block-structure lines that
// definition lists, lists, quotes, fences and indented code mix, each with
// &#9731; (a snowman) somewhere a reference might decode.
func genBlocks(n int) []string {
	pool := []string{
		"Term", ": def", ":   def", ":\tdef", "", "", "    code &#9731; x", "\tcode &#9731;",
		"- item &#9731;", "  : def &#9731;", "      ind &#9731;", "```", "```go", "~~~", "   ```",
		"text &#9731; t", "~~&#*9731*;~~", "> quote &#9731;", ">     q &#9731;", "1. x", "   : def",
		"# h &#9731;", "| a &#9731; | b |", "|---|---|", "<div>", "</div>", "  - nested", "        deep &#9731;",
		"`&#9731;`", "&#x2603;", "a && b",
	}
	rng := rand.New(rand.NewPCG(1, 2))
	out := make([]string, n)
	for i := range out {
		lines := make([]string, 3+rng.IntN(10))
		for j := range lines {
			lines[j] = pool[rng.IntN(len(pool))]
		}
		out[i] = strings.Join(lines, "\n")
	}
	return out
}

// Whatever the block structure, glamour decodes no reference: the snowman
// never shows, only its reference as typed.
func TestRenderDecodesNoReferenceInAnyBlock(t *testing.T) {
	th := mdTheme(t, "dark", false)
	c := newMarkdownCache()
	for _, src := range genBlocks(400) {
		out := strings.Join(c.render(src, 80, th), "\n")
		if strings.ContainsRune(out, '☃') {
			t.Errorf("a reference was decoded in:\n%s\n---\n%s", src, ansi.Strip(out))
		}
	}
}
