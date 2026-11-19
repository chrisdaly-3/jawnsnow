package post

import (
	"html"
	"regexp"
	"strconv"
	"unicode"
)

// charRef is one HTML character reference: decimal, hex, or named.
var charRef = regexp.MustCompile(`&(?:#[0-9]+|#[xX][0-9A-Fa-f]+|[A-Za-z][A-Za-z0-9]*);`)

// ReplaceControlRefs replaces each character reference that decodes to a
// character Sanitize strips with U+FFFD. Markdown decodes references, so
// "&#27;" would otherwise reach a terminal as a raw ESC.
func ReplaceControlRefs(s string) string {
	return charRef.ReplaceAllStringFunc(s, func(ref string) string {
		if controlRef(ref) {
			return "�"
		}
		return ref
	})
}

// controlRef reports whether ref decodes to a character Sanitize strips.
// Numeric references are decoded here because html.UnescapeString maps
// C1 code points to Windows-1252 characters, which a markdown renderer may
// not do.
func controlRef(ref string) bool {
	body := ref[1 : len(ref)-1]
	if body[0] == '#' {
		digits, base := body[1:], 10
		if digits[0] == 'x' || digits[0] == 'X' {
			digits, base = digits[1:], 16
		}
		n, err := strconv.ParseUint(digits, base, 32)
		return err == nil && n <= unicode.MaxRune && stripped(rune(n))
	}
	dec := html.UnescapeString(ref)
	if dec == ref {
		return false
	}
	for _, r := range dec {
		if stripped(r) {
			return true
		}
	}
	return false
}

// stripped reports whether Sanitize removes r.
func stripped(r rune) bool {
	return r != '\n' && r != '\t' && (unicode.IsControl(r) || invisible(r))
}
