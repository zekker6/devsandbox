package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// zellijSocketDirs returns candidate directories containing Zellij session sockets.
// Modern zellij (0.41+) stores the IPC socket under $XDG_RUNTIME_DIR/zellij; older
// versions and cache/log files use /tmp/zellij-<uid>. ZELLIJ_SOCKET_DIR overrides
// the default. We mount every candidate that exists so CLI commands resolve.
func zellijSocketDirs() []string {
	if dir := os.Getenv("ZELLIJ_SOCKET_DIR"); dir != "" {
		return []string{dir}
	}

	var dirs []string
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		dirs = append(dirs, filepath.Join(runtimeDir, "zellij"))
	}
	dirs = append(dirs, fmt.Sprintf("/tmp/zellij-%d", os.Getuid()))
	return dirs
}

func (z *Zellij) Bindings(_ string, _ string) []Binding {
	if os.Getenv("ZELLIJ") == "" {
		return nil
	}

	var bindings []Binding

	// Mount zellij socket directories as real bind mounts. Unix sockets cannot be
	// exposed through an overlayfs lower layer, so MountBind is mandatory here —
	// the default "split" policy maps CategoryRuntime to tmpoverlay, which hides
	// the socket and breaks `zellij run`.
	for _, sockDir := range zellijSocketDirs() {
		if _, err := os.Stat(sockDir); err != nil {
			continue
		}
		bindings = append(bindings, Binding{
			Source:   sockDir,
			Dest:     sockDir,
			Type:     MountBind,
			Category: CategoryRuntime,
			ReadOnly: false,
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

	var foundAny bool
	for _, sockDir := range zellijSocketDirs() {
		if _, err := os.Stat(sockDir); err != nil {
			continue
		}
		foundAny = true
		result.AddInfo("socket dir: " + sockDir)
		entries, err := os.ReadDir(sockDir)
		if err == nil {
			for _, e := range entries {
				result.AddInfo("session socket: " + filepath.Join(sockDir, e.Name()))
			}
		}
	}
	if !foundAny {
		result.Available = false
		result.AddIssue("zellij socket directory not found in any of: " + strings.Join(zellijSocketDirs(), ", "))
		return result
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
