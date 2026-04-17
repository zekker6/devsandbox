package tools

import (
	"os/exec"

	"devsandbox/internal/kittyproxy"
)

func init() {
	Register(&Revdiff{})
}

// Revdiff declares the kitty capabilities the revdiff TUI launcher needs
// (open an overlay window and run `revdiff` inside it). The revdiff binary
// itself runs OUTSIDE the sandbox — kitty spawns it on the host — so this tool
// adds no bindings.
type Revdiff struct{}

func (r *Revdiff) Name() string { return "revdiff" }
func (r *Revdiff) Description() string {
	return "revdiff overlay launcher (kitty capability declaration)"
}
func (r *Revdiff) Available(_ string) bool                 { _, err := exec.LookPath("revdiff"); return err == nil }
func (r *Revdiff) Bindings(_ string, _ string) []Binding   { return nil }
func (r *Revdiff) Environment(_ string, _ string) []EnvVar { return nil }
func (r *Revdiff) ShellInit(_ string) string               { return "" }

func (r *Revdiff) KittyCapabilities() []kittyproxy.Capability {
	return []kittyproxy.Capability{kittyproxy.CapLaunchOverlay}
}

func (r *Revdiff) KittyLaunchPatterns() []kittyproxy.CommandPattern {
	innerRevdiff := kittyproxy.CommandPattern{Program: "revdiff", ArgsMatcher: kittyproxy.MatchAny()}
	return []kittyproxy.CommandPattern{
		// Direct revdiff invocation (no wrapping shell).
		innerRevdiff,
		// `sh -c 'revdiff ...'` (simple wrapper, no sentinel).
		{Program: "sh", ArgsMatcher: kittyproxy.MatchShellExec(innerRevdiff)},
		// `sh -c "'revdiff' '...'; touch '<sentinel>'"` — the exact form
		// produced by the upstream revdiff kitty launcher to signal completion
		// back to the sandbox via a sentinel file.
		{Program: "sh", ArgsMatcher: kittyproxy.MatchShellExecSentinel(innerRevdiff)},
	}
}

var (
	_ Tool                        = (*Revdiff)(nil)
	_ ToolWithKittyRequirements   = (*Revdiff)(nil)
	_ ToolWithKittyLaunchPatterns = (*Revdiff)(nil)
)
