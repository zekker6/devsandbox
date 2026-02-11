package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"devsandbox/internal/dockerproxy"
)

func init() {
	Register(&Docker{})
}

const (
	defaultLinuxDockerSocket = "/run/docker.sock"
	dockerSocketName         = "docker.sock"
)

// macOSDockerSocketCandidates returns ordered candidate paths for the Docker
// socket on macOS. The order reflects popularity: Docker Desktop, OrbStack
// (symlinks /var/run/docker.sock), then Colima.
func macOSDockerSocketCandidates(homeDir string) []string {
	return []string{
		filepath.Join(homeDir, ".docker", "run", "docker.sock"), // Docker Desktop
		"/var/run/docker.sock", // OrbStack, symlinks
		filepath.Join(homeDir, ".colima", "default", "docker.sock"), // Colima
	}
}

// resolveDockerSocket determines the Docker socket path to use.
// If userSocket is non-empty it is returned as-is (explicit user override).
// On macOS (goos == "darwin"), candidate paths are probed in order and the
// first existing socket is returned. On Linux the default /run/docker.sock
// is returned.
func resolveDockerSocket(goos, homeDir, userSocket string) string {
	if userSocket != "" {
		return userSocket
	}

	if goos == "darwin" {
		for _, candidate := range macOSDockerSocketCandidates(homeDir) {
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
		// None found — return first candidate so error messages are clear.
		candidates := macOSDockerSocketCandidates(homeDir)
		if len(candidates) > 0 {
			return candidates[0]
		}
	}

	return defaultLinuxDockerSocket
}

// Docker provides filtered access to the Docker socket.
// Only read operations and exec/attach are allowed.
type Docker struct {
	enabled    bool
	hostSocket string
	proxy      *dockerproxy.Proxy
	logger     ErrorLogger
}

// SetLogger sets the logger for Docker proxy errors.
// Implements ToolWithLogger.
func (d *Docker) SetLogger(logger ErrorLogger) {
	d.logger = logger
}

func (d *Docker) Name() string {
	return "docker"
}

func (d *Docker) Description() string {
	if d.enabled {
		return "Docker socket proxy (read-only + exec)"
	}
	return "Docker socket proxy (disabled)"
}

func (d *Docker) Available(homeDir string) bool {
	socket := d.hostSocket
	if socket == "" {
		socket = resolveDockerSocket(runtime.GOOS, homeDir, "")
	}
	_, err := os.Stat(socket)
	return err == nil
}

// socketPath returns the path where the proxy socket will be created.
// It's placed in sandboxHome so it's visible inside the sandbox.
func (d *Docker) socketPath(sandboxHome string) string {
	return filepath.Join(sandboxHome, dockerSocketName)
}

// Configure implements ToolWithConfig.
func (d *Docker) Configure(globalCfg GlobalConfig, toolCfg map[string]any) {
	d.enabled = false
	d.hostSocket = ""

	// Parse user-provided socket first.
	var userSocket string
	if toolCfg != nil {
		if enabled, ok := toolCfg["enabled"]; ok {
			if b, ok := enabled.(bool); ok {
				d.enabled = b
			}
		}
		if socket, ok := toolCfg["socket"]; ok {
			if s, ok := socket.(string); ok && s != "" {
				userSocket = s
			}
		}
	}

	d.hostSocket = resolveDockerSocket(runtime.GOOS, globalCfg.HomeDir, userSocket)
}

func (d *Docker) Bindings(homeDir, sandboxHome string) []Binding {
	// Docker tool uses proxy, socket is in sandboxHome which is already bound
	return nil
}

func (d *Docker) Environment(homeDir, sandboxHome string) []EnvVar {
	if !d.enabled {
		return nil
	}

	// The socket is created at sandboxHome/docker.sock on the host,
	// but sandboxHome is mounted at $HOME inside the sandbox.
	// So we return $HOME/docker.sock as the path visible inside the sandbox.
	sandboxVisiblePath := filepath.Join(homeDir, dockerSocketName)
	return []EnvVar{
		{Name: "DOCKER_HOST", Value: "unix://" + sandboxVisiblePath},
	}
}

func (d *Docker) ShellInit(shell string) string {
	return ""
}

// Start implements ActiveTool.
func (d *Docker) Start(ctx context.Context, homeDir, sandboxHome string) error {
	if !d.enabled {
		return nil
	}

	fmt.Fprintln(os.Stderr, "WARNING: Docker socket proxy enabled. The sandbox can access ALL Docker containers on this host.")
	fmt.Fprintln(os.Stderr, "         This is equivalent to root access on the host. Only enable for trusted code.")

	listenPath := d.socketPath(sandboxHome)
	d.proxy = dockerproxy.New(d.hostSocket, listenPath)
	if d.logger != nil {
		d.proxy.SetLogger(d.logger)
	}
	return d.proxy.Start(ctx)
}

// Stop implements ActiveTool.
func (d *Docker) Stop() error {
	if d.proxy == nil {
		return nil
	}
	return d.proxy.Stop()
}

// Check implements ToolWithCheck.
func (d *Docker) Check(homeDir string) CheckResult {
	result := CheckResult{
		BinaryName:  "docker",
		InstallHint: "Docker socket not found. Ensure Docker is installed and running.",
	}

	// Resolve socket if not yet configured.
	socket := d.hostSocket
	if socket == "" {
		socket = resolveDockerSocket(runtime.GOOS, homeDir, "")
	}

	if _, err := os.Stat(socket); err != nil {
		result.Available = false
		result.AddIssue("Docker socket not found at " + socket)

		// On macOS, list all searched candidate paths.
		if runtime.GOOS == "darwin" {
			searched := macOSDockerSocketCandidates(homeDir)
			result.AddIssue("Searched macOS candidates: " + strings.Join(searched, ", "))
		}

		return result
	}

	result.Available = true
	result.ConfigPaths = []string{socket}

	// Add mode info
	if d.enabled {
		result.AddInfo("mode: enabled (read-only + exec)")
	} else {
		result.AddInfo("mode: disabled (add [tools.docker] enabled=true to config)")
	}

	return result
}

// Ensure interfaces are implemented
var (
	_ Tool           = (*Docker)(nil)
	_ ToolWithConfig = (*Docker)(nil)
	_ ToolWithCheck  = (*Docker)(nil)
	_ ActiveTool     = (*Docker)(nil)
	_ ToolWithLogger = (*Docker)(nil)
)
