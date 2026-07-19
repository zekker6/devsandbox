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
	"strconv"
	"strings"
	"sync"
	"time"

	"devsandbox/internal/logging"
	"devsandbox/internal/notice"
	"devsandbox/internal/proxy"
	"devsandbox/internal/sandbox"
	"devsandbox/internal/sandbox/tools"
)

const (
	// DefaultImage is the default devsandbox Docker image.
	DefaultImage = "ghcr.io/zekker6/devsandbox:latest"
)

const (
	// CacheMountPath is where the cache volume is mounted
	CacheMountPath = "/cache"
)

// CacheVolumeName returns a per-project cache volume name.
// Each project gets its own cache volume to prevent cross-project cache poisoning.
func CacheVolumeName(projectDir string) string {
	hash := sha256.Sum256([]byte(projectDir))
	return fmt.Sprintf("devsandbox-cache-%x", hash[:4])
}

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

// DockerIsolator implements Isolator using an OCI container engine. The same
// implementation drives both the Docker backend and the krun microVM backend
// (podman + libkrun); the engine field selects which CLI and runtime to use.
type DockerIsolator struct {
	config       DockerConfig
	engine       containerEngine // container CLI/runtime descriptor (docker or krun)
	imageTag     string          // set after buildImage
	networkName  string          // per-session network
	gatewayIP    string          // per-session network gateway IP (proxy bind address)
	logger       *logging.ComponentLogger
	manifestPath string // host path to overlay manifest temp file
}

// SetLogger configures the logger for the Docker isolator.
func (d *DockerIsolator) SetLogger(l *logging.ComponentLogger) {
	d.logger = l
}

// logInfo logs an informational message through notice and the logger.
func (d *DockerIsolator) logInfo(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	notice.Info("%s", msg)
	if d.logger != nil {
		d.logger.Infof("%s", msg)
	}
}

// logWarn logs a warning message through notice and the logger.
func (d *DockerIsolator) logWarn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	notice.Warn("%s", msg)
	if d.logger != nil {
		d.logger.Warnf("%s", msg)
	}
}

// NewDockerIsolator creates a new Docker isolator.
func NewDockerIsolator(cfg DockerConfig) *DockerIsolator {
	return &DockerIsolator{config: cfg, engine: dockerEngine}
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
		if err := os.MkdirAll(configDir, 0o700); err != nil {
			return "", fmt.Errorf("failed to create config dir: %w", err)
		}
		content := fmt.Sprintf("FROM %s\n", DefaultImage)
		if err := os.WriteFile(defaultPath, []byte(content), 0o600); err != nil {
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

// shouldWarnHostBuild reports whether a krun (microVM) launch is about to build a
// project-provided Dockerfile. Such a build runs via host `podman build` before
// the microVM boots, so its RUN steps execute on the host - outside the krun
// guest isolation and outside the proxy egress lockdown. The auto-generated
// config-dir Dockerfile (projectDockerfile == "") is trusted devsandbox content
// and stays silent; only a user-supplied Dockerfile (relative or absolute)
// warrants the trust-boundary disclosure. Pure so tests exercise the decision
// without engine/runtime state.
func shouldWarnHostBuild(microVM bool, projectDockerfile string) bool {
	return microVM && projectDockerfile != ""
}

// buildImage builds a Docker image from the resolved Dockerfile.
func (d *DockerIsolator) buildImage(ctx context.Context, dockerfilePath, imageTag string) error {
	if shouldWarnHostBuild(d.engine.microVM, d.config.Dockerfile) {
		d.logWarn("krun: building project Dockerfile %s on the host via %s build; its RUN steps run outside the microVM guest and the proxy egress lockdown. Only build Dockerfiles you trust.",
			dockerfilePath, d.engine.binary)
	}
	buildContext := filepath.Dir(dockerfilePath)
	cmd := exec.CommandContext(ctx, d.engine.binary, "build",
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
	return d.engine.backend
}

// Available checks if the configured container engine is usable.
func (d *DockerIsolator) Available() error {
	if d.engine.microVM {
		return d.availableMicroVM()
	}

	_, err := exec.LookPath(d.engine.binary)
	if err != nil {
		return errors.New("docker CLI is not installed\n" +
			"Install Docker Desktop: https://docs.docker.com/get-docker/")
	}

	// Check if daemon is running
	cmd := exec.Command(d.engine.binary, "info")
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
	return d.engine.isolationType
}

// PrepareNetwork creates a per-session Docker network for proxy isolation
// and returns the network gateway IP for the proxy to bind to.
func (d *DockerIsolator) PrepareNetwork(ctx context.Context, projectDir string) (*NetworkInfo, error) {
	if d.engine.microVM {
		// Rootless podman + krun: the per-session bridge-gateway model does not
		// apply - rootless, the bridge gateway lives inside the container's network
		// namespace and the proxy (a host process) cannot bind to it. Instead we
		// bind the proxy to host loopback and the guest reaches it through the pasta
		// gateway (--map-host-loopback; see krunEngine.hostAlias). podman's own
		// host.containers.internal is deliberately not used - it resolves to a
		// link-local, LAN-exposed host IP. No network to create, hence none to clean up.
		return &NetworkInfo{BindAddress: "127.0.0.1"}, nil
	}

	hash := sha256.Sum256([]byte(projectDir))
	networkName := fmt.Sprintf("devsandbox-net-%x", hash[:4])
	createNet := exec.CommandContext(ctx, d.engine.binary, "network", "create", networkName)
	if output, err := createNet.CombinedOutput(); err != nil {
		// Ignore "already exists" errors — network may persist from a previous session
		if !strings.Contains(string(output), "already exists") {
			d.logWarn("failed to create network %s: %v", networkName, err)
		}
	}
	d.networkName = networkName

	// Get the gateway IP from the network for proxy binding.
	// Never fall back to the Docker bridge IP — that's accessible to all containers.
	gatewayCmd := exec.CommandContext(ctx, d.engine.binary, "network", "inspect", networkName, "--format", "{{(index .IPAM.Config 0).Gateway}}")
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

	// Fail closed before any launch: krun proxy mode has no egress lockdown off
	// Linux, so it would run untrusted code with open egress. Reject that here
	// rather than silently degrade to weaker isolation.
	if err := microVMProxyUnsupported(runtime.GOOS, d.engine.microVM, sandboxCfg.ProxyEnabled); err != nil {
		return err
	}

	// Set up logger for Docker isolator
	logDir := filepath.Join(sandboxCfg.SandboxHome, proxy.LogBaseDirName, proxy.InternalLogDirName)
	engineName := string(d.engine.backend)
	dockerLogger, _ := logging.NewErrorLogger(filepath.Join(logDir, engineName+".log"))
	d.SetLogger(logging.NewComponentLogger(engineName, dockerLogger, cfg.LogDispatcher))

	// Build isolator config from RunConfig
	isoCfg := &Config{
		ProjectDir:       sandboxCfg.ProjectDir,
		GitRepoRoot:      sandboxCfg.GitRepoRoot,
		SandboxHome:      sandboxCfg.SandboxHome,
		HomeDir:          sandboxCfg.HomeDir,
		Shell:            string(sandboxCfg.Shell),
		ShellPath:        sandboxCfg.ShellPath,
		Command:          cfg.Command,
		Interactive:      cfg.Interactive,
		ProxyEnabled:     sandboxCfg.ProxyEnabled,
		ProxyPort:        cfg.ProxyPort,
		ProxyExtraEnv:    sandboxCfg.ProxyExtraEnv,
		ProxyExtraCAEnv:  sandboxCfg.ProxyExtraCAEnv,
		EnvPassthrough:   sandboxCfg.EnvPassthrough,
		EnvVars:          sandboxCfg.EnvVars,
		Environment:      make(map[string]string),
		ToolsConfig:      sandboxCfg.ToolsConfig,
		DefaultMountMode: sandboxCfg.DefaultMountMode,
		HideEnvFiles:     sandboxCfg.HideEnvFiles,
	}

	// Add CA path if proxy is enabled and MITM generated a CA
	if sandboxCfg.ProxyEnabled && cfg.ProxyServer != nil && cfg.ProxyServer.CA() != nil {
		isoCfg.ProxyCAPath = cfg.ProxyServer.Config().CACertPath
	}

	// krun runs one ephemeral microVM per project at a time. The deterministic
	// --name + --replace (buildRunArgs) means two simultaneous same-project
	// launches could otherwise both pass BuildDocker's running-container guard
	// (a TOCTOU check) and have the second --replace clobber the first live
	// microVM. Hold an exclusive, cross-process per-project lock across both the
	// guard and the run so only one wins; the loser fails fast below. Docker is
	// unaffected (anonymous --rm, no name collision).
	if d.engine.microVM {
		lock, lockErr := acquireMicroVMSessionLock(d.containerName(sandboxCfg.ProjectDir))
		if lockErr != nil {
			return lockErr
		}
		defer func() { _ = lock.Release() }()
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
		// krun + proxy: start non-blocking so the booted guest's PID can be
		// resolved and the session registered for port forwarding (modeled on the
		// bwrap StartWithPasta -> OnSandboxStart -> Wait flow). Docker and
		// krun-without-proxy keep the simple blocking run.
		if d.usesMicroVMSession(sandboxCfg.ProxyEnabled) {
			return d.runMicroVMSession(ctx, cmd, result.ContainerName, cfg.OnSandboxStart, isoCfg.SandboxHome, isoCfg.ProxyPort)
		}
		return asCommandExit(cmd.Run())

	case DockerActionCreate:
		createCmd := exec.Command(result.BinaryPath, result.Args...)
		if out, err := createCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to create container: %s", strings.TrimSpace(string(out)))
		}
		notice.Info("Created container")

		startCmd := exec.Command(result.BinaryPath, "start", result.ContainerName)
		if out, err := startCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to start container: %s", strings.TrimSpace(string(out)))
		}
		notice.Info("Started container")

		if err := d.waitForContainerReady(result.BinaryPath, result.ContainerName, readinessTimeout); err != nil {
			notice.Warn("Container setup timeout")
			return d.withStartupDiagnostics(fmt.Errorf("container setup timed out after %s", readinessTimeout), result.ContainerName)
		}
		notice.Info("Container setup ready")

		if err := d.installMiseTools(result.BinaryPath, result.ContainerName, isoCfg); err != nil {
			notice.Warn("failed to install tools: %v", err)
		}

		return d.execIntoContainer(result.BinaryPath, result.ContainerName, isoCfg.Interactive, isoCfg.Shell, cfg.Command)

	case DockerActionExec:
		if result.ContainerJustStarted {
			if err := d.waitForContainerReady(result.BinaryPath, result.ContainerName, readinessTimeout); err != nil {
				notice.Warn("Container setup timeout")
				return d.withStartupDiagnostics(fmt.Errorf("container startup failed: %w", err), result.ContainerName)
			}
			notice.Info("Container setup ready")
		}

		if err := d.installMiseTools(result.BinaryPath, result.ContainerName, isoCfg); err != nil {
			notice.Warn("failed to install tools: %v", err)
		}

		return d.execIntoContainer(result.BinaryPath, result.ContainerName, isoCfg.Interactive, isoCfg.Shell, cfg.Command)

	default:
		return fmt.Errorf("unexpected docker action: %d", result.Action)
	}
}

// usesMicroVMSession reports whether the krun non-blocking session path should
// run for this launch: a microVM engine in proxy mode. The non-blocking path
// resolves the booted guest's PID, which is needed for BOTH the host-side egress
// lockdown (always, on Linux) and session registration for port forwarding (only
// when a callback is wired). Gating it on proxy alone - not on a callback - keeps
// it aligned with the DEVSANDBOX_EGRESS_LOCKDOWN env set in buildCommonArgs: if
// this path were skipped while that env was set, the guest would wait forever for
// a sentinel the host never writes. The proxy gate mirrors bwrap (it only calls
// OnSandboxStart under ProxyEnabled) and the proxy-gated session naming/cleanup
// in cmd/devsandbox/main.go; runMicroVMSession skips the callback when it is nil,
// so no nameless session file is written. Docker and krun-without-proxy keep the
// simple blocking run.
func (d *DockerIsolator) usesMicroVMSession(proxyEnabled bool) bool {
	return d.engine.microVM && proxyEnabled
}

// runMicroVMSession starts an already-built ephemeral krun run non-blocking,
// resolves the booted guest's PID in the background, and invokes onStart so the
// session registers for port forwarding - then waits for the session to exit.
//
// PID resolution runs concurrently rather than before Wait (unlike the bwrap
// path, whose child PID is known synchronously): a short-lived `devsandbox run
// <cmd>` would otherwise stall waiting for a PID on a container that has already
// `--rm`'d itself. When the container exits, Wait returns and the poll is
// cancelled, so a fast run is never delayed.
//
// Reachability caveat (validated on a KVM host in Post-Completion, not here):
// the resolved PID is the host-side VMM process, whose network namespace is the
// pasta/container netns - one layer outside the guest. Forwarding therefore
// reaches that netns; whether a guest listener is visible there depends on pasta,
// so krun forward is best-effort in v2. Session registration itself is correct
// regardless (it powers `devsandbox forward`/liveness).
func (d *DockerIsolator) runMicroVMSession(ctx context.Context, cmd *exec.Cmd, containerName string, onStart func(nsPID int, nsPath string), sandboxHome string, proxyPort int) error {
	// Egress lockdown is applied here, host-side, in the VMM's pasta netns (see
	// egress.go for why it cannot run in-guest under libkrun TSI). It is gated to
	// the same condition that sets DEVSANDBOX_EGRESS_LOCKDOWN in buildCommonArgs:
	// a krun microVM session always runs in proxy mode, so on Linux the guest is
	// waiting on the sentinel and we MUST produce it (or fail closed).
	// Clear any stale sentinel BEFORE the guest boots so it waits for THIS run's
	// lockdown rather than honoring a file left by a previous run of the same
	// project. Fail-closed: if the path cannot be verified clean, abort the launch
	// here - before cmd.Start() below - rather than boot the guest against a
	// sentinel path that could spoof the gate.
	lockdown, prepErr := prepareEgressLockdown(d.engine.microVM, runtime.GOOS, sandboxHome)
	if prepErr != nil {
		return prepErr
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start %s microVM: %w", d.engine.binary, err)
	}

	resolveCtx, cancelResolve := context.WithCancel(ctx)
	defer cancelResolve()

	var (
		lockdownMu  sync.Mutex
		lockdownErr error
	)
	var wg sync.WaitGroup
	wg.Go(func() {
		pid, err := d.waitForContainerPID(resolveCtx, containerName, 30*time.Second)
		if err != nil {
			// Suppress the warning when the container simply exited first
			// (resolveCtx cancelled) - that is the normal short-run case.
			if resolveCtx.Err() == nil {
				if diag := d.startupDiagnostics(containerName); diag != "" {
					d.logWarn("port forwarding unavailable for this krun session: %v\nlast %d lines of %s output from %q:\n%s",
						err, startupLogTailLines, d.engine.binary, containerName, diag)
				} else {
					d.logWarn("port forwarding unavailable for this krun session: %v", err)
				}
			}
			return
		}
		// Skip the rest if the container exited while we were resolving.
		if resolveCtx.Err() != nil {
			return
		}

		// Lock egress before releasing the guest workload. The guest shim blocks
		// on the sentinel until writeEgressSentinel below, so the workload never
		// runs while direct egress is still open. Fail-closed: on any error, tear
		// down the microVM and record the error so Run aborts.
		if lockdown {
			if err := lockdownGuestEgress(pid, d.engine.hostAlias, proxyPort); err != nil {
				d.recordLockdownFailure(&lockdownMu, &lockdownErr, err, containerName, cancelResolve)
				return
			}
			if err := writeEgressSentinel(sandboxHome); err != nil {
				d.recordLockdownFailure(&lockdownMu, &lockdownErr, fmt.Errorf("write egress sentinel: %w", err), containerName, cancelResolve)
				return
			}
		}

		// Register the port-forward session when a callback is wired. krun+proxy
		// runs may reach this path without one (the lockdown above still applies);
		// skip rather than write a nameless session file.
		if onStart != nil {
			onStart(pid, containerNetnsPath(pid))
		}
	})

	err := cmd.Wait()
	// Cancel and join the resolver before returning. The caller registers the
	// session inside onStart and tears it down in a deferred cleanup once Run
	// returns; without the join the resolver could call onStart after that
	// teardown, leaving a stale session/forward target. The cancel also unblocks
	// a resolver still polling for a PID on the now-exited container.
	cancelResolve()
	wg.Wait()

	lockdownMu.Lock()
	le := lockdownErr
	lockdownMu.Unlock()
	if le != nil {
		return fmt.Errorf("krun egress lockdown failed (fail-closed): %w", le)
	}
	return asCommandExit(err)
}

// recordLockdownFailure stores the first egress-lockdown error and fails closed
// by killing the microVM (so the sentinel-gated guest never runs the workload)
// and cancelling the resolver.
func (d *DockerIsolator) recordLockdownFailure(mu *sync.Mutex, dst *error, err error, containerName string, cancelResolve context.CancelFunc) {
	mu.Lock()
	if *dst == nil {
		*dst = err
	}
	mu.Unlock()
	d.logWarn("egress lockdown failed, tearing down microVM: %v", err)
	_ = exec.Command(d.engine.binary, "kill", containerName).Run()
	cancelResolve()
}

// waitForContainerPID polls the engine for the container's main-process PID until
// it reports a running value, the timeout elapses, or ctx is cancelled.
func (d *DockerIsolator) waitForContainerPID(ctx context.Context, containerName string, timeout time.Duration) (int, error) {
	timeoutCh := time.After(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var lastErr error
	for {
		pid, err := d.inspectContainerPID(ctx, containerName)
		if err == nil && pid > 0 {
			return pid, nil
		}
		if err != nil {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-timeoutCh:
			if lastErr != nil {
				return 0, fmt.Errorf("container %s did not report a running PID within %s: %w", containerName, timeout, lastErr)
			}
			return 0, fmt.Errorf("container %s did not report a running PID within %s", containerName, timeout)
		case <-ticker.C:
		}
	}
}

// inspectContainerPID runs `<engine> inspect --format '{{.State.Pid}}'` and
// parses the result.
func (d *DockerIsolator) inspectContainerPID(ctx context.Context, containerName string) (int, error) {
	cmd := exec.CommandContext(ctx, d.engine.binary, "inspect", "--format", "{{.State.Pid}}", containerName)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("inspect %s: %w", containerName, err)
	}
	return parseContainerPID(string(out))
}

// parseContainerPID extracts the integer PID from a `podman/docker inspect
// --format '{{.State.Pid}}'` output. A created-but-not-yet-running container
// reports 0; that is returned as an error so callers keep polling.
func parseContainerPID(inspectOutput string) (int, error) {
	s := strings.TrimSpace(inspectOutput)
	if s == "" {
		return 0, errors.New("empty PID from container inspect")
	}
	pid, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("parse container PID %q: %w", s, err)
	}
	if pid <= 0 {
		return 0, fmt.Errorf("container not running yet (PID %d)", pid)
	}
	return pid, nil
}

// containerNetnsPath returns the procfs network-namespace path for a PID, the
// form OnSandboxStart and the namespace dialer consume.
func containerNetnsPath(pid int) string {
	return fmt.Sprintf("/proc/%d/ns/net", pid)
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

// startupLogTailLines is how many trailing lines of container output to surface
// when a startup readiness/PID wait times out.
const startupLogTailLines = 50

// startupDiagnostics fetches the tail of a container's combined stdout/stderr so
// a startup timeout can explain itself. On the detached create/exec path the
// in-guest shim's fatal output (e.g. a permission-denied overlay-manifest read)
// goes to the container log, never to the user's terminal, so a bare "did not
// become ready" timeout hides the actual cause. This is best-effort: it returns
// "" when there is no output to show or the lookup itself fails, so it can only
// add context to the original timeout error, never mask it. It uses its own
// short-lived context rather than the (possibly already-cancelled) run context
// so log retrieval still works after a startup timeout.
func (d *DockerIsolator) startupDiagnostics(containerName string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, d.engine.binary, "logs", "--tail", strconv.Itoa(startupLogTailLines), containerName)
	out, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if err != nil && trimmed == "" {
		return ""
	}
	return trimmed
}

// withStartupDiagnostics appends the container's recent log output to a startup
// timeout error so the user sees why readiness was never reached (typically an
// in-guest shim failure) instead of a bare timeout. Returns baseErr unchanged
// when no logs are available.
func (d *DockerIsolator) withStartupDiagnostics(baseErr error, containerName string) error {
	logs := d.startupDiagnostics(containerName)
	if logs == "" {
		return baseErr
	}
	return fmt.Errorf("%w\n\nlast %d lines of %s output from %q:\n%s",
		baseErr, startupLogTailLines, d.engine.binary, containerName, logs)
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

	notice.Info("Installing tools")
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
	dockerPath, err := exec.LookPath(d.engine.binary)
	if err != nil {
		return nil, fmt.Errorf("%s CLI not found: %w", d.engine.binary, err)
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
	//
	// krun reuses a deterministic per-project --name so Run can resolve the
	// guest PID for session registration, paired with --replace to clear a
	// stopped leftover from a hard-killed prior run. But --replace would also
	// silently destroy a sibling microVM still running for this project, so
	// refuse loudly here instead of killing a live session. Docker stays
	// anonymous and is unaffected.
	if d.engine.microVM {
		if _, running := d.getContainerState(ctx, containerName); running {
			return nil, d.microVMSessionActiveError(containerName)
		}
	}

	args, err := d.buildRunArgs(cfg)
	if err != nil {
		return nil, err
	}
	res := &DockerBuildResult{
		Action:     DockerActionRun,
		BinaryPath: dockerPath,
		Args:       args,
	}
	// Surface the krun container name so Run can resolve the guest PID for
	// session registration (buildRunArgs injected the matching --name).
	if d.engine.microVM {
		res.ContainerName = containerName
	}
	return res, nil
}

// Preflight refuses to start a krun launch when another microVM session is
// already running for this project. It runs before proxy startup and the image
// build so the conflict surfaces in milliseconds instead of after that work is
// wasted; BuildDocker keeps the identical guard (under the per-project session
// lock) as the authoritative TOCTOU check for two launches racing past here.
// Docker and bwrap have no launch-time conflict (anonymous --rm containers /
// overlay-based concurrency), so Preflight is a no-op for them.
func (d *DockerIsolator) Preflight(ctx context.Context, projectDir string) error {
	if !d.engine.microVM {
		return nil
	}
	containerName := d.containerName(projectDir)
	if _, running := d.getContainerState(ctx, containerName); running {
		return d.microVMSessionActiveError(containerName)
	}
	return nil
}

// microVMSessionActiveError reports that a krun microVM is already running for
// this project and tells the user exactly how to clear it. krun runs one
// ephemeral microVM per project, so a second launch would have to clobber the
// live guest; the engine binary (podman) name is interpolated so the remediation
// command is copy-pasteable.
func (d *DockerIsolator) microVMSessionActiveError(containerName string) error {
	return fmt.Errorf(
		"a krun session is already active for this project (container %q); stop it before starting another:\n  %s rm -f %s",
		containerName, d.engine.binary, containerName,
	)
}

// acquireMicroVMSessionLock takes an exclusive, cross-process lock so only one
// krun microVM session runs per project at a time. It is keyed on the container
// name (unique per project) and held by Run across the guard check and the
// `podman run --replace`, closing the TOCTOU window between them. A non-blocking
// try-lock means a concurrent same-project launch fails fast with a clear error
// rather than silently replacing the first live microVM. A lock file left by a
// hard-killed prior run never blocks acquisition: the kernel auto-releases the
// flock when the holder's fd closes on exit, so the leftover file (which is
// never unlinked) is simply re-locked. The lock lives in the host temp dir, not
// the guest-visible sandbox home, so untrusted guest code cannot remove it to
// defeat the exclusion.
func acquireMicroVMSessionLock(containerName string) (*proxy.FileLock, error) {
	lockPath := filepath.Join(os.TempDir(), containerName+".krun.lock")
	lock, err := proxy.TryFileLock(lockPath)
	if err != nil {
		return nil, fmt.Errorf("acquire krun session lock for container %q (a session may already be active; stop it before starting another): %w", containerName, err)
	}
	return lock, nil
}

// buildExecArgs builds arguments for docker exec into a running container.
func buildExecArgs(cfg *Config, containerName string) []string {
	args := []string{"exec"}
	// Attach stdin (-i) so piped input reaches the exec'd command; add a TTY only
	// for interactive sessions.
	if cfg.Interactive {
		args = append(args, "-it")
	} else {
		args = append(args, "-i")
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
	args := []string{"run"}
	args = append(args, d.engine.runtimeArgs...)
	args = append(args, "--rm")

	// microVM (krun) ephemeral runs get a deterministic name so the run path can
	// `podman inspect` the booted guest's PID and register a session for port
	// forwarding. Docker keeps its anonymous --rm run (its forward path differs).
	// --replace atomically removes a same-named container left behind by a prior
	// run that was hard-killed (SIGKILL/OOM/host crash) before --rm cleanup, so a
	// relaunch never fails with "container name already in use".
	if d.engine.microVM {
		args = append(args, "--replace", "--name", d.containerName(cfg.ProjectDir))
	}

	// Attach stdin so piped input reaches the workload; add a TTY only for an
	// interactive session. Without -i, `podman/docker run` leaves the container's
	// stdin closed, so `data | devsandbox -- cmd` silently loses its input.
	if cfg.Interactive {
		args = append(args, "-it")
	} else {
		args = append(args, "-i")
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
	args := []string{"create"}
	args = append(args, d.engine.runtimeArgs...)
	args = append(args, "--name", containerName)

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
	// DAC_OVERRIDE is needed during the shim's root-phase setup, which must
	// populate and chown a /home/sandboxuser that is NOT owned by container-root:
	// rootful Docker bind-mounts it from the host (owned by the host UID) and the
	// :ro overlay manifest is a host temp file (mode 0600, host-owned); krun shares
	// it over virtio-fs under keep-id (owned by the mapped host user). With
	// DAC_OVERRIDE dropped, container-root still obeys those DAC bits and cannot
	// create files in the home or read the manifest (the read is fatal), so startup
	// hangs on the readiness timeout. The shim setuids to the unprivileged user -
	// which clears its capability set - before exec'ing the workload, and
	// no-new-privileges blocks regaining anything, so the workload never holds it.
	args = append(args, "--cap-add", "DAC_OVERRIDE")
	args = append(args, "--security-opt", "no-new-privileges:true")

	// microVM (krun) runs under rootless podman: keep-id maps the host user 1:1
	// into the guest so files the workload writes to the virtio-fs-shared project
	// dir/home come back owned by the host user instead of an unprivileged subuid.
	// Rootful Docker needs no such remap.
	if d.engine.microVM {
		args = append(args, "--userns", "keep-id")
	}

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
	toolMounts, toolEnvVars, overlayManifest := d.getToolBindings(cfg)
	for _, mount := range toolMounts {
		args = append(args, "-v", mount)
	}
	for _, env := range toolEnvVars {
		args = append(args, "-e", env)
	}

	// Write overlay manifest if any tmpoverlay bindings exist
	if len(overlayManifest.Overlays) > 0 {
		manifestPath, err := d.writeOverlayManifest(overlayManifest)
		if err != nil {
			return nil, fmt.Errorf("write overlay manifest: %w", err)
		}
		args = append(args, "-v", manifestPath+":"+OverlayManifestPath+":ro")
	}

	// Per-project cache volume for tools (mise, go, etc.)
	cacheMounts := tools.CollectCacheMounts()
	if len(cacheMounts) > 0 {
		args = append(args, "-v", CacheVolumeName(cfg.ProjectDir)+":"+CacheMountPath)
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

	// Pass through host environment variables
	for _, name := range cfg.EnvPassthrough {
		if val, ok := os.LookupEnv(name); ok {
			args = append(args, "-e", name+"="+val)
		}
	}

	// Explicit env vars from config.sandbox.environment override everything above.
	evNames := make([]string, 0, len(cfg.EnvVars))
	for k := range cfg.EnvVars {
		evNames = append(evNames, k)
	}
	sort.Strings(evNames)
	for _, k := range evNames {
		args = append(args, "-e", k+"="+cfg.EnvVars[k])
	}

	// .env hiding — mount /dev/null over .env files at container creation time.
	// This is a security default that can be disabled via config or --no-hide-env flag.
	if cfg.HideEnvFiles {
		envFiles := sandbox.FindEnvFiles(cfg.ProjectDir, 3)
		for _, hostPath := range envFiles {
			relPath, err := filepath.Rel(cfg.ProjectDir, hostPath)
			if err != nil {
				continue
			}
			containerPath := filepath.Join(cfg.ProjectDir, relPath)
			args = append(args, "-v", "/dev/null:"+containerPath+":ro")
		}
	}

	// On Linux, add host.docker.internal mapping for proxy access.
	// Use the per-session network gateway IP (where the proxy binds) instead of
	// host-gateway, which resolves to the default bridge (docker0) gateway —
	// a different IP that doesn't have the proxy listening on it.
	// krun is excluded: rootless podman already injects a working
	// host.containers.internal that reaches the host loopback (where the proxy
	// binds), so an explicit --add-host would override it with a wrong target.
	if cfg.ProxyEnabled && runtime.GOOS == "linux" && !d.engine.microVM {
		hostIP := "host-gateway"
		if d.gatewayIP != "" {
			hostIP = d.gatewayIP
		}
		args = append(args, "--add-host", d.engine.hostAlias+":"+hostIP)
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

		// Tool-specific proxy env vars
		args = append(args, "-e", fmt.Sprintf("YARN_HTTP_PROXY=%s", proxyURL))
		args = append(args, "-e", fmt.Sprintf("YARN_HTTPS_PROXY=%s", proxyURL))

		// Node.js >=24: opt-in for built-in fetch (undici) to honor HTTP(S)_PROXY env vars.
		// Without this, npx-based tools like mcp-remote bypass the proxy and fail with ENETUNREACH.
		args = append(args, "-e", "NODE_USE_ENV_PROXY=1")

		// User-defined extra proxy env vars from config
		for _, name := range cfg.ProxyExtraEnv {
			args = append(args, "-e", fmt.Sprintf("%s=%s", name, proxyURL))
		}

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

			// User-defined extra CA bundle env vars from config
			for _, name := range cfg.ProxyExtraCAEnv {
				args = append(args, "-e", fmt.Sprintf("%s=%s", name, caDest))
			}
		}
	}

	// Per-session network for proxy isolation
	if d.networkName != "" {
		args = append(args, "--network", d.networkName)
	}

	// krun proxy mode: bind the proxy to host loopback (never LAN-reachable) and
	// have pasta map the gateway alias to the host's 127.0.0.1, so only this
	// microVM can reach it - the same model the bwrap backend uses. d.engine.hostAlias
	// holds the gateway IP for krun, and PROXY_HOST below points the guest at it.
	if d.engine.microVM && cfg.ProxyEnabled {
		args = append(args, "--network", "pasta:--map-host-loopback,"+d.engine.hostAlias)

		// Egress lockdown: force all guest traffic through the proxy by deleting
		// the default route (keeping only a /32 to the gateway) so direct-IP and
		// DNS exfiltration have no path out. Under libkrun TSI the guest has no
		// routable interface, so this CANNOT run in-guest; the host applies it in
		// the VMM's pasta netns after boot (see runMicroVMSession -> egress.go).
		// The shim only WAITS for that to finish (DEVSANDBOX_EGRESS_LOCKDOWN=1)
		// before exec'ing the workload - no in-guest NET_ADMIN is needed. Gated on
		// Linux: the surgery uses nsenter into the VMM netns.
		if runtime.GOOS == "linux" {
			args = append(args, "-e", "DEVSANDBOX_EGRESS_LOCKDOWN=1")
		}

		// The egress-locked guest gets offline mise. Some mise backends' remote
		// version-list lookups (npm registry, python-build) never traverse the
		// proxy and hang to their timeout, and mise re-resolves the toolset per
		// listed row with no negative cache, so one `mise ls` against a config
		// with `@latest` specs turns into hundreds of doomed lookups (measured:
		// 14 minutes). Offline mode resolves from installed/cached data instantly
		// - with host installs seeded, that is the full host toolchain. Boot-time
		// project installs still run online (installMiseTools overrides this),
		// and a manual in-sandbox install can too: MISE_OFFLINE=0 mise install.
		args = append(args, "-e", "MISE_OFFLINE=1")
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
// Removes the per-session Docker network if one was created and cleans up temp files.
func (d *DockerIsolator) Cleanup() error {
	if d.manifestPath != "" {
		_ = os.Remove(d.manifestPath)
	}
	if d.networkName != "" {
		rmNet := exec.Command(d.engine.binary, "network", "rm", d.networkName)
		if output, err := rmNet.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to remove network %s: %s", d.networkName, string(output))
		}
	}
	return nil
}

// writeOverlayManifest writes the manifest to a temp file and returns the host path.
func (d *DockerIsolator) writeOverlayManifest(manifest *OverlayManifest) (string, error) {
	f, err := os.CreateTemp("", "devsandbox-overlay-manifest-*.json")
	if err != nil {
		return "", err
	}
	path := f.Name()
	_ = f.Close()

	if err := manifest.Write(path); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	d.manifestPath = path
	return path, nil
}

// NetworkName returns the per-session Docker network name, if any.
func (d *DockerIsolator) NetworkName() string {
	return d.networkName
}

// proxyHost returns the host address for proxy connections from within the container.
func (d *DockerIsolator) proxyHost() string {
	// host.docker.internal (Docker) / host.containers.internal (podman) resolve to
	// the host natively on macOS and on Linux via the --add-host mapping above.
	return d.engine.hostAlias
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

// copyOverlayShadowPath returns a deterministic shadow mount path for a container destination.
// Used on macOS where overlayfs is unavailable inside Docker Desktop containers.
func copyOverlayShadowPath(containerDest string) string {
	h := sha256.Sum256([]byte(containerDest))
	return fmt.Sprintf("/tmp/.overlay-shadow/%x", h[:8])
}

// getToolBindings retrieves bindings from registered tools and converts them
// to Docker volume mount strings.
func (d *DockerIsolator) getToolBindings(cfg *Config) (mounts []string, envVars []string, manifest *OverlayManifest) {
	manifest = &OverlayManifest{}

	globalCfg := tools.GlobalConfig{
		DefaultMountMode: cfg.DefaultMountMode,
		ProjectDir:       cfg.ProjectDir,
		HomeDir:          cfg.HomeDir,
		GitRepoRoot:      cfg.GitRepoRoot,
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
				notice.Warn("tool %s setup: %v", tool.Name(), err)
			}
		}

		// Skip disabled tools
		toolMountMode := getToolMountMode(cfg.ToolsConfig, tool.Name())
		if toolMountMode == "disabled" {
			continue
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
				sandbox.ResolveBindingType(&b, toolMountMode, cfg.DefaultMountMode)
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

				// Determine if this is a tmpoverlay candidate
				isTmpOverlay := b.Type == tools.MountTmpOverlay
				isDir := false
				if info, err := os.Stat(b.Source); err == nil {
					isDir = info.IsDir()
				}

				if isTmpOverlay && isDir && (runtime.GOOS == "darwin" || d.engine.microVM) {
					// macOS: overlayfs unavailable in Docker Desktop.
					// krun: kernel overlayfs mount fails in the libkrun guest (EPERM
					// for an overlay whose lowerdir is on virtio-fs inside a nested
					// userns), so fall back to the same copy strategy.
					// Mount source read-only at a shadow path; shim copies to target.
					shadowDest := copyOverlayShadowPath(dest)
					mounts = append(mounts, b.Source+":"+shadowDest+":ro")
					manifest.Overlays = append(manifest.Overlays, OverlayEntry{
						Path:   dest,
						Source: shadowDest,
						Type:   "copyoverlay",
					})
				} else {
					mount := b.Source + ":" + dest
					if isTmpOverlay || b.Type == tools.MountOverlay {
						// Both overlay types mount as :ro in Docker;
						// shim applies overlayfs on top for tmpoverlay dirs.
						mount += ":ro"
					} else if b.ReadOnly {
						mount += ":ro"
					}
					mounts = append(mounts, mount)

					// Add to manifest if it's a tmpoverlay directory
					if isTmpOverlay && isDir {
						manifest.Overlays = append(manifest.Overlays, OverlayEntry{
							Path: dest,
							Type: "tmpoverlay",
						})
					}
				}
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

	return mounts, envVars, manifest
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

// getToolMountMode extracts mount_mode from the tool's config section.
func getToolMountMode(toolsConfig map[string]any, toolName string) string {
	cfg := getToolConfig(toolsConfig, toolName)
	if cfg == nil {
		return ""
	}
	mode, _ := cfg["mount_mode"].(string)
	return mode
}

// containerName generates a Docker container name for the sandbox.
func (d *DockerIsolator) containerName(projectDir string) string {
	return sandbox.DockerContainerName(projectDir)
}

// getContainerState checks if a container exists and its state.
func (d *DockerIsolator) getContainerState(ctx context.Context, name string) (exists bool, running bool) {
	cmd := exec.CommandContext(ctx, d.engine.binary, "inspect", "--format", "{{.State.Running}}", name)
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
	_, _ = fmt.Fprintf(h, "mount_mode=%s\n", cfg.DefaultMountMode)
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

	// Overlay manifest — changes in overlay paths require container recreation.
	_, _, overlayManifest := d.getToolBindings(cfg)
	for _, entry := range overlayManifest.Overlays {
		_, _ = fmt.Fprintf(h, "overlay=%s:%s\n", entry.Path, entry.Type)
	}

	return fmt.Sprintf("%x", h.Sum(nil)[:8])
}

// getContainerConfigHash reads the config hash label from an existing container.
func (d *DockerIsolator) getContainerConfigHash(ctx context.Context, name string) string {
	cmd := exec.CommandContext(ctx, d.engine.binary, "inspect", "--format",
		fmt.Sprintf("{{index .Config.Labels %q}}", LabelConfigHash), name)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// removeContainer stops and removes a container by name.
func (d *DockerIsolator) removeContainer(ctx context.Context, name string) {
	rmCmd := exec.CommandContext(ctx, d.engine.binary, "rm", "-f", name)
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
