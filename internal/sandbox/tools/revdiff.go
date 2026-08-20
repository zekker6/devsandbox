package tools

import (
	"fmt"
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
type Revdiff struct{ Mounting }

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

func (r *Revdiff) KittyLaunchPatterns(bounds cmdpattern.LaunchBounds) []kittyproxy.CommandPattern {
	resolved, err := resolveRevdiffBin(bounds)
	if err != nil {
		notice.Warn("revdiff: %v; kitty launch requests will be denied", err)
		return nil
	}
	if err := hostShellTrusted(bounds); err != nil {
		notice.Warn("revdiff: %v; kitty launch requests will be denied", err)
		return nil
	}
	return kittyLaunchPatternsFor(resolved, bounds)
}

// resolveRevdiffBin returns the one revdiff path the launch patterns accept.
//
// Pinning to a resolved path is what keeps basename matching out: the IPC
// directory is a write-through bind shared with the host at an identical path,
// so any path ending in "revdiff" can be a file the sandbox dropped there.
//
// Resolving it is not on its own enough. ResolveProgram answers which file the
// name means on the host, not who may rewrite that file, and the project tree is
// bound read-write at the path the host knows it by - so a revdiff resolving
// inside it is pinned to bytes the sandbox chooses. Either failure denies every
// launch rather than widening.
func resolveRevdiffBin(bounds cmdpattern.LaunchBounds) (string, error) {
	resolved, err := cmdpattern.ResolveProgram("revdiff")
	if err != nil {
		return "", fmt.Errorf("cannot resolve the revdiff binary (%v)", err)
	}
	if cmdpattern.PathUnder(resolved, bounds.UntrustedRoots()) {
		return "", fmt.Errorf("the host resolves `revdiff` to %s, which the sandbox can write", resolved)
	}
	return resolved, nil
}

// hostShellTrusted reports whether the wrapping shell these patterns accept
// resolves to a file the sandbox cannot supply.
//
// The launcher spells the shell `sh`, and the payload is forwarded to kitty
// verbatim, so the program that runs is whatever kitty's own PATH yields. A
// project-local bin directory on that PATH - a virtualenv, node_modules, a
// `bin` direnv adds - is bind-mounted read-write into the sandbox, so `sh`
// would then be a file the sandbox wrote. Denying every launch is the only
// answer available here: the argv cannot be rewritten on the way through, and
// the spelling cannot be narrowed to an absolute path without refusing the one
// the upstream launcher emits.
//
// Both spellings the host's lookup yields are checked, not just the
// symlink-resolved one. The lookup runs again on the host when kitty execs the
// argv, so the file it opens is the entry PATH found - and a `$PROJECT/bin/sh`
// symlink aimed at `/bin/sh` resolves out of every bound while remaining a
// directory entry the sandbox can repoint or replace before the launch.
func hostShellTrusted(bounds cmdpattern.LaunchBounds) error {
	paths := cmdpattern.HostProgramPaths("sh")
	if len(paths) == 0 {
		return fmt.Errorf("cannot resolve the host shell `sh`")
	}
	for _, p := range paths {
		if cmdpattern.PathUnder(p, bounds.UntrustedRoots()) {
			return fmt.Errorf("the host resolves `sh` to %s, which the sandbox can write", p)
		}
	}
	return nil
}

// kittyLaunchPatternsFor builds the pattern set around an already-resolved
// binary path. Split out from KittyLaunchPatterns so the tests that pin what
// these patterns refuse can run on a machine with no revdiff installed - they
// previously skipped, which is every CI machine, so the planted-binary and
// planted-EDITOR regressions were guarded by tests nothing ever executed.
func kittyLaunchPatternsFor(resolved string, bounds cmdpattern.LaunchBounds) []kittyproxy.CommandPattern {
	innerRevdiff := kittyproxy.CommandPattern{
		Program:     "revdiff",
		ResolvedBin: resolved,
		ArgsMatcher: kittyproxy.MatchAny(),
	}

	// The wrapping shell is pinned by spelling rather than by resolved path:
	// these two are the ones the host resolves itself, out of directories the
	// sandbox cannot write. Anything else — notably an absolute path into a
	// write-through bind such as the shared temp directory, where the sandbox
	// can plant its own `sh` at a path the host reads back — is denied, which is
	// what CommandPattern's exact Program matching enforces. Same two spellings
	// the herdr script parser accepts, for the same reason.
	shellArgv0 := []string{"sh", "/bin/sh"}
	shellForms := []func([]string) bool{
		// `sh -c 'revdiff ...'` (simple wrapper, no sentinel).
		kittyproxy.MatchShellExec(innerRevdiff),
		// `sh -c "'revdiff' '...'; touch '<sentinel>'"` — the exact form
		// produced by the upstream revdiff kitty launcher to signal completion
		// back to the sandbox via a sentinel file. The sentinel is bounded to
		// the shared temp directory the launcher places it in with
		// `mktemp "${TMPDIR:-/tmp}/…"`; the shape test alone let the sandbox
		// name any host path for the host to create, truncate and rename over.
		kittyproxy.MatchShellExecSentinel(innerRevdiff, bounds),
		// Same sentinel form but with a leading `/usr/bin/env KEY=VAL ...`
		// prefix; emitted by the launcher (v0.8.0+) when EDITOR/VISUAL are
		// set on the caller's shell so the overlay inherits them.
		kittyproxy.MatchShellExecEnvSentinel(innerRevdiff, bounds),
	}

	// Direct revdiff invocation (no wrapping shell), then every accepted shell
	// spelling crossed with every accepted wrapper form.
	patterns := []kittyproxy.CommandPattern{innerRevdiff}
	for _, argv0 := range shellArgv0 {
		for _, match := range shellForms {
			patterns = append(patterns, kittyproxy.CommandPattern{
				Program:     argv0,
				ArgsMatcher: match,
			})
		}
	}
	return patterns
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
//	[/usr/bin/env 'EDITOR=…'] REVDIFF_EXIT_CODE_ON_ANNOTATIONS=true '<revdiff>' '--output=…' … [2>'<stderr>']; rc=$?; printf "%s" "$rc" > '<sentinel>'.tmp && mv -f '<sentinel>'.tmp '<sentinel>'
//
// The program is pinned to its resolved path for the same reason as the kitty
// patterns, and by the same helper: see resolveRevdiffBin. A binary that cannot
// be resolved, or one resolving into a directory the sandbox writes, returns a
// pattern that matches nothing, so the proxy denies every launch rather than
// widening.
//
// The trailing redirect is the launcher's stderr capture (v0.8.23+). Its target
// is bounded by the same Bounds as the sentinel, and the herdr proxy clears the
// file before the run; see cmdpattern.ScriptMatch.
func (r *Revdiff) HerdrLaunchScript(bounds cmdpattern.LaunchBounds) cmdpattern.ScriptPattern {
	resolved, err := resolveRevdiffBin(bounds)
	if err != nil {
		notice.Warn("revdiff: %v; herdr launch requests will be denied", err)
		return cmdpattern.ScriptPattern{}
	}
	return herdrLaunchScriptFor(resolved, bounds)
}

// herdrLaunchScriptFor builds the script pattern around an already-resolved
// binary path, for the reason kittyLaunchPatternsFor is split out.
func herdrLaunchScriptFor(resolved string, bounds cmdpattern.LaunchBounds) cmdpattern.ScriptPattern {
	return cmdpattern.ScriptPattern{
		Shebangs: []string{"#!/bin/sh"},
		Statement: cmdpattern.CommandPattern{
			Program:     "revdiff",
			ResolvedBin: resolved,
			ArgsMatcher: cmdpattern.MatchAny(),
		},
		// Bounds the file the trailing clause has the host write and rename
		// into place, and the roots a program name may not resolve into. An
		// empty shared directory denies every body, which is what a caller that
		// could not derive it must get.
		Bounds: bounds,
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
