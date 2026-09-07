// Package termsafe renders strings a sandbox supplied so that a host terminal
// shows them rather than acts on them.
//
// A terminal interprets control characters wherever they appear in its output,
// raw mode or not: ESC opens a sequence that can move the cursor, clear the
// screen or repaint a prompt, and a C1 control such as U+009B (CSI) does the
// same arriving UTF-8-encoded. Format characters (Cf) render nothing themselves
// but change how the surrounding text reads - a bidi override reverses a line,
// a zero-width joiner hides a break. Text the sandbox wrote and devsandbox
// prints on the host goes through Escape when it must be shown, or is refused
// with HasControlRune when it can be.
package termsafe

import (
	"strconv"
	"unicode"
	"unicode/utf8"
)

const ellipsis = "..."

// Escape returns s with every character a terminal might act on rather than
// render - the Unicode control (Cc) and format (Cf) categories, and any byte
// that is not valid UTF-8 - replaced by the visible Go escape strconv prints
// for it: `\x1b`, `\u009b`, `\u202e`. Printable text passes through, ASCII or
// not; a double quote and a backslash are escaped as well, so that the result
// reads unambiguously. The rune classes are strconv's, not reimplemented here.
func Escape(s string) string {
	q := strconv.QuoteToGraphic(s)
	return q[1 : len(q)-1]
}

// Truncate returns s when it is at most n runes long, and otherwise its first
// n-3 runes followed by "...", so the result never exceeds n runes and never
// ends inside a UTF-8 sequence. Escape before truncating, not after: escaping
// can lengthen a string past the width, and cutting escaped text leaves only
// printable ASCII on either side of the cut.
func Truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	if n < len(ellipsis) {
		return prefix(s, n)
	}
	return prefix(s, n-len(ellipsis)) + ellipsis
}

// prefix returns the first n runes of s, which must hold more than n.
func prefix(s string, n int) string {
	if n <= 0 {
		return ""
	}
	seen := 0
	for i := range s {
		if seen == n {
			return s[:i]
		}
		seen++
	}
	return s
}

// HasControlRune reports whether s contains a character a terminal may act on
// rather than render: a Unicode control (Cc) or format (Cf) character.
//
// The check is per rune, not per byte: a byte-wise scan for < 0x20 sees only
// C0, while the C1 controls (U+0080-U+009F, U+009B being CSI) arrive
// UTF-8-encoded as bytes >= 0xC2 and would pass it - defeating the check in
// exactly the case it exists for. Format characters are refused with them: a
// bidi override or zero-width joiner in a one-line label can only misrepresent
// what the user is looking at. Ordinary non-ASCII text is allowed, because the
// check bounds behavior, not charset.
func HasControlRune(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return true
		}
	}
	return false
}
