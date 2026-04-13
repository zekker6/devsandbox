package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func init() {
	Register(&Zellij{})
}

// Zellij forwards the Zellij session environment into the sandbox.
// Mounts the Zellij socket directory and binary so the CLI works inside the sandbox.
type Zellij struct{}

func (z *Zellij) Name() string              { return "zellij" }
func (z *Zellij) Description() string       { return "Zellij terminal multiplexer session forwarding" }
func (z *Zellij) ShellInit(_ string) string { return "" }

// Available returns true when running inside a Zellij session.
// Requires ZELLIJ env var to be set and the zellij binary in PATH.
func (z *Zellij) Available(_ string) bool {
	if os.Getenv("ZELLIJ") == "" {
		return false
	}
	_, err := exec.LookPath("zellij")
	return err == nil
}

// zellijSocketDir returns the directory containing Zellij session sockets.
// Checks ZELLIJ_SOCK_DIR first, then falls back to /tmp/zellij-<uid>.
func zellijSocketDir() string {
	if dir := os.Getenv("ZELLIJ_SOCK_DIR"); dir != "" {
		return dir
	}
	return fmt.Sprintf("/tmp/zellij-%d", os.Getuid())
}

func (z *Zellij) Bindings(_ string, _ string) []Binding {
	if os.Getenv("ZELLIJ") == "" {
		return nil
	}

	var bindings []Binding

	// Mount the zellij socket directory so CLI commands work inside the sandbox.
	sockDir := zellijSocketDir()
	if _, err := os.Stat(sockDir); err == nil {
		bindings = append(bindings, Binding{
			Source:   sockDir,
			Dest:     sockDir,
			Category: CategoryRuntime,
			ReadOnly: false, // zellij CLI needs bidirectional socket access
		})
	}

	// Mount zellij binary if found.
	if zellijBin, err := exec.LookPath("zellij"); err == nil {
		bindings = append(bindings, Binding{
			Source:   zellijBin,
			Dest:     zellijBin,
			Category: CategoryRuntime,
			ReadOnly: true,
			Optional: true,
		})
	}

	return bindings
}

func (z *Zellij) Environment(_, _ string) []EnvVar {
	return []EnvVar{
		{Name: "ZELLIJ", FromHost: true},
		{Name: "ZELLIJ_SESSION_NAME", FromHost: true},
		{Name: "ZELLIJ_PANE_ID", FromHost: true},
	}
}

func (z *Zellij) Check(_ string) CheckResult {
	result := CheckBinary("zellij", "Install zellij: https://zellij.dev/documentation/installation")
	if !result.Available {
		return result
	}

	if os.Getenv("ZELLIJ") == "" {
		result.Available = false
		result.AddIssue("ZELLIJ not set — not running inside a Zellij session")
		return result
	}

	sockDir := zellijSocketDir()
	if _, err := os.Stat(sockDir); err != nil {
		result.Available = false
		result.AddIssue("zellij socket directory not found: " + sockDir)
		return result
	}

	// List session sockets for info.
	entries, err := os.ReadDir(sockDir)
	if err == nil {
		for _, e := range entries {
			result.AddInfo("session socket: " + filepath.Join(sockDir, e.Name()))
		}
	}

	if name := os.Getenv("ZELLIJ_SESSION_NAME"); name != "" {
		result.AddInfo("session: " + name)
	}

	return result
}

// Ensure interfaces are implemented.
var (
	_ Tool          = (*Zellij)(nil)
	_ ToolWithCheck = (*Zellij)(nil)
)
