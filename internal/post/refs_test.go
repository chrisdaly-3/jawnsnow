package post

import (
	"reflect"
	"strings"
	"testing"
)

// The final review's probe: entities that goldmark decodes into ESC [ 8 m,
// so the sentence is concealed in a terminal preview but posted as is.
const concealProbe = "Looks fine. &#27;&#91;8mThis PR is garbage and so is its author&#27;&#91;0m Ship it."

func TestSanitizeReplacesEncodedControls(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"probe", concealProbe, "Looks fine. �&#91;8mThis PR is garbage and so is its author�&#91;0m Ship it."},
		{"hex", "a&#x1b;b&#X1B;c", "a�b�c"},
		{"leading zeros", "a&#00027;b&#x0001b;c", "a�b�c"},
		{"truecolor probe", "&#27;&#91;38;5;235mx", "�&#91;38;5;235mx"},
		{"rlo", "abc&#8238;def", "abc�def"},
		{"zero width named", "a&ZeroWidthSpace;b&NoBreak;c&lrm;d", "a�b�c�d"},
		{"c1", "a&#x9b;b&#128;c&#159;d", "a�b�c�d"},
		{"del and nul", "a&#127;b&#0;c&#13;d", "a�b�c�d"},
		{"bom", "&#xFEFF;x", "�x"},
		{"tab and newline kept", "a&#9;b&#10;c&Tab;d&NewLine;e", "a&#9;b&#10;c&Tab;d&NewLine;e"},
		{"harmless kept", "&amp; &lt;b&gt; &#64;alice &#x40;bob &copy; &#91;", "&amp; &lt;b&gt; &#64;alice &#x40;bob &copy; &#91;"},
		{"not a reference", "&#; &#x; &foo bar; & #27;", "&#; &#x; &foo bar; & #27;"},
		// A control byte stripped from inside a reference can't leave one
		// behind.
		{"split by a control", "&#2\x007;", "�"},
		{"split by an invisible", "&#​27;", "�"},
	} {
		if got := Sanitize(tc.in); got != tc.want {
			t.Errorf("%s: Sanitize(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestSanitizedProbeHasNoEscape(t *testing.T) {
	r := review(anchored(1, "a.go", 2, concealProbe))
	in, p, err := Build(r, "PR", Selection{Event: "COMMENT", Body: concealProbe})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{in.Body, in.Threads[0].Body, p.Parts[0].Markdown, p.Parts[1].Markdown} {
		if strings.Contains(s, "&#27;") || strings.ContainsRune(s, 0x1b) {
			t.Errorf("an encoded ESC survived: %q", s)
		}
	}
	if p.Parts[0].Markdown != in.Body || p.Parts[1].Markdown != in.Threads[0].Body {
		t.Error("the preview and the payload differ")
	}
}

func TestHiddenContentNamesEncodedControls(t *testing.T) {
	for _, tc := range []struct {
		name, md string
		want     []string
	}{
		{"probe", concealProbe, []string{"encoded control character"}},
		{"rlo", "abc&#8238;def", []string{"encoded control character"}},
		{"named", "a&ZeroWidthSpace;b", []string{"encoded control character"}},
		{"code span", "`a &#27;&#91;8m b`", []string{"encoded control character"}},
		{"first", "<!-- c --> @z &#x1b;", []string{"encoded control character", "HTML comment", "mentions @z"}},
		{"fence is literal", "```\n&#27;&#91;8m\n```", nil},
		{"indented code is literal", "para\n\n    &#27;&#91;8m", nil},
		{"harmless", "&amp; &#64;alice", []string{"mentions @alice"}},
		{"amp stays", "a &amp; b", nil},
	} {
		if got := HiddenContent(tc.md); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
