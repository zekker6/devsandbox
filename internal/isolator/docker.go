package isolator

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"devsandbox/internal/sandbox/tools"
)

const (
	// DefaultImage is the default devsandbox Docker image.
	DefaultImage = "ghcr.io/zekker6/devsandbox:latest"
)

// DockerConfig contains Docker-specific settings.
type DockerConfig struct {
	// Image is the Docker image to use.
	Image string
	// PullPolicy controls when to pull the image: "always", "missing", "never".
	PullPolicy string
	// HideEnvFiles enables .env file hiding in the container.
	HideEnvFiles bool
	// MemoryLimit is the memory limit (e.g., "4g").
	MemoryLimit string
	// CPULimit is the CPU limit (e.g., "2").
	CPULimit string
}

// DockerIsolator implements Isolator using Docker containers.
type DockerIsolator struct {
	config DockerConfig
}

// NewDockerIsolator creates a new Docker isolator with sensible defaults.
func NewDockerIsolator(cfg DockerConfig) *DockerIsolator {
	if cfg.Image == "" {
		cfg.Image = DefaultImage
	}
	if cfg.PullPolicy == "" {
		cfg.PullPolicy = "missing"
	}
	return &DockerIsolator{config: cfg}
}

// Name returns the backend name.
func (d *DockerIsolator) Name() Backend {
	return BackendDocker
}

// Available checks if Docker CLI and daemon are available.
func (d *DockerIsolator) Available() error {
	_, err := exec.LookPath("docker")
	if err != nil {
		return errors.New("docker CLI is not installed\n" +
			"Install Docker Desktop: https://docs.docker.com/get-docker/")
	}

	// Check if daemon is running
	cmd := exec.Command("docker", "info")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return errors.New("docker daemon is not running\n" +
			"Start Docker Desktop or run: sudo systemctl start docker")
	}

	return nil
}

// Build constructs the docker run command with all necessary arguments.
func (d *DockerIsolator) Build(ctx context.Context, cfg *Config) (string, []string, error) {
	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		return "", nil, fmt.Errorf("docker CLI not found: %w", err)
	}

	args := []string{
		"run",
		"--rm",
	}

	// Interactive mode only if stdin is a TTY
	if cfg.Interactive {
		args = append(args, "-it")
	}

	args = append(args, "--hostname", "sandbox")

	// Pull policy
	args = append(args, "--pull", d.config.PullPolicy)

	// User mapping - pass host UID/GID for entrypoint to use
	args = append(args,
		"-e", fmt.Sprintf("HOST_UID=%d", os.Getuid()),
		"-e", fmt.Sprintf("HOST_GID=%d", os.Getgid()),
	)

	// Working directory - match host PWD
	args = append(args, "-w", cfg.ProjectDir)

	// Project mount - same path as host for PWD consistency
	args = append(args, "-v", cfg.ProjectDir+":"+cfg.ProjectDir)

	// Sandbox home - platform specific
	if runtime.GOOS == "darwin" {
		// Named volume on macOS for better performance
		volumeName := d.sandboxVolumeName(cfg.SandboxHome)
		args = append(args, "-v", volumeName+":/home/sandboxuser")
	} else {
		// Bind mount on Linux for consistency with bwrap
		args = append(args, "-v", cfg.SandboxHome+":/home/sandboxuser")
	}

	// Tool bindings (nvim, mise, git, starship, etc.)
	toolMounts, toolEnvVars := d.getToolBindings(cfg)
	for _, mount := range toolMounts {
		args = append(args, "-v", mount)
	}
	for _, env := range toolEnvVars {
		args = append(args, "-e", env)
	}

	// Standard devsandbox environment variables
	args = append(args, "-e", "DEVSANDBOX=1")
	if cfg.ProjectDir != "" {
		// Extract project name from path
		projectName := filepath.Base(cfg.ProjectDir)
		args = append(args, "-e", "DEVSANDBOX_PROJECT="+projectName)
		// Pass project dir for entrypoint
		args = append(args, "-e", "PROJECT_DIR="+cfg.ProjectDir)
	}
	args = append(args, "-e", "GOTOOLCHAIN=local")

	// XDG directories inside container
	args = append(args, "-e", "XDG_CONFIG_HOME=/home/sandboxuser/.config")
	args = append(args, "-e", "XDG_DATA_HOME=/home/sandboxuser/.local/share")
	args = append(args, "-e", "XDG_CACHE_HOME=/home/sandboxuser/.cache")

	// User-provided environment variables
	for k, v := range cfg.Environment {
		args = append(args, "-e", k+"="+v)
	}

	// .env hiding
	if cfg.HideEnvFiles {
		args = append(args, "-e", "HIDE_ENV_FILES=true")
	}

	// On Linux, add host.docker.internal mapping for proxy access
	if runtime.GOOS == "linux" {
		args = append(args, "--add-host", "host.docker.internal:host-gateway")
	}

	// Proxy mode
	if cfg.ProxyEnabled {
		args = append(args, "-e", "PROXY_MODE=true")
		args = append(args, "-e", "DEVSANDBOX_PROXY=1")
		proxyHost := cfg.ProxyHost
		if proxyHost == "" {
			proxyHost = d.proxyHost()
		}
		args = append(args, "-e", fmt.Sprintf("PROXY_HOST=%s", proxyHost))
		args = append(args, "-e", fmt.Sprintf("PROXY_PORT=%d", cfg.ProxyPort))
	}

	// Resource limits
	if d.config.MemoryLimit != "" {
		args = append(args, "--memory", d.config.MemoryLimit)
	}
	if d.config.CPULimit != "" {
		args = append(args, "--cpus", d.config.CPULimit)
	}

	// Read-only bindings (host configs)
	for _, b := range cfg.Bindings {
		if _, err := os.Stat(b.Source); os.IsNotExist(err) {
			if b.Optional {
				continue
			}
			return "", nil, fmt.Errorf("binding source does not exist: %s", b.Source)
		}
		mount := b.Source + ":" + b.Dest
		if b.ReadOnly {
			mount += ":ro"
		}
		args = append(args, "-v", mount)
	}

	// Image
	args = append(args, d.config.Image)

	// Command
	if len(cfg.Command) > 0 {
		args = append(args, cfg.Command...)
	} else {
		// Interactive shell
		args = append(args, cfg.Shell)
	}

	return dockerPath, args, nil
}

// Cleanup performs any post-sandbox cleanup.
// DockerIsolator has no cleanup requirements as --rm handles container removal.
func (d *DockerIsolator) Cleanup() error {
	return nil
}

// proxyHost returns the host address for proxy connections from within the container.
func (d *DockerIsolator) proxyHost() string {
	// host.docker.internal works on macOS natively and on Linux with --add-host
	return "host.docker.internal"
}

// sandboxVolumeName generates a Docker volume name for the sandbox home.
// Uses a hash of the sandbox home path for uniqueness.
func (d *DockerIsolator) sandboxVolumeName(sandboxHome string) string {
	hash := sha256.Sum256([]byte(sandboxHome))
	return fmt.Sprintf("devsandbox-%x", hash[:8])
}

// containerHome is the home directory inside the Docker container.
const containerHome = "/home/sandboxuser"

// remapToContainerHome converts a host path to its equivalent container path.
// Paths under homeDir are remapped to /home/sandboxuser.
func (d *DockerIsolator) remapToContainerHome(hostPath, homeDir string) string {
	// Check if path is under home directory
	if strings.HasPrefix(hostPath, homeDir) {
		// Replace home prefix with container home
		relPath := strings.TrimPrefix(hostPath, homeDir)
		return containerHome + relPath
	}
	// Non-home paths stay the same
	return hostPath
}

// getToolBindings retrieves bindings from registered tools and converts them
// to Docker volume mount strings.
func (d *DockerIsolator) getToolBindings(cfg *Config) (mounts []string, envVars []string) {
	globalCfg := tools.GlobalConfig{
		OverlayEnabled: cfg.OverlayEnabled,
		ProjectDir:     cfg.ProjectDir,
	}

	for _, tool := range tools.Available(cfg.HomeDir) {
		// Configure tool if it supports configuration
		if configurable, ok := tool.(tools.ToolWithConfig); ok {
			toolCfg := getToolConfig(cfg.ToolsConfig, tool.Name())
			configurable.Configure(globalCfg, toolCfg)
		}

		// Run setup if tool requires it (e.g., generate safe gitconfig)
		if setupTool, ok := tool.(tools.ToolWithSetup); ok {
			_ = setupTool.Setup(cfg.HomeDir, cfg.SandboxHome)
		}

		// Get Docker-specific bindings if available
		var toolBindings []tools.Binding
		if dockerTool, ok := tool.(tools.ToolWithDocker); ok {
			dockerMounts := dockerTool.DockerBindings(cfg.HomeDir, cfg.SandboxHome)
			for _, m := range dockerMounts {
				if m.Source == "" {
					continue
				}
				if _, err := os.Stat(m.Source); os.IsNotExist(err) {
					continue // Skip non-existent optional paths
				}
				mount := m.Source + ":" + m.Dest
				if m.ReadOnly {
					mount += ":ro"
				}
				mounts = append(mounts, mount)
			}
		} else {
			// Convert regular bindings to Docker mounts
			toolBindings = tool.Bindings(cfg.HomeDir, cfg.SandboxHome)
			for _, b := range toolBindings {
				if b.Source == "" {
					continue
				}
				if _, err := os.Stat(b.Source); os.IsNotExist(err) {
					if b.Optional {
						continue
					}
				}
				dest := b.Dest
				if dest == "" {
					// Remap home directory paths to /home/sandboxuser
					dest = d.remapToContainerHome(b.Source, cfg.HomeDir)
				}
				mount := b.Source + ":" + dest
				if b.ReadOnly {
					mount += ":ro"
				}
				mounts = append(mounts, mount)
			}
		}

		// Get environment variables from tool
		for _, env := range tool.Environment(cfg.HomeDir, cfg.SandboxHome) {
			if env.FromHost {
				if val := os.Getenv(env.Name); val != "" {
					envVars = append(envVars, env.Name+"="+val)
				}
			} else if env.Value != "" {
				envVars = append(envVars, env.Name+"="+env.Value)
			}
		}
	}

	return mounts, envVars
}

// getToolConfig extracts tool-specific config from the tools map.
func getToolConfig(toolsConfig map[string]any, toolName string) map[string]any {
	if toolsConfig == nil {
		return nil
	}
	if cfg, ok := toolsConfig[toolName]; ok {
		if m, ok := cfg.(map[string]any); ok {
			return m
		}
	}
	return nil
}
