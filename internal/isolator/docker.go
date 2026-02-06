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
	"time"

	"devsandbox/internal/sandbox/tools"
)

const (
	// DefaultImage is the default devsandbox Docker image.
	DefaultImage = "ghcr.io/zekker6/devsandbox:latest"
)

const (
	// CacheVolumeName is the shared cache volume name
	CacheVolumeName = "devsandbox-cache"
	// CacheMountPath is where the cache volume is mounted
	CacheMountPath = "/cache"
)

// Docker labels for devsandbox containers and volumes
const (
	LabelDevsandbox  = "devsandbox"
	LabelProjectDir  = "devsandbox.project_dir"
	LabelProjectName = "devsandbox.project_name"
	LabelCreatedAt   = "devsandbox.created_at"
)

// DockerAction represents what docker command to run.
type DockerAction int

const (
	DockerActionRun    DockerAction = iota // Run with --rm (old behavior)
	DockerActionCreate                     // Create new container then start
	DockerActionStart                      // Start existing stopped container
	DockerActionExec                       // Exec into running container
)

// DockerBuildResult contains the command to execute.
type DockerBuildResult struct {
	Action        DockerAction
	BinaryPath    string
	Args          []string
	ContainerName string // For create->start flow
}

// DockerConfig contains Docker-specific settings.
type DockerConfig struct {
	// Dockerfile is the path to the Dockerfile to build.
	Dockerfile string
	// ConfigDir is the devsandbox config directory (for default Dockerfile).
	ConfigDir string
	// HideEnvFiles enables .env file hiding in the container.
	HideEnvFiles bool
	// MemoryLimit is the memory limit (e.g., "4g").
	MemoryLimit string
	// CPULimit is the CPU limit (e.g., "2").
	CPULimit string
	// KeepContainer keeps the container after exit for fast restarts.
	KeepContainer bool
}

// DockerIsolator implements Isolator using Docker containers.
type DockerIsolator struct {
	config   DockerConfig
	imageTag string // set after buildImage
}

// NewDockerIsolator creates a new Docker isolator.
func NewDockerIsolator(cfg DockerConfig) *DockerIsolator {
	return &DockerIsolator{config: cfg}
}

// resolveDockerfile determines the Dockerfile path to use.
// Priority: config.Dockerfile (absolute or relative to projectDir) -> default in configDir.
// If the default doesn't exist, it auto-creates it with the default FROM line.
func (d *DockerIsolator) resolveDockerfile(projectDir, configDir string) (string, error) {
	if d.config.Dockerfile != "" {
		path := d.config.Dockerfile
		if !filepath.IsAbs(path) {
			path = filepath.Join(projectDir, path)
		}
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("dockerfile not found: %s", path)
		}
		return path, nil
	}

	// Default: configDir/Dockerfile
	defaultPath := filepath.Join(configDir, "Dockerfile")
	if _, err := os.Stat(defaultPath); os.IsNotExist(err) {
		if err := os.MkdirAll(configDir, 0o755); err != nil {
			return "", fmt.Errorf("failed to create config dir: %w", err)
		}
		content := fmt.Sprintf("FROM %s\n", DefaultImage)
		if err := os.WriteFile(defaultPath, []byte(content), 0o644); err != nil {
			return "", fmt.Errorf("failed to create default Dockerfile: %w", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("failed to check Dockerfile: %w", err)
	}

	return defaultPath, nil
}

// determineImageTag returns the Docker image tag for the build.
func (d *DockerIsolator) determineImageTag(dockerfilePath, configDir, projectDir string) string {
	if strings.HasPrefix(dockerfilePath, configDir) {
		return "devsandbox:local"
	}
	projectName := filepath.Base(projectDir)
	hash := sha256.Sum256([]byte(projectDir))
	return fmt.Sprintf("devsandbox:%s-%x", projectName, hash[:4])
}

// buildImage builds a Docker image from the resolved Dockerfile.
func (d *DockerIsolator) buildImage(dockerfilePath, imageTag string) error {
	buildContext := filepath.Dir(dockerfilePath)
	cmd := exec.Command("docker", "build",
		"-t", imageTag,
		"-f", dockerfilePath,
		buildContext,
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to build Docker image: %w", err)
	}
	return nil
}

// ResolveAndBuild resolves the Dockerfile and builds the image. Returns the image tag.
func (d *DockerIsolator) ResolveAndBuild(projectDir string) (string, error) {
	dockerfilePath, err := d.resolveDockerfile(projectDir, d.config.ConfigDir)
	if err != nil {
		return "", err
	}
	imageTag := d.determineImageTag(dockerfilePath, d.config.ConfigDir, projectDir)
	fmt.Fprintf(os.Stderr, "Building image %s...\n", imageTag)
	if err := d.buildImage(dockerfilePath, imageTag); err != nil {
		return "", err
	}
	return imageTag, nil
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

// Build implements the Isolator interface but should not be used directly for Docker.
// Use BuildDocker() instead which returns DockerBuildResult with lifecycle information.
func (d *DockerIsolator) Build(ctx context.Context, cfg *Config) (string, []string, error) {
	// For backwards compatibility, delegate to BuildDocker and return run args
	result, err := d.BuildDocker(ctx, cfg)
	if err != nil {
		return "", nil, err
	}
	return result.BinaryPath, result.Args, nil
}

// BuildDocker constructs the docker command based on container state.
// Returns a DockerBuildResult that indicates what action to take.
func (d *DockerIsolator) BuildDocker(ctx context.Context, cfg *Config) (*DockerBuildResult, error) {
	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		return nil, fmt.Errorf("docker CLI not found: %w", err)
	}

	// Resolve Dockerfile and build image
	dockerfilePath, err := d.resolveDockerfile(cfg.ProjectDir, d.config.ConfigDir)
	if err != nil {
		return nil, err
	}
	imageTag := d.determineImageTag(dockerfilePath, d.config.ConfigDir, cfg.ProjectDir)
	fmt.Fprintf(os.Stderr, "Building image %s...\n", imageTag)
	if err := d.buildImage(dockerfilePath, imageTag); err != nil {
		return nil, err
	}
	d.imageTag = imageTag

	containerName := d.containerName(cfg.ProjectDir)

	// Check if we should keep containers
	if !d.config.KeepContainer {
		// Old behavior: run with --rm
		args, err := d.buildRunArgs(cfg)
		if err != nil {
			return nil, err
		}
		return &DockerBuildResult{
			Action:     DockerActionRun,
			BinaryPath: dockerPath,
			Args:       args,
		}, nil
	}

	// Check container state
	exists, running := d.getContainerState(containerName)

	if running {
		// Container is running - exec into it
		args := []string{"exec"}
		if cfg.Interactive {
			args = append(args, "-it")
		}
		// Run as the host user's UID:GID to match file permissions
		args = append(args, "-u", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
		args = append(args, containerName)
		if len(cfg.Command) > 0 {
			args = append(args, cfg.Command...)
		} else {
			args = append(args, cfg.Shell)
		}
		return &DockerBuildResult{
			Action:        DockerActionExec,
			BinaryPath:    dockerPath,
			Args:          args,
			ContainerName: containerName,
		}, nil
	}

	if exists {
		// Container exists but stopped - start it in background, then exec into it.
		// We can't use "docker start -ai" because it would run the original command
		// that was used when creating the container, not the current command.
		// Instead, start the container (which runs the shell), then exec into it.
		startCmd := exec.Command(dockerPath, "start", containerName)
		if err := startCmd.Run(); err != nil {
			return nil, fmt.Errorf("failed to start container: %w", err)
		}

		// Now exec into the running container
		args := []string{"exec"}
		if cfg.Interactive {
			args = append(args, "-it")
		}
		// Run as the host user's UID:GID to match file permissions
		args = append(args, "-u", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
		args = append(args, containerName)
		if len(cfg.Command) > 0 {
			args = append(args, cfg.Command...)
		} else {
			args = append(args, cfg.Shell)
		}
		return &DockerBuildResult{
			Action:        DockerActionExec,
			BinaryPath:    dockerPath,
			Args:          args,
			ContainerName: containerName,
		}, nil
	}

	// Container doesn't exist - create it
	args, err := d.buildCreateArgs(cfg, containerName)
	if err != nil {
		return nil, err
	}
	return &DockerBuildResult{
		Action:        DockerActionCreate,
		BinaryPath:    dockerPath,
		Args:          args,
		ContainerName: containerName,
	}, nil
}

// buildRunArgs builds arguments for docker run with --rm.
func (d *DockerIsolator) buildRunArgs(cfg *Config) ([]string, error) {
	args := []string{
		"run",
		"--rm",
	}

	// Interactive mode only if stdin is a TTY
	if cfg.Interactive {
		args = append(args, "-it")
	}

	args = append(args, "--hostname", "sandbox")

	// Add common args
	commonArgs, err := d.buildCommonArgs(cfg)
	if err != nil {
		return nil, err
	}
	args = append(args, commonArgs...)

	// Image
	args = append(args, d.imageTag)

	// Command
	if len(cfg.Command) > 0 {
		args = append(args, cfg.Command...)
	} else {
		// Interactive shell
		args = append(args, cfg.Shell)
	}

	return args, nil
}

// buildCreateArgs builds arguments for docker create.
func (d *DockerIsolator) buildCreateArgs(cfg *Config, containerName string) ([]string, error) {
	args := []string{"create", "--name", containerName}

	// Add labels
	args = append(args, d.buildLabels(cfg.ProjectDir)...)

	// Always create with -it to support both interactive and non-interactive use.
	// The container may be reused later for interactive sessions even if initially
	// created for a non-interactive command. TTY allocation at creation time is
	// required for interactive shells to work properly.
	args = append(args, "-it")

	args = append(args, "--hostname", "sandbox")

	// Add common args
	commonArgs, err := d.buildCommonArgs(cfg)
	if err != nil {
		return nil, err
	}
	args = append(args, commonArgs...)

	// Image
	args = append(args, d.imageTag)

	// Always create with just the shell (no command arguments).
	// This ensures the container stays running and can be reused for any command.
	// Actual commands are executed via "docker exec" after the container is started.
	args = append(args, cfg.Shell)

	return args, nil
}

// buildCommonArgs builds arguments common to both run and create.
func (d *DockerIsolator) buildCommonArgs(cfg *Config) ([]string, error) {
	var args []string

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

	// Shared cache volume for tools (mise, go, etc.)
	cacheMounts := tools.CollectCacheMounts(cfg.HomeDir)
	if len(cacheMounts) > 0 {
		args = append(args, "-v", CacheVolumeName+":"+CacheMountPath)
		for _, cm := range cacheMounts {
			args = append(args, "-e", cm.EnvVar+"="+cm.FullPath())
		}
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
	args = append(args, "-e", "XDG_STATE_HOME=/home/sandboxuser/.local/state")

	// Fish shell data directory - must be set before fish starts
	// to ensure universal variables are stored in the right location
	args = append(args, "-e", "__fish_user_data_dir=/home/sandboxuser/.local/share/fish")

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

		// Set HTTP_PROXY env vars directly so they're available in all processes
		// (not just the entrypoint shell). This ensures curl, mise, etc. use the proxy.
		proxyURL := fmt.Sprintf("http://%s:%d", proxyHost, cfg.ProxyPort)
		args = append(args, "-e", fmt.Sprintf("HTTP_PROXY=%s", proxyURL))
		args = append(args, "-e", fmt.Sprintf("HTTPS_PROXY=%s", proxyURL))
		args = append(args, "-e", fmt.Sprintf("http_proxy=%s", proxyURL))
		args = append(args, "-e", fmt.Sprintf("https_proxy=%s", proxyURL))
		args = append(args, "-e", "no_proxy=localhost,127.0.0.1")

		// Mount CA certificate for HTTPS MITM and set SSL_CERT_FILE
		if cfg.ProxyCAPath != "" {
			caDest := "/etc/ssl/certs/devsandbox-ca.crt"
			args = append(args, "-v", fmt.Sprintf("%s:%s:ro", cfg.ProxyCAPath, caDest))
			args = append(args, "-e", fmt.Sprintf("SSL_CERT_FILE=%s", caDest))
			// Also set for Node.js which uses its own env var
			args = append(args, "-e", fmt.Sprintf("NODE_EXTRA_CA_CERTS=%s", caDest))
		}
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
			return nil, fmt.Errorf("binding source does not exist: %s", b.Source)
		}
		mount := b.Source + ":" + b.Dest
		if b.ReadOnly {
			mount += ":ro"
		}
		args = append(args, "-v", mount)
	}

	return args, nil
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
// Paths under the project directory keep their original path (project is mounted
// at its host path for PWD consistency). Other paths under homeDir are remapped
// to /home/sandboxuser.
func (d *DockerIsolator) remapToContainerHome(hostPath, homeDir, projectDir string) string {
	// Paths under project directory stay the same - project is mounted at host path
	if projectDir != "" && strings.HasPrefix(hostPath, projectDir+"/") {
		return hostPath
	}
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
			// DEBUG: log which tools have DockerBindings
			debugLog("/tmp/devsandbox-debug.log", "Tool %s using DockerBindings", tool.Name())
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
			// DEBUG: log which tools use regular Bindings
			debugLog("/tmp/devsandbox-debug.log", "Tool %s using regular Bindings", tool.Name())
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
					// Paths under project dir stay unchanged (project mounted at host path)
					dest = d.remapToContainerHome(b.Source, cfg.HomeDir, cfg.ProjectDir)
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

// debugLog writes a debug message to the specified log file.
// This is a temporary helper for debugging tool binding issues.
func debugLog(path, format string, args ...any) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	msg := fmt.Sprintf(format, args...)
	timestamp := time.Now().Format(time.RFC3339)
	_, _ = fmt.Fprintf(f, "%s DEBUG %s\n", timestamp, msg)
}

// containerName generates a Docker container name for the sandbox.
// Format: devsandbox-<project>-<hash>
func (d *DockerIsolator) containerName(projectDir string) string {
	projectName := filepath.Base(projectDir)
	hash := sha256.Sum256([]byte(projectDir))
	return fmt.Sprintf("devsandbox-%s-%x", projectName, hash[:4])
}

// getContainerState checks if a container exists and its state.
func (d *DockerIsolator) getContainerState(name string) (exists bool, running bool) {
	cmd := exec.Command("docker", "inspect", "--format", "{{.State.Running}}", name)
	output, err := cmd.Output()
	if err != nil {
		return false, false // Container doesn't exist
	}

	isRunning := strings.TrimSpace(string(output)) == "true"
	return true, isRunning
}

// buildLabels returns Docker label arguments for a container/volume.
func (d *DockerIsolator) buildLabels(projectDir string) []string {
	projectName := filepath.Base(projectDir)
	return []string{
		"--label", LabelDevsandbox + "=true",
		"--label", LabelProjectDir + "=" + projectDir,
		"--label", LabelProjectName + "=" + projectName,
		"--label", LabelCreatedAt + "=" + time.Now().Format(time.RFC3339),
	}
}
