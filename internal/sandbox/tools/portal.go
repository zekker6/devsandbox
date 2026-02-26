package tools

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func init() {
	Register(&Portal{})
}

// Portal provides XDG Desktop Portal access (notifications) via xdg-dbus-proxy.
// It launches a filtered D-Bus proxy that only exposes portal interfaces to the sandbox.
type Portal struct {
	notifications bool
	xdgRuntime    string // XDG_RUNTIME_DIR value (from host env or /run/user/<uid>)
	logger        ErrorLogger
	proxyCmd      *exec.Cmd
	proxySocket   string // path to the proxy socket file
}

func (p *Portal) Name() string              { return "portal" }
func (p *Portal) Description() string       { return "XDG Desktop Portal (notifications)" }
func (p *Portal) ShellInit(_ string) string { return "" }

// Available checks if xdg-dbus-proxy binary and a D-Bus session bus exist.
func (p *Portal) Available(homeDir string) bool {
	if _, err := exec.LookPath("xdg-dbus-proxy"); err != nil {
		return false
	}
	addr := dbusSessionBusAddress()
	if addr == "" {
		return false
	}
	socketPath := dbusSocketPath(addr)
	if socketPath == "" {
		return false
	}
	_, err := os.Stat(socketPath)
	return err == nil
}

// Configure applies portal-specific settings.
// Default: notifications=true.
func (p *Portal) Configure(globalCfg GlobalConfig, toolCfg map[string]any) {
	p.notifications = true
	p.xdgRuntime = os.Getenv("XDG_RUNTIME_DIR")
	if p.xdgRuntime == "" {
		p.xdgRuntime = fmt.Sprintf("/run/user/%d", os.Getuid())
	}

	if toolCfg == nil {
		return
	}
	if v, ok := toolCfg["notifications"].(bool); ok {
		p.notifications = v
	}
}

// SetLogger sets the logger for portal proxy errors.
// Implements ToolWithLogger.
func (p *Portal) SetLogger(logger ErrorLogger) {
	p.logger = logger
}

// proxySocketDir returns the directory for the proxy socket inside sandbox home.
func (p *Portal) proxySocketDir(sandboxHome string) string {
	return filepath.Join(sandboxHome, ".dbus-proxy")
}

// buildProxyArgs constructs xdg-dbus-proxy arguments.
// Only portal interfaces enabled in config are allowed through.
func (p *Portal) buildProxyArgs(busAddr, socketPath string) []string {
	args := []string{busAddr, socketPath, "--filter"}

	// Always allow portal Desktop interface — it's the entry point for all portals
	args = append(args, "--talk=org.freedesktop.portal.Desktop")

	// Notification portal
	if p.notifications {
		args = append(args, "--talk=org.freedesktop.portal.Notification")
	}

	return args
}

// Start launches xdg-dbus-proxy as a background process.
func (p *Portal) Start(ctx context.Context, homeDir, sandboxHome string) error {
	if !p.notifications {
		return nil
	}

	busAddr := dbusSessionBusAddress()
	if busAddr == "" {
		return fmt.Errorf("portal: DBUS_SESSION_BUS_ADDRESS not set")
	}

	// Create proxy socket directory on host
	proxyDir := p.proxySocketDir(sandboxHome)
	if err := os.MkdirAll(proxyDir, 0o700); err != nil {
		return fmt.Errorf("portal: create proxy socket dir: %w", err)
	}

	p.proxySocket = filepath.Join(proxyDir, "bus")

	// Remove stale socket from previous run
	_ = os.Remove(p.proxySocket)

	proxyArgs := p.buildProxyArgs(busAddr, p.proxySocket)

	p.proxyCmd = exec.CommandContext(ctx, "xdg-dbus-proxy", proxyArgs...)
	p.proxyCmd.Stdout = nil
	p.proxyCmd.Stderr = nil

	if err := p.proxyCmd.Start(); err != nil {
		return fmt.Errorf("portal: start xdg-dbus-proxy: %w", err)
	}

	// Wait for socket to appear (proxy creates it asynchronously)
	if err := waitForSocket(p.proxySocket, 3*time.Second); err != nil {
		_ = p.proxyCmd.Process.Kill()
		_ = p.proxyCmd.Wait()
		return fmt.Errorf("portal: proxy socket not created: %w", err)
	}

	return nil
}

// Stop shuts down the xdg-dbus-proxy process.
func (p *Portal) Stop() error {
	if p.proxyCmd == nil || p.proxyCmd.Process == nil {
		return nil
	}
	_ = p.proxyCmd.Process.Kill()
	_ = p.proxyCmd.Wait()

	// Clean up socket
	if p.proxySocket != "" {
		_ = os.Remove(p.proxySocket)
	}
	return nil
}

const flatpakInfoContent = `[Application]
name=dev.devsandbox.App
runtime=dev.devsandbox.Platform/x86_64/1.0
`

// Setup generates the .flatpak-info file needed by xdg-desktop-portal.
// Implements ToolWithSetup.
func (p *Portal) Setup(homeDir, sandboxHome string) error {
	if !p.notifications {
		return nil
	}

	infoPath := filepath.Join(sandboxHome, ".flatpak-info")
	return os.WriteFile(infoPath, []byte(flatpakInfoContent), 0o644)
}

func (p *Portal) Bindings(homeDir, sandboxHome string) []Binding {
	if !p.notifications {
		return nil
	}

	// Bind the proxy socket directory into XDG_RUNTIME_DIR/.dbus-proxy
	proxyDir := p.proxySocketDir(sandboxHome)
	dest := filepath.Join(p.xdgRuntime, ".dbus-proxy")

	flatpakInfoSrc := filepath.Join(sandboxHome, ".flatpak-info")

	return []Binding{
		{
			Source:   proxyDir,
			Dest:     dest,
			ReadOnly: true,
			Optional: true,
		},
		{
			Source:   flatpakInfoSrc,
			Dest:     "/.flatpak-info",
			ReadOnly: true,
			Optional: true,
		},
	}
}

func (p *Portal) Environment(homeDir, sandboxHome string) []EnvVar {
	if !p.notifications {
		return nil
	}

	// Point DBUS_SESSION_BUS_ADDRESS at the proxy socket inside sandbox
	socketPath := filepath.Join(p.xdgRuntime, ".dbus-proxy", "bus")

	return []EnvVar{
		{Name: "DBUS_SESSION_BUS_ADDRESS", Value: "unix:path=" + socketPath},
	}
}

// Check provides detailed availability information.
// Implements ToolWithCheck.
func (p *Portal) Check(homeDir string) CheckResult {
	result := CheckResult{
		BinaryName:  "xdg-dbus-proxy",
		InstallHint: "Install xdg-dbus-proxy (usually in xdg-dbus-proxy package) and ensure xdg-desktop-portal is running.",
	}

	binaryPath, err := exec.LookPath("xdg-dbus-proxy")
	if err != nil {
		result.Available = false
		result.AddIssue("xdg-dbus-proxy not found in PATH")
		return result
	}
	result.BinaryPath = binaryPath

	busAddr := dbusSessionBusAddress()
	if busAddr == "" {
		result.Available = false
		result.AddIssue("DBUS_SESSION_BUS_ADDRESS not set")
		return result
	}

	socketPath := dbusSocketPath(busAddr)
	if socketPath == "" {
		result.Available = false
		result.AddIssue("D-Bus session bus address is not a unix socket: " + busAddr)
		return result
	}

	if _, err := os.Stat(socketPath); err != nil {
		result.Available = false
		result.AddIssue("D-Bus session bus socket not found: " + socketPath)
		return result
	}

	result.Available = true
	result.ConfigPaths = []string{binaryPath, socketPath}

	if p.notifications {
		result.AddInfo("notifications: enabled")
	} else {
		result.AddInfo("notifications: disabled")
	}

	return result
}

// Ensure interfaces are implemented.
var (
	_ Tool           = (*Portal)(nil)
	_ ToolWithConfig = (*Portal)(nil)
	_ ToolWithSetup  = (*Portal)(nil)
	_ ToolWithCheck  = (*Portal)(nil)
	_ ActiveTool     = (*Portal)(nil)
	_ ToolWithLogger = (*Portal)(nil)
)

// dbusSessionBusAddress returns the D-Bus session bus address from environment.
func dbusSessionBusAddress() string {
	return os.Getenv("DBUS_SESSION_BUS_ADDRESS")
}

// dbusSocketPath extracts the unix socket path from a D-Bus address string.
// Handles formats like "unix:path=/run/user/1000/bus" and
// "unix:path=/run/user/1000/bus,guid=...".
func dbusSocketPath(addr string) string {
	for part := range strings.SplitSeq(addr, ",") {
		if path, ok := strings.CutPrefix(part, "unix:path="); ok {
			return path
		}
	}
	return ""
}

// waitForSocket polls for a unix socket to appear, up to timeout.
func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			conn, err := net.DialTimeout("unix", path, 500*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", path)
}
