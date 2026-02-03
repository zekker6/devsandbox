package tools

import (
	"context"
	"os"
	"path/filepath"

	"devsandbox/internal/dockerproxy"
)

func init() {
	Register(&Docker{})
}

const (
	defaultDockerSocket = "/run/docker.sock"
	dockerSocketName    = "docker.sock"
)

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
		socket = defaultDockerSocket
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
	d.hostSocket = defaultDockerSocket

	if toolCfg == nil {
		return
	}

	// Check enabled
	if enabled, ok := toolCfg["enabled"]; ok {
		if b, ok := enabled.(bool); ok {
			d.enabled = b
		}
	}

	// Custom socket path
	if socket, ok := toolCfg["socket"]; ok {
		if s, ok := socket.(string); ok && s != "" {
			d.hostSocket = s
		}
	}
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

	// Check if socket exists
	if d.hostSocket == "" {
		d.hostSocket = defaultDockerSocket
	}

	if _, err := os.Stat(d.hostSocket); err != nil {
		result.Available = false
		result.AddIssue("Docker socket not found at " + d.hostSocket)
		return result
	}

	result.Available = true
	result.ConfigPaths = []string{d.hostSocket}

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
