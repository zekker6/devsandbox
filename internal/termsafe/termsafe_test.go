package termsafe

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Inputs are spelled as UTF-8 bytes where the rune matters: "\xc2\x9b" is
// U+009B (the C1 control sequence introducer), "\xe2\x80\xae" is U+202E (the
// right-to-left override) and "\xe2\x80\x8e" is U+200E (left-to-right mark).

func TestEscape(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain ASCII", in: "GET /index.html", want: "GET /index.html"},
		{name: "ESC clears the screen", in: "\x1b[2J\x1b[H", want: `\x1b[2J\x1b[H`},
		{name: "C1 CSI arrives UTF-8-encoded", in: "\xc2\x9b2J", want: "\\u009b2J"},
		{name: "OSC sets the title", in: "\x1b]0;pwned\a", want: `\x1b]0;pwned\a`},
		{name: "DEL", in: "a\x7fb", want: `a\x7fb`},
		{name: "left-to-right mark is Cf", in: "a\xe2\x80\x8eb", want: "a\\u200eb"},
		{name: "right-to-left override is Cf", in: "\xe2\x80\xaeevil", want: "\\u202eevil"},
		{name: "newline and tab", in: "a\nb\tc", want: `a\nb\tc`},
		{name: "printable Latin", in: "café", want: "café"},
		{name: "printable CJK", in: "日本", want: "日本"},
		{name: "invalid UTF-8 byte", in: "a\xffb", want: `a\xffb`},
		{name: "quote and backslash are escaped too", in: `say "hi" \o/`, want: `say \"hi\" \\o/`},
		{name: "empty", in: "", want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Escape(tc.in)
			if got != tc.want {
				t.Errorf("Escape(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if HasControlRune(got) {
				t.Errorf("Escape(%q) = %q still carries a control rune", tc.in, got)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{name: "under the limit", in: "abc", n: 5, want: "abc"},
		{name: "at the limit", in: "abcde", n: 5, want: "abcde"},
		{name: "over the limit", in: "abcdefgh", n: 5, want: "ab..."},
		{name: "counts runes not bytes", in: "日本語テキスト", n: 5, want: "日本..."},
		{name: "multibyte at the limit", in: "日本語テキ", n: 5, want: "日本語テキ"},
		{name: "limit leaves no room for the ellipsis", in: "abcdef", n: 2, want: "ab"},
		{name: "limit of three", in: "abcdef", n: 3, want: "..."},
		{name: "zero limit", in: "abc", n: 0, want: ""},
		{name: "empty", in: "", n: 5, want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Truncate(tc.in, tc.n)
			if got != tc.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("Truncate(%q, %d) = %q splits a UTF-8 sequence", tc.in, tc.n, got)
			}
			if utf8.RuneCountInString(got) > tc.n {
				t.Errorf("Truncate(%q, %d) = %q exceeds %d runes", tc.in, tc.n, got, tc.n)
			}
		})
	}
}

// Truncate never splits a rune however the cut falls across a mixed string.
func TestTruncate_NeverSplitsSequences(t *testing.T) {
	in := "aé日\xf0\x9f\x98\x80b" + strings.Repeat("ü", 4)
	for n := 0; n <= utf8.RuneCountInString(in)+1; n++ {
		got := Truncate(in, n)
		if !utf8.ValidString(got) {
			t.Errorf("Truncate(%q, %d) = %q is not valid UTF-8", in, n, got)
		}
	}
}

func TestHasControlRune(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{name: "plain", in: "claude session 3 blocked", want: false},
		{name: "empty", in: "", want: false},
		{name: "non-ASCII letters", in: "café 日本", want: false},
		{name: "ESC", in: "clean\x1b[2Joverwritten", want: true},
		{name: "carriage return", in: "a\rb", want: true},
		{name: "tab", in: "a\tb", want: true},
		{name: "DEL", in: "a\x7fb", want: true},
		{name: "C1 control sequence introducer", in: "a\xc2\x9b2Jb", want: true},
		{name: "zero-width space is Cf", in: "blo\xe2\x80\x8bcked", want: true},
		{name: "right-to-left override is Cf", in: "\xe2\x80\xaeevil", want: true},
		{name: "left-to-right mark is Cf", in: "a\xe2\x80\x8eb", want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasControlRune(tc.in); got != tc.want {
				t.Errorf("HasControlRune(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
