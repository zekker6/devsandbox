package tools

import (
	"os"
	"os/exec"
	"strings"
)

func init() {
	Register(&Kitty{})
}

// Kitty provides kitty terminal remote control access inside the sandbox.
// Mounts the kitty listen socket so tools like revdiff can open overlay panes.
type Kitty struct{}

func (k *Kitty) Name() string              { return "kitty" }
func (k *Kitty) Description() string       { return "Kitty terminal remote control for overlay panes" }
func (k *Kitty) ShellInit(_ string) string { return "" }

// Available returns true when kitty remote control is configured.
// Requires KITTY_LISTEN_ON to be set (kitty with allow_remote_control + listen_on)
// and the kitty binary in PATH.
func (k *Kitty) Available(_ string) bool {
	if os.Getenv("KITTY_LISTEN_ON") == "" {
		return false
	}
	_, err := exec.LookPath("kitty")
	return err == nil
}

// kittySocketPath extracts the unix socket path from KITTY_LISTEN_ON.
// Handles both "unix:/path" and bare "/path" formats.
func kittySocketPath(listenOn string) string {
	if listenOn == "" {
		return ""
	}
	return strings.TrimPrefix(listenOn, "unix:")
}

func (k *Kitty) Bindings(_ string, _ string) []Binding {
	listenOn := os.Getenv("KITTY_LISTEN_ON")
	sockPath := kittySocketPath(listenOn)
	if sockPath == "" {
		return nil
	}

	bindings := []Binding{
		{
			Source:   sockPath,
			Dest:     sockPath,
			Category: CategoryRuntime,
			ReadOnly: false, // kitty @ needs bidirectional socket access
		},
	}

	// Mount kitty binary if found (needed for `kitty @` remote control CLI)
	if kittyBin, err := exec.LookPath("kitty"); err == nil {
		bindings = append(bindings, Binding{
			Source:   kittyBin,
			Dest:     kittyBin,
			Category: CategoryRuntime,
			ReadOnly: true,
			Optional: true,
		})
	}

	return bindings
}

func (k *Kitty) Environment(_, _ string) []EnvVar {
	return []EnvVar{
		{Name: "KITTY_LISTEN_ON", FromHost: true},
		{Name: "KITTY_WINDOW_ID", FromHost: true},
		{Name: "KITTY_PID", FromHost: true},
	}
}

func (k *Kitty) Check(_ string) CheckResult {
	result := CheckBinary("kitty", "Install kitty terminal: https://sw.kovidgoyal.net/kitty/")
	if !result.Available {
		return result
	}

	listenOn := os.Getenv("KITTY_LISTEN_ON")
	if listenOn == "" {
		result.Available = false
		result.AddIssue("KITTY_LISTEN_ON not set — enable remote control in kitty.conf: allow_remote_control socket-only, listen_on unix:/tmp/kitty-{kitty_pid}")
		return result
	}

	sockPath := kittySocketPath(listenOn)
	if _, err := os.Stat(sockPath); err != nil {
		result.Available = false
		result.AddIssue("kitty socket not found: " + sockPath)
		return result
	}

	result.AddInfo("listen socket: " + sockPath)
	return result
}

// Ensure interfaces are implemented.
var (
	_ Tool          = (*Kitty)(nil)
	_ ToolWithCheck = (*Kitty)(nil)
)
