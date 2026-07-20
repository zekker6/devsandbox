package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"devsandbox/internal/cmdpattern"
	"devsandbox/internal/herdrproxy"
	"devsandbox/internal/kittyproxy"
	"devsandbox/internal/notice"
)

// revdiffIpcRelPath is the per-session IPC directory root inside the host user's
// home, used as BOTH the bind source on the host and the bind destination inside
// the sandbox. The kitty-spawned overlay shell runs on the host and receives the
// sentinel/output paths as literal strings, so the path must resolve identically
// in both mount namespaces — hence the same string.
const revdiffIpcRelPath = ".cache/devsandbox/revdiff-ipc"

func init() {
	Register(&Revdiff{})
}

// Revdiff declares the terminal capabilities the revdiff TUI launcher needs
// (kitty argv patterns and the herdr launch-script shape) and provides a
// host-visible TMPDIR so its sentinel and output files can cross the
// host↔sandbox boundary.
type Revdiff struct {
	hostIpcDir string // captured on Start, consumed on Stop
}

func (r *Revdiff) Name() string { return "revdiff" }
func (r *Revdiff) Description() string {
	return "revdiff overlay launcher (kitty and herdr capability declarations)"
}
func (r *Revdiff) Available(_ string) bool { _, err := exec.LookPath("revdiff"); return err == nil }

func (r *Revdiff) Bindings(homeDir, sandboxHome string) []Binding {
	if homeDir == "" || sandboxHome == "" {
		return nil
	}
	shared := revdiffIpcPath(homeDir, sandboxHome)
	return []Binding{{
		Source:   shared,
		Dest:     shared,
		Type:     MountBind,
		Category: CategoryRuntime,
	}}
}

func (r *Revdiff) Environment(homeDir, sandboxHome string) []EnvVar {
	if homeDir == "" || sandboxHome == "" {
		return nil
	}
	return []EnvVar{{Name: "TMPDIR", Value: revdiffIpcPath(homeDir, sandboxHome)}}
}

func (r *Revdiff) ShellInit(_ string) string { return "" }

// Start ensures the shared IPC dir exists. It MUST NOT wipe the directory:
// the same path is exported as $TMPDIR for every sandboxed process, and
// long-lived tenants (Claude Code's per-session task cache, Node's compile
// cache, Go's build cache) populate subtrees under it. Wiping would yank state
// out from under a running caller; a subsequent non-recursive mkdir (as Node's
// fs.mkdirSync does) would then fail with ENOENT. Stale revdiff sentinels
// left behind from crashes are harmless — the launcher uses mktemp with fresh
// randomized names on every invocation.
func (r *Revdiff) Start(_ context.Context, homeDir, sandboxHome string) error {
	host := revdiffIpcPath(homeDir, sandboxHome)
	if err := os.MkdirAll(host, 0o700); err != nil {
		return fmt.Errorf("revdiff: create ipc dir: %w", err)
	}
	if err := os.Chmod(host, 0o700); err != nil {
		return fmt.Errorf("revdiff: chmod ipc dir: %w", err)
	}
	r.hostIpcDir = host
	return nil
}

// Stop is a no-op for the same reason Start doesn't wipe: the dir hosts
// long-lived tenants that must survive sandbox restarts for the same project
// (sessionID is stable per sandboxHome).
func (r *Revdiff) Stop() error {
	r.hostIpcDir = ""
	return nil
}

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

// revdiffIpcPath returns the shared IPC directory path — identical string on
// host and inside the sandbox.
func revdiffIpcPath(homeDir, sandboxHome string) string {
	return filepath.Join(homeDir, revdiffIpcRelPath, revdiffSessionID(sandboxHome))
}

// revdiffSessionID derives a stable, collision-resistant session tag from the
// host-side sandbox-home path. Keeps sibling sessions isolated under the shared
// .cache/devsandbox/revdiff-ipc/ root.
func revdiffSessionID(sandboxHome string) string {
	h := sha256.Sum256([]byte(sandboxHome))
	return hex.EncodeToString(h[:6]) // 12 hex chars is plenty for per-user collision resistance
}

var (
	_ Tool                        = (*Revdiff)(nil)
	_ ToolWithKittyRequirements   = (*Revdiff)(nil)
	_ ToolWithKittyLaunchPatterns = (*Revdiff)(nil)
	_ ToolWithHerdrRequirements   = (*Revdiff)(nil)
	_ ToolWithHerdrLaunchScript   = (*Revdiff)(nil)
	_ ActiveTool                  = (*Revdiff)(nil)
)
