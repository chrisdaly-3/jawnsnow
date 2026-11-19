package post

import (
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// charRef is one HTML character reference: decimal, hex, or named. Glamour
// decodes with html.UnescapeString, which takes a numeric reference with
// no ";" and ends it at the last digit, so the ";" is optional there. The
// legacy names it takes with no ";" (amp, lt, nbsp…) all decode to
// printable characters, so a named reference still needs one.
var charRef = regexp.MustCompile(`&(?:#[0-9]+;?|#[xX][0-9A-Fa-f]+;?|[A-Za-z][A-Za-z0-9]*;)`)

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

// controlRef reports whether ref decodes to a character Sanitize strips,
// read either way it may be: as html.UnescapeString does, which glamour
// uses and which wraps an overlong number into an int32, and as its value,
// which goldmark uses and html.UnescapeString would map from C1 to
// Windows-1252.
func controlRef(ref string) bool {
	for _, r := range html.UnescapeString(ref) {
		if stripped(r) {
			return true
		}
	}
	if ref[1] != '#' {
		return false
	}
	digits, base := strings.TrimSuffix(ref[2:], ";"), 10
	if digits[0] == 'x' || digits[0] == 'X' {
		digits, base = digits[1:], 16
	}
	n, err := strconv.ParseUint(digits, base, 32)
	return err == nil && n <= unicode.MaxRune && stripped(rune(n))
}

// stripped reports whether Sanitize removes r.
func stripped(r rune) bool {
	return r != '\n' && r != '\t' && (unicode.IsControl(r) || invisible(r))
}

// IsInvisible reports zero-width and bidi format characters, which can
// hide or reorder text without being visible.
func IsInvisible(r rune) bool { return invisible(r) }
