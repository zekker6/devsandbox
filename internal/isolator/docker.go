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
	"sort"
	"strings"
	"time"

	"devsandbox/internal/logging"
	"devsandbox/internal/proxy"
	"devsandbox/internal/sandbox"
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
	LabelNetworkName = "devsandbox.network_name"
	LabelConfigHash  = "devsandbox.config_hash"
)

// DockerAction represents what docker command to run.
type DockerAction int

const (
	DockerActionRun    DockerAction = iota // Run with --rm (old behavior)
	DockerActionCreate                     // Create new container then start
	_                                      // reserved (formerly DockerActionStart)
	DockerActionExec                       // Exec into running container
)

// DockerBuildResult contains the command to execute.
type DockerBuildResult struct {
	Action               DockerAction
	BinaryPath           string
	Args                 []string
	ContainerName        string // For create->start flow
	ContainerJustStarted bool   // True when a stopped container was just started (needs readiness wait)
}

// DockerConfig contains Docker-specific settings.
type DockerConfig struct {
	// Dockerfile is the path to the Dockerfile to build.
	Dockerfile string
	// ConfigDir is the devsandbox config directory (for default Dockerfile).
	ConfigDir string
	// MemoryLimit is the memory limit (e.g., "4g").
	MemoryLimit string
	// CPULimit is the CPU limit (e.g., "2").
	CPULimit string
	// KeepContainer keeps the container after exit for fast restarts.
	KeepContainer bool
}

// DockerIsolator implements Isolator using Docker containers.
type DockerIsolator struct {
	config      DockerConfig
	imageTag    string // set after buildImage
	networkName string // per-session Docker network
	gatewayIP   string // per-session network gateway IP (proxy bind address)
	logger      *logging.ComponentLogger
}

// SetLogger configures the logger for the Docker isolator.
func (d *DockerIsolator) SetLogger(l *logging.ComponentLogger) {
	d.logger = l
}

// logInfo logs an informational message to stderr and the logger.
func (d *DockerIsolator) logInfo(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(os.Stderr, msg)
	if d.logger != nil {
		d.logger.Infof("%s", msg)
	}
}

// logWarn logs a warning message to stderr and the logger.
func (d *DockerIsolator) logWarn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stderr, "warning: %s\n", msg)
	if d.logger != nil {
		d.logger.Warnf("%s", msg)
	}
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
func (d *DockerIsolator) buildImage(ctx context.Context, dockerfilePath, imageTag string) error {
	buildContext := filepath.Dir(dockerfilePath)
	cmd := exec.CommandContext(ctx, "docker", "build",
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
func (d *DockerIsolator) ResolveAndBuild(ctx context.Context, projectDir string) (string, error) {
	dockerfilePath, err := d.resolveDockerfile(projectDir, d.config.ConfigDir)
	if err != nil {
		return "", err
	}
	imageTag := d.determineImageTag(dockerfilePath, d.config.ConfigDir, projectDir)
	d.logInfo("Building image %s...", imageTag)
	if err := d.buildImage(ctx, dockerfilePath, imageTag); err != nil {
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

// IsolationType returns the sandbox isolation type for metadata.
func (d *DockerIsolator) IsolationType() sandbox.IsolationType {
	return sandbox.IsolationDocker
}

// PrepareNetwork creates a per-session Docker network for proxy isolation
// and returns the network gateway IP for the proxy to bind to.
func (d *DockerIsolator) PrepareNetwork(ctx context.Context, projectDir string) (*NetworkInfo, error) {
	hash := sha256.Sum256([]byte(projectDir))
	networkName := fmt.Sprintf("devsandbox-net-%x", hash[:4])
	createNet := exec.CommandContext(ctx, "docker", "network", "create", networkName)
	if output, err := createNet.CombinedOutput(); err != nil {
		// Ignore "already exists" errors — network may persist from a previous session
		if !strings.Contains(string(output), "already exists") {
			d.logWarn("failed to create network %s: %v", networkName, err)
		}
	}
	d.networkName = networkName

	// Get the gateway IP from the network for proxy binding.
	// Never fall back to the Docker bridge IP — that's accessible to all containers.
	gatewayCmd := exec.CommandContext(ctx, "docker", "network", "inspect", networkName, "--format", "{{(index .IPAM.Config 0).Gateway}}")
	gatewayOut, err := gatewayCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to determine proxy bind address for network %s: %w\nEnsure Docker networking is functioning correctly", networkName, err)
	}
	bindAddress := strings.TrimSpace(string(gatewayOut))
	if bindAddress == "" {
		return nil, fmt.Errorf("docker network %s has no gateway IP; cannot bind proxy securely", networkName)
	}
	d.gatewayIP = bindAddress

	return &NetworkInfo{BindAddress: bindAddress}, nil
}

// Run executes the full Docker sandbox lifecycle.
func (d *DockerIsolator) Run(ctx context.Context, cfg *RunConfig) error {
	sandboxCfg := cfg.SandboxCfg

	// Set up logger for Docker isolator
	logDir := filepath.Join(sandboxCfg.SandboxHome, proxy.LogBaseDirName, proxy.InternalLogDirName)
	dockerLogger, _ := logging.NewErrorLogger(filepath.Join(logDir, "docker.log"))
	d.SetLogger(logging.NewComponentLogger("docker", dockerLogger, cfg.LogDispatcher))

	// Build isolator config from RunConfig
	isoCfg := &Config{
		ProjectDir:     sandboxCfg.ProjectDir,
		SandboxHome:    sandboxCfg.SandboxHome,
		HomeDir:        sandboxCfg.HomeDir,
		Shell:          string(sandboxCfg.Shell),
		ShellPath:      sandboxCfg.ShellPath,
		Command:        cfg.Command,
		Interactive:    cfg.Interactive,
		ProxyEnabled:   sandboxCfg.ProxyEnabled,
		ProxyPort:      cfg.ProxyPort,
		Environment:    make(map[string]string),
		ToolsConfig:    sandboxCfg.ToolsConfig,
		OverlayEnabled: sandboxCfg.OverlayEnabled,
	}

	// Add CA path if proxy is enabled
	if sandboxCfg.ProxyEnabled && cfg.ProxyServer != nil {
		isoCfg.ProxyCAPath = cfg.ProxyServer.Config().CACertPath
	}

	// Build command
	result, err := d.BuildDocker(ctx, isoCfg)
	if err != nil {
		return err
	}

	readinessTimeout := 90 * time.Second
	// Handle different actions
	switch result.Action {
	case DockerActionRun:
		cmd := exec.Command(result.BinaryPath, result.Args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()

	case DockerActionCreate:
		fmt.Fprint(os.Stderr, "Creating container...")
		createCmd := exec.Command(result.BinaryPath, result.Args...)
		if out, err := createCmd.CombinedOutput(); err != nil {
			fmt.Fprintln(os.Stderr, " failed")
			return fmt.Errorf("failed to create container: %s", strings.TrimSpace(string(out)))
		}
		fmt.Fprintln(os.Stderr, " done")

		fmt.Fprint(os.Stderr, "Starting container...")
		startCmd := exec.Command(result.BinaryPath, "start", result.ContainerName)
		if out, err := startCmd.CombinedOutput(); err != nil {
			fmt.Fprintln(os.Stderr, " failed")
			return fmt.Errorf("failed to start container: %s", strings.TrimSpace(string(out)))
		}
		fmt.Fprintln(os.Stderr, " done")

		fmt.Fprint(os.Stderr, "Waiting for setup...")
		if err := d.waitForContainerReady(result.BinaryPath, result.ContainerName, readinessTimeout); err != nil {
			fmt.Fprintln(os.Stderr, " timeout")
			return fmt.Errorf("container setup timed out after %s", readinessTimeout)
		}
		fmt.Fprintln(os.Stderr, " ready")

		if err := d.installMiseTools(result.BinaryPath, result.ContainerName, isoCfg); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to install tools: %v\n", err)
		}

		return d.execIntoContainer(result.BinaryPath, result.ContainerName, isoCfg.Interactive, isoCfg.Shell, cfg.Command)

	case DockerActionExec:
		if result.ContainerJustStarted {
			fmt.Fprint(os.Stderr, "Waiting for container setup...")
			if err := d.waitForContainerReady(result.BinaryPath, result.ContainerName, readinessTimeout); err != nil {
				fmt.Fprintln(os.Stderr, " timeout")
				return fmt.Errorf("container startup failed: %w", err)
			}
			fmt.Fprintln(os.Stderr, " ready")
		}

		if err := d.installMiseTools(result.BinaryPath, result.ContainerName, isoCfg); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to install tools: %v\n", err)
		}

		return d.execIntoContainer(result.BinaryPath, result.ContainerName, isoCfg.Interactive, isoCfg.Shell, cfg.Command)

	default:
		return fmt.Errorf("unexpected docker action: %d", result.Action)
	}
}

// execIntoContainer runs docker exec into a container with the given command.
func (d *DockerIsolator) execIntoContainer(dockerBinary, containerName string, interactive bool, shell string, userArgs []string) error {
	execArgs := []string{"exec"}
	if interactive {
		execArgs = append(execArgs, "-it")
	} else {
		execArgs = append(execArgs, "-i")
	}
	execArgs = append(execArgs, "-u", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
	execArgs = append(execArgs, containerName)
	if len(userArgs) > 0 {
		execArgs = append(execArgs, userArgs...)
	} else {
		execArgs = append(execArgs, shell)
	}
	cmd := exec.Command(dockerBinary, execArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// waitForContainerReady polls for the ready sentinel file inside the container.
func (d *DockerIsolator) waitForContainerReady(dockerBinary, containerName string, timeout time.Duration) error {
	const readySentinel = "/tmp/.devsandbox-ready"
	timeoutCh := time.After(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeoutCh:
			return fmt.Errorf("container %s did not become ready within %s", containerName, timeout)
		case <-ticker.C:
			check := exec.Command(dockerBinary, "exec", containerName, "test", "-f", readySentinel)
			if check.Run() == nil {
				return nil
			}
		}
	}
}

// installMiseTools installs mise tools if the project has a mise config file.
func (d *DockerIsolator) installMiseTools(dockerBinary, containerName string, cfg *Config) error {
	miseToml := filepath.Join(cfg.ProjectDir, ".mise.toml")
	toolVersions := filepath.Join(cfg.ProjectDir, ".tool-versions")

	hasMiseConfig := false
	if _, err := os.Stat(miseToml); err == nil {
		hasMiseConfig = true
	}
	if _, err := os.Stat(toolVersions); err == nil {
		hasMiseConfig = true
	}

	if !hasMiseConfig {
		return nil
	}

	userSpec := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	checkArgs := []string{
		"exec",
		"-u", userSpec,
		"-e", "MISE_GLOBAL_CONFIG_FILE=/dev/null",
		"--workdir", cfg.ProjectDir,
		containerName,
		"mise", "ls", "--missing",
	}
	checkCmd := exec.Command(dockerBinary, checkArgs...)
	output, err := checkCmd.Output()
	if err == nil && len(strings.TrimSpace(string(output))) == 0 {
		return nil
	}

	fmt.Fprintln(os.Stderr, "Installing tools...")
	installArgs := []string{
		"exec",
		"-u", userSpec,
		"-e", "MISE_GLOBAL_CONFIG_FILE=/dev/null",
		"--workdir", cfg.ProjectDir,
		containerName,
		"mise", "install", "-y",
	}

	installCmd := exec.Command(dockerBinary, installArgs...)
	installCmd.Stdout = os.Stderr
	installCmd.Stderr = os.Stderr

	if err := installCmd.Run(); err != nil {
		return fmt.Errorf("mise install failed: %w", err)
	}

	return nil
}

// BuildDocker constructs the docker command based on container state.
// Returns a DockerBuildResult that indicates what action to take.
func (d *DockerIsolator) BuildDocker(ctx context.Context, cfg *Config) (*DockerBuildResult, error) {
	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		return nil, fmt.Errorf("docker CLI not found: %w", err)
	}

	// Resolve Dockerfile and determine image tag
	dockerfilePath, err := d.resolveDockerfile(cfg.ProjectDir, d.config.ConfigDir)
	if err != nil {
		return nil, err
	}
	imageTag := d.determineImageTag(dockerfilePath, d.config.ConfigDir, cfg.ProjectDir)
	d.imageTag = imageTag

	containerName := d.containerName(cfg.ProjectDir)

	// When KeepContainer is enabled, check if an existing container can be
	// reused before paying the cost of an image build.
	if d.config.KeepContainer {
		exists, running := d.getContainerState(ctx, containerName)

		if exists {
			// Check if the container's config still matches what we need.
			// Settings like proxy, network, and resource limits are baked at
			// creation time and cannot be changed via docker exec.
			wantHash := d.configHash(cfg)
			haveHash := d.getContainerConfigHash(ctx, containerName)

			if haveHash != wantHash {
				d.logInfo("Container config changed (have=%s want=%s), recreating...", haveHash, wantHash)
				d.removeContainer(ctx, containerName)
				// Fall through to image build + create path below.
			} else {
				if !running {
					// Container exists but stopped — start it.
					// If start fails (e.g., its Docker network was removed),
					// remove the stale container and fall through to recreate.
					startCmd := exec.CommandContext(ctx, dockerPath, "start", containerName)
					if out, err := startCmd.CombinedOutput(); err != nil {
						d.logWarn("failed to restart container, recreating: %s", strings.TrimSpace(string(out)))
						d.removeContainer(ctx, containerName)
						// Fall through to image build + create below.
					} else {
						return &DockerBuildResult{
							Action:               DockerActionExec,
							BinaryPath:           dockerPath,
							Args:                 buildExecArgs(cfg, containerName),
							ContainerName:        containerName,
							ContainerJustStarted: true,
						}, nil
					}
				} else {
					return &DockerBuildResult{
						Action:        DockerActionExec,
						BinaryPath:    dockerPath,
						Args:          buildExecArgs(cfg, containerName),
						ContainerName: containerName,
					}, nil
				}
			}
		}
	}

	// No reusable container — build the image before creating/running.
	if err := d.buildImage(ctx, dockerfilePath, imageTag); err != nil {
		return nil, err
	}

	if d.config.KeepContainer {
		// Create a persistent container.
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

	// Ephemeral mode — docker run --rm.
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

// buildExecArgs builds arguments for docker exec into a running container.
func buildExecArgs(cfg *Config, containerName string) []string {
	args := []string{"exec"}
	if cfg.Interactive {
		args = append(args, "-it")
	}
	args = append(args, "-u", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
	args = append(args, containerName)
	if len(cfg.Command) > 0 {
		args = append(args, cfg.Command...)
	} else {
		args = append(args, cfg.Shell)
	}
	return args
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

	// Command — use shell name (not host path) so the container's PATH
	// resolves it. Host path (e.g., /bin/fish on Arch) may not match the
	// container path (e.g., /usr/bin/fish on Debian).
	if len(cfg.Command) > 0 {
		args = append(args, cfg.Command...)
	} else {
		args = append(args, cfg.Shell)
	}

	return args, nil
}

// buildCreateArgs builds arguments for docker create.
func (d *DockerIsolator) buildCreateArgs(cfg *Config, containerName string) ([]string, error) {
	args := []string{"create", "--name", containerName}

	// Add labels (include config hash for stale-container detection on reuse)
	args = append(args, d.buildLabels(cfg.ProjectDir,
		"--label", LabelConfigHash+"="+d.configHash(cfg),
	)...)

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

	// Use /bin/sh as the keep-alive process. The user's preferred shell is
	// started via "docker exec" — using the host shell here would fail if it
	// isn't installed in the image (e.g., fish on a minimal base image).
	args = append(args, "/bin/sh")

	return args, nil
}

// buildCommonArgs builds arguments common to both run and create.
func (d *DockerIsolator) buildCommonArgs(cfg *Config) ([]string, error) {
	var args []string

	// Security: drop all capabilities, add back only what the shim needs
	args = append(args, "--cap-drop", "ALL")
	args = append(args, "--cap-add", "CHOWN")  // chown dirs during setup
	args = append(args, "--cap-add", "SETUID") // privilege drop
	args = append(args, "--cap-add", "SETGID") // privilege drop
	args = append(args, "--security-opt", "no-new-privileges:true")

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
	cacheMounts := tools.CollectCacheMounts()
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

	// User-provided environment variables (sorted for deterministic ordering)
	envKeys := make([]string, 0, len(cfg.Environment))
	for k := range cfg.Environment {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		args = append(args, "-e", k+"="+cfg.Environment[k])
	}

	// .env hiding — always mount /dev/null over .env files at container creation time.
	// This is a security default that cannot be disabled. If env vars are needed
	// inside the sandbox, use explicit env passthrough instead of mounting .env files.
	envFiles := sandbox.FindEnvFiles(cfg.ProjectDir, 3)
	for _, hostPath := range envFiles {
		relPath, err := filepath.Rel(cfg.ProjectDir, hostPath)
		if err != nil {
			continue
		}
		containerPath := filepath.Join(cfg.ProjectDir, relPath)
		args = append(args, "-v", "/dev/null:"+containerPath+":ro")
	}

	// On Linux, add host.docker.internal mapping for proxy access.
	// Use the per-session network gateway IP (where the proxy binds) instead of
	// host-gateway, which resolves to the default bridge (docker0) gateway —
	// a different IP that doesn't have the proxy listening on it.
	if cfg.ProxyEnabled && runtime.GOOS == "linux" {
		hostIP := "host-gateway"
		if d.gatewayIP != "" {
			hostIP = d.gatewayIP
		}
		args = append(args, "--add-host", "host.docker.internal:"+hostIP)
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
		args = append(args, "-e", "NO_PROXY=localhost,127.0.0.1")

		// Mount CA certificate for HTTPS MITM and set SSL_CERT_FILE
		if cfg.ProxyCAPath != "" {
			caDest := "/etc/ssl/certs/devsandbox-ca.crt"
			args = append(args, "-v", fmt.Sprintf("%s:%s:ro", cfg.ProxyCAPath, caDest))
			args = append(args, "-e", fmt.Sprintf("SSL_CERT_FILE=%s", caDest))
			// Also set for Node.js which uses its own env var
			args = append(args, "-e", fmt.Sprintf("NODE_EXTRA_CA_CERTS=%s", caDest))
			// Match bwrap backend's proxy env vars for consistency
			args = append(args, "-e", fmt.Sprintf("REQUESTS_CA_BUNDLE=%s", caDest))
			args = append(args, "-e", fmt.Sprintf("CURL_CA_BUNDLE=%s", caDest))
			args = append(args, "-e", fmt.Sprintf("GIT_SSL_CAINFO=%s", caDest))
		}
	}

	// Per-session network for proxy isolation
	if d.networkName != "" {
		args = append(args, "--network", d.networkName)
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
// Removes the per-session Docker network if one was created.
func (d *DockerIsolator) Cleanup() error {
	if d.networkName != "" {
		rmNet := exec.Command("docker", "network", "rm", d.networkName)
		if output, err := rmNet.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to remove network %s: %s", d.networkName, string(output))
		}
	}
	return nil
}

// NetworkName returns the per-session Docker network name, if any.
func (d *DockerIsolator) NetworkName() string {
	return d.networkName
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
	// Check if path is under home directory (exact match or subpath)
	if hostPath == homeDir {
		return containerHome
	}
	if strings.HasPrefix(hostPath, homeDir+"/") {
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
		HomeDir:        cfg.HomeDir,
	}

	for _, tool := range tools.Available(cfg.HomeDir) {
		// Configure tool if it supports configuration
		if configurable, ok := tool.(tools.ToolWithConfig); ok {
			toolCfg := getToolConfig(cfg.ToolsConfig, tool.Name())
			configurable.Configure(globalCfg, toolCfg)
		}

		// Run setup if tool requires it (e.g., generate safe gitconfig)
		if setupTool, ok := tool.(tools.ToolWithSetup); ok {
			if err := setupTool.Setup(cfg.HomeDir, cfg.SandboxHome); err != nil {
				fmt.Fprintf(os.Stderr, "warning: tool %s setup: %v\n", tool.Name(), err)
			}
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

		// Get environment variables from tool.
		// Remap paths: tools return paths relative to the host home directory,
		// but inside the Docker container sandboxHome is at /home/sandboxuser.
		for _, env := range tool.Environment(cfg.HomeDir, cfg.SandboxHome) {
			if env.FromHost {
				if val := os.Getenv(env.Name); val != "" {
					envVars = append(envVars, env.Name+"="+val)
				}
			} else if env.Value != "" {
				value := strings.ReplaceAll(env.Value, cfg.HomeDir, containerHome)
				envVars = append(envVars, env.Name+"="+value)
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

// containerName generates a Docker container name for the sandbox.
// Format: devsandbox-<project>-<hash>
func (d *DockerIsolator) containerName(projectDir string) string {
	projectName := filepath.Base(projectDir)
	hash := sha256.Sum256([]byte(projectDir))
	return fmt.Sprintf("devsandbox-%s-%x", projectName, hash[:8])
}

// getContainerState checks if a container exists and its state.
func (d *DockerIsolator) getContainerState(ctx context.Context, name string) (exists bool, running bool) {
	cmd := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.Running}}", name)
	output, err := cmd.Output()
	if err != nil {
		return false, false // Container doesn't exist
	}

	isRunning := strings.TrimSpace(string(output)) == "true"
	return true, isRunning
}

// configHash computes a hash of the container-creation-time settings that cannot
// be changed after `docker create`. When any of these change, the container must
// be recreated. This includes network/proxy settings, resource limits, volume
// mounts (bindings, tool mounts, .env hiding), and environment variables — all
// of which are baked into the container at creation and not passed via docker exec.
func (d *DockerIsolator) configHash(cfg *Config) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "proxy=%t\n", cfg.ProxyEnabled)
	_, _ = fmt.Fprintf(h, "proxy_port=%d\n", cfg.ProxyPort)
	_, _ = fmt.Fprintf(h, "proxy_ca=%s\n", cfg.ProxyCAPath)
	_, _ = fmt.Fprintf(h, "network=%s\n", d.networkName)
	_, _ = fmt.Fprintf(h, "gateway=%s\n", d.gatewayIP)
	_, _ = fmt.Fprintf(h, "image=%s\n", d.imageTag)
	_, _ = fmt.Fprintf(h, "mem=%s\n", d.config.MemoryLimit)
	_, _ = fmt.Fprintf(h, "cpu=%s\n", d.config.CPULimit)

	// Bindings — volume mounts passed to docker create.
	_, _ = fmt.Fprintf(h, "overlay=%t\n", cfg.OverlayEnabled)
	for _, b := range cfg.Bindings {
		_, _ = fmt.Fprintf(h, "bind=%s:%s:ro=%t:opt=%t\n", b.Source, b.Dest, b.ReadOnly, b.Optional)
	}

	// Tool bindings — derived from tools config, so hash the config inputs.
	toolKeys := make([]string, 0, len(cfg.ToolsConfig))
	for k := range cfg.ToolsConfig {
		toolKeys = append(toolKeys, k)
	}
	sort.Strings(toolKeys)
	for _, k := range toolKeys {
		_, _ = fmt.Fprintf(h, "tool=%s:%v\n", k, cfg.ToolsConfig[k])
	}

	// Environment variables — baked into docker create, not passed via exec.
	envKeys := make([]string, 0, len(cfg.Environment))
	for k := range cfg.Environment {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		_, _ = fmt.Fprintf(h, "env=%s=%s\n", k, cfg.Environment[k])
	}

	// .env file hiding — bind /dev/null over .env files at creation time.
	envFiles := sandbox.FindEnvFiles(cfg.ProjectDir, 3)
	for _, f := range envFiles {
		_, _ = fmt.Fprintf(h, "envhide=%s\n", f)
	}

	return fmt.Sprintf("%x", h.Sum(nil)[:8])
}

// getContainerConfigHash reads the config hash label from an existing container.
func (d *DockerIsolator) getContainerConfigHash(ctx context.Context, name string) string {
	cmd := exec.CommandContext(ctx, "docker", "inspect", "--format",
		fmt.Sprintf("{{index .Config.Labels %q}}", LabelConfigHash), name)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// removeContainer stops and removes a container by name.
func (d *DockerIsolator) removeContainer(ctx context.Context, name string) {
	rmCmd := exec.CommandContext(ctx, "docker", "rm", "-f", name)
	_ = rmCmd.Run()
}

// buildLabels returns Docker label arguments for a container/volume.
func (d *DockerIsolator) buildLabels(projectDir string, extraLabels ...string) []string {
	projectName := filepath.Base(projectDir)
	labels := []string{
		"--label", LabelDevsandbox + "=true",
		"--label", LabelProjectDir + "=" + projectDir,
		"--label", LabelProjectName + "=" + projectName,
		"--label", LabelCreatedAt + "=" + time.Now().Format(time.RFC3339),
	}
	if d.networkName != "" {
		labels = append(labels, "--label", LabelNetworkName+"="+d.networkName)
	}
	labels = append(labels, extraLabels...)
	return labels
}
