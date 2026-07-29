package tools

import (
	"os/exec"

	"devsandbox/internal/cmdpattern"
	"devsandbox/internal/herdrproxy"
	"devsandbox/internal/kittyproxy"
	"devsandbox/internal/notice"
)

func init() {
	Register(&Revdiff{})
}

// Revdiff declares the terminal capabilities the revdiff TUI launcher needs:
// the kitty argv patterns, the herdr launch-script shape, and a dependency on
// the shared temp directory so the launcher's sentinel and output files can
// cross the host↔sandbox boundary.
type Revdiff struct{}

func (r *Revdiff) Name() string { return "revdiff" }
func (r *Revdiff) Description() string {
	return "revdiff overlay launcher (kitty and herdr capability declarations)"
}
func (r *Revdiff) Available(_ string) bool { _, err := exec.LookPath("revdiff"); return err == nil }

// SharedTmp marks the dependency on the host↔sandbox shared temp directory. The
// launcher is a third-party shell script that places its sentinel, output and
// launch-script files with `mktemp "${TMPDIR:-/tmp}/…"`, so those files land
// somewhere the host can read only because sharedtmp.go points $TMPDIR there.
func (r *Revdiff) SharedTmp() {}

// Bindings and Environment are empty: the shared temp directory this tool needs
// is mounted and exported centrally, keyed on the SharedTmp marker above, so
// that several tools can depend on it without emitting the same mount twice.
func (r *Revdiff) Bindings(_, _ string) []Binding   { return nil }
func (r *Revdiff) Environment(_, _ string) []EnvVar { return nil }
func (r *Revdiff) ShellInit(_ string) string        { return "" }

func (r *Revdiff) KittyCapabilities() []kittyproxy.Capability {
	return []kittyproxy.Capability{kittyproxy.CapLaunchOverlay}
}

func (r *Revdiff) KittyLaunchPatterns() []kittyproxy.CommandPattern {
	// Pin the pattern to the revdiff binary at one resolved path. Matching on
	// basename alone accepts any path ending in "revdiff", and the IPC
	// directory below is a write-through bind shared with the host at an
	// identical path — so the sandbox could drop its own `revdiff` there and
	// have the host execute it. The resolved path instead lives on an overlay
	// whose sandbox-side writes never reach the host.
	//
	// Resolution failure denies every launch rather than falling back to
	// basename matching: a pattern that cannot pin its binary must not widen.
	resolved, err := cmdpattern.ResolveProgram("revdiff")
	if err != nil {
		notice.Warn("revdiff: cannot resolve the revdiff binary (%v); kitty launch requests will be denied", err)
		return nil
	}

	innerRevdiff := kittyproxy.CommandPattern{
		Program:     "revdiff",
		ResolvedBin: resolved,
		ArgsMatcher: kittyproxy.MatchAny(),
	}
	return []kittyproxy.CommandPattern{
		// Direct revdiff invocation (no wrapping shell).
		innerRevdiff,
		// `sh -c 'revdiff ...'` (simple wrapper, no sentinel).
		{Program: "sh", ArgsMatcher: kittyproxy.MatchShellExec(innerRevdiff)},
		// `sh -c "'revdiff' '...'; touch '<sentinel>'"` — the exact form
		// produced by the upstream revdiff kitty launcher to signal completion
		// back to the sandbox via a sentinel file.
		{Program: "sh", ArgsMatcher: kittyproxy.MatchShellExecSentinel(innerRevdiff)},
		// Same sentinel form but with a leading `/usr/bin/env KEY=VAL ...`
		// prefix; emitted by the launcher (v0.8.0+) when EDITOR/VISUAL are
		// set on the caller's shell so the overlay inherits them.
		{Program: "sh", ArgsMatcher: kittyproxy.MatchShellExecEnvSentinel(innerRevdiff)},
	}
}

func (r *Revdiff) HerdrCapabilities() []herdrproxy.Capability {
	return []herdrproxy.Capability{herdrproxy.CapLaunchOverlay}
}

// HerdrLaunchScript declares the one script shape the herdr proxy may run.
//
// Under herdr the launcher does not pass an inline command as it does for
// kitty; it writes a script into the shared IPC directory and asks herdr to run
// `sh <path>`. What needs constraining is therefore the script body:
//
//	#!/bin/sh
//	[/usr/bin/env 'EDITOR=…'] REVDIFF_EXIT_CODE_ON_ANNOTATIONS=true '<revdiff>' '--output=…' …; rc=$?; printf "%s" "$rc" > '<sentinel>'.tmp && mv -f '<sentinel>'.tmp '<sentinel>'
//
// The program is pinned to its resolved path for the same reason as the kitty
// patterns: the IPC directory is a write-through bind shared with the host at
// an identical path, so basename matching would let the sandbox supply the
// binary. Resolution failure returns a pattern that matches nothing, so the
// proxy denies every launch rather than widening.
func (r *Revdiff) HerdrLaunchScript() cmdpattern.ScriptPattern {
	resolved, err := cmdpattern.ResolveProgram("revdiff")
	if err != nil {
		notice.Warn("revdiff: cannot resolve the revdiff binary (%v); herdr launch requests will be denied", err)
		return cmdpattern.ScriptPattern{}
	}
	return cmdpattern.ScriptPattern{
		Shebangs: []string{"#!/bin/sh"},
		Statement: cmdpattern.CommandPattern{
			Program:     "revdiff",
			ResolvedBin: resolved,
			ArgsMatcher: cmdpattern.MatchAny(),
		},
	}
}

var (
	_ Tool                        = (*Revdiff)(nil)
	_ ToolWithKittyRequirements   = (*Revdiff)(nil)
	_ ToolWithKittyLaunchPatterns = (*Revdiff)(nil)
	_ ToolWithHerdrRequirements   = (*Revdiff)(nil)
	_ ToolWithHerdrLaunchScript   = (*Revdiff)(nil)
	_ ToolWithSharedTmp           = (*Revdiff)(nil)
)
