// Package isolator provides an abstraction over sandbox backends (bwrap, docker).
package isolator

import (
	"context"
	"fmt"
	"runtime"

	"devsandbox/internal/cgroups"
	"devsandbox/internal/config"
	"devsandbox/internal/logging"
	"devsandbox/internal/proxy"
	"devsandbox/internal/sandbox"
)

// Backend represents the isolation backend type.
type Backend string

const (
	// BackendBwrap uses bubblewrap for isolation (Linux only).
	BackendBwrap Backend = "bwrap"
	// BackendDocker uses Docker containers for isolation (cross-platform).
	BackendDocker Backend = "docker"
	// BackendKrun runs the sandbox image inside a libkrun microVM (podman +
	// --runtime krun) for hardware-level isolation of untrusted code.
	BackendKrun Backend = "krun"
	// BackendAuto automatically selects the best available backend.
	BackendAuto Backend = "auto"
)

// NetworkInfo holds backend-specific network configuration returned by PrepareNetwork.
type NetworkInfo struct {
	// BindAddress is the IP for proxy to bind to.
	BindAddress string
}

// RunConfig holds all configuration needed to execute a sandbox.
type RunConfig struct {
	SandboxCfg     *sandbox.Config
	AppCfg         *config.Config
	Command        []string
	Interactive    bool
	RemoveOnExit   bool
	HasActiveTools bool

	// Proxy state (started by main.go before Run)
	ProxyServer *proxy.Server // nil if proxy disabled
	ProxyCAPath string
	ProxyPort   int // actual port after binding

	// Logging
	SandboxLogger *logging.ErrorLogger
	LogDispatcher *logging.Dispatcher

	// OnSandboxStart is called after the sandbox process starts but before Wait.
	// Receives the PID of a process inside the sandbox network namespace and
	// the namespace path. Can be nil.
	OnSandboxStart func(nsPID int, nsPath string)

	// SandboxName is the human-readable session name (from --name or auto-generated).
	SandboxName string
}

// Isolator is the interface for sandbox backends.
type Isolator interface {
	// Name returns the backend name.
	Name() Backend
	// Available checks if this backend can be used.
	Available() error
	// IsolationType returns the sandbox isolation type for metadata.
	IsolationType() sandbox.IsolationType
	// Preflight performs cheap, backend-specific conflict detection before any
	// expensive setup (proxy startup, image build) runs, so a doomed launch
	// fails fast with an actionable error instead of after the work is wasted.
	// No-op for backends without a launch-time conflict.
	Preflight(ctx context.Context, projectDir string) error
	// PrepareNetwork sets up backend-specific networking before proxy starts.
	// Docker: creates per-session network, returns gateway IP for proxy binding.
	// Bwrap: no-op, returns nil.
	PrepareNetwork(ctx context.Context, projectDir string) (*NetworkInfo, error)
	// Run executes the full sandbox lifecycle.
	Run(ctx context.Context, cfg *RunConfig) error
	// Cleanup removes backend-specific resources (Docker network, etc).
	Cleanup() error
}

// Config contains settings passed to the Docker isolator for building container args.
type Config struct {
	// ProjectDir is the directory to sandbox.
	ProjectDir string
	// GitRepoRoot is the main git repo root when ProjectDir is a worktree.
	// Empty in non-worktree mode.
	GitRepoRoot string
	// LaunchedAgent is the canonical name of the AI agent devsandbox was asked
	// to run, or empty when the command is not a known agent. Tools that key
	// behavior on agent identity must take it from here: it is derived
	// host-side from the command argv and never from anything the sandbox
	// reports.
	LaunchedAgent string
	// SandboxHome is the per-project sandbox home directory.
	SandboxHome string
	// SandboxRoot is the per-project state directory that holds SandboxHome.
	// Host-only: nothing under it except SandboxHome is exposed to the sandbox,
	// so it is where host-authored files the guest must not rewrite belong.
	SandboxRoot string
	// HomeDir is the user's home directory.
	HomeDir string
	// Shell is the shell to run inside the sandbox.
	Shell string
	// ShellPath is the path to the shell binary.
	ShellPath string
	// Command is the command to run (empty for interactive shell).
	Command []string
	// Interactive indicates if stdin is a TTY.
	Interactive bool
	// ProxyEnabled enables network isolation with proxy.
	ProxyEnabled bool
	// ProxyPort is the proxy server port.
	ProxyPort int
	// ProxyHost is the proxy server host (for Docker: host.docker.internal on macOS).
	ProxyHost string
	// ProxyCAPath is the path to the proxy CA certificate (for HTTPS MITM).
	ProxyCAPath string
	// ProxyExtraEnv is a list of additional env var names set to the proxy URL.
	ProxyExtraEnv []string
	// ProxyExtraCAEnv is a list of additional env var names set to the CA cert path.
	ProxyExtraCAEnv []string
	// EnvPassthrough is a list of host env var names to pass through to the sandbox.
	EnvPassthrough []string
	// EnvVars are explicit name→value env vars, applied last so they override
	// Environment and EnvPassthrough on conflict.
	EnvVars map[string]string
	// Environment variables to set.
	Environment map[string]string
	// Bindings are filesystem mounts (translated per-backend).
	Bindings []Binding
	// ToolsConfig contains tool-specific configuration from config file.
	ToolsConfig map[string]any
	// DefaultMountMode is the global mount mode for tool bindings.
	DefaultMountMode string
	// HideEnvFiles controls whether .env files are hidden from the sandbox.
	HideEnvFiles bool
}

// Binding represents a filesystem mount.
type Binding struct {
	Source   string
	Dest     string
	ReadOnly bool
	Optional bool
}

// Option configures isolator creation.
type Option func(*options)

type options struct {
	dockerfile string
	configDir  string
	// limits come from the backend-neutral [sandbox.resources] section alone.
	limits cgroups.Limits
	// containerLimits additionally carry the deprecated
	// [sandbox.docker.resources] section, and are read by the container
	// backends only. See WithResources.
	containerLimits cgroups.Limits
	keepContainer   bool
}

// WithDockerConfig sets Docker-specific configuration.
func WithDockerConfig(dockerfile, configDir string, keep bool) Option {
	return func(o *options) {
		o.dockerfile = dockerfile
		o.configDir = configDir
		o.keepContainer = keep
	}
}

// WithResources sets the sandbox resource limits. Empty memory and cpus
// strings, and a zero pids count, mean unlimited.
//
// neutral is the backend-neutral [sandbox.resources] section, and is the only
// source the bwrap backend honors. container is that section merged over the
// deprecated [sandbox.docker.resources] one, and is read by docker and krun.
//
// The split is what keeps the deprecated alias backward compatible. bwrap never
// honored a docker-scoped limit, and it enforces limits by aborting the run when
// the host cannot apply them - so feeding it that section would turn a config
// written for the docker backend into a refusal to start for a user who
// daily-drives bwrap. Opting bwrap into enforcement takes an explicit
// [sandbox.resources] block.
func WithResources(neutral, container cgroups.Limits) Option {
	return func(o *options) {
		o.limits = neutral
		o.containerLimits = container
	}
}

// bwrapConfig builds the bwrap backend configuration from the options.
func (o options) bwrapConfig() BwrapConfig {
	return BwrapConfig{Limits: o.limits}
}

// dockerConfig builds the docker backend configuration from the options.
// Resource limits are passed through verbatim: docker gets no defaults.
func (o options) dockerConfig() DockerConfig {
	return DockerConfig{
		Dockerfile:    o.dockerfile,
		ConfigDir:     o.configDir,
		MemoryLimit:   o.containerLimits.Memory,
		CPULimit:      o.containerLimits.CPUs,
		PIDsLimit:     o.containerLimits.PIDs,
		KeepContainer: o.keepContainer,
	}
}

// krunConfig builds the krun backend configuration from the options.
//
// krun runs ephemeral (a fresh microVM per launch) regardless of the
// keep-container setting: a clean guest kernel each run is the whole point of
// using a microVM for untrusted code.
//
// Sane VM resource defaults fill the limits the user left unset. This reuses the
// docker MemoryLimit/CPULimit fields (and the single buildCommonArgs emission),
// so no extra --memory/--cpus path is added; dockerConfig is untouched and
// provably inherits nothing.
//
// PIDsLimit is passed through unchanged even though krun cannot enforce it: the
// suppression and its warning live at the single buildCommonArgs emission site,
// which needs the configured value to report what it dropped.
func (o options) krunConfig() DockerConfig {
	memLimit, cpuLimit := krunResourceDefaults(o.containerLimits.Memory, o.containerLimits.CPUs)
	return DockerConfig{
		Dockerfile:    o.dockerfile,
		ConfigDir:     o.configDir,
		MemoryLimit:   memLimit,
		CPULimit:      cpuLimit,
		PIDsLimit:     o.containerLimits.PIDs,
		KeepContainer: false,
	}
}

// Detect returns the appropriate backend based on configuration and platform.
// Returns an error if the requested backend is unavailable.
func Detect(requested Backend) (Backend, error) {
	switch requested {
	case BackendBwrap:
		return BackendBwrap, nil
	case BackendDocker:
		return BackendDocker, nil
	case BackendKrun:
		return BackendKrun, nil
	case BackendAuto:
		return autoDetect()
	default:
		return "", fmt.Errorf("unknown backend: %s", requested)
	}
}

func autoDetect() (Backend, error) {
	// krun is never auto-selected: it needs podman + the krun OCI runtime +
	// accessible /dev/kvm (or Apple Silicon HVF), which most hosts lack, and it
	// trades startup speed for a hardware boundary that only matters for
	// genuinely untrusted code. It stays opt-in via --isolation krun.
	if runtime.GOOS == "darwin" {
		return BackendDocker, nil
	}
	// Linux: prefer bwrap
	return BackendBwrap, nil
}

// Default microVM resources applied when [sandbox.resources] is left
// unset for the krun backend. A libkrun guest given no hint can be starved or
// oversized, so a sane baseline beats the engine default; explicit config is
// always respected (only empty values are filled).
const (
	defaultKrunMemory = "4g"
	defaultKrunCPUs   = "2"
)

// krunResourceDefaults fills empty memory/cpu limits with the microVM defaults,
// leaving explicit values untouched. The result feeds the same
// DockerConfig.MemoryLimit/CPULimit that buildCommonArgs already emits, so there
// is exactly one --memory/--cpus code path and no double-emit.
func krunResourceDefaults(memLimit, cpuLimit string) (string, string) {
	if memLimit == "" {
		memLimit = defaultKrunMemory
	}
	if cpuLimit == "" {
		cpuLimit = defaultKrunCPUs
	}
	return memLimit, cpuLimit
}

// New creates an isolator for the specified backend.
func New(backend Backend, opts ...Option) (Isolator, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	switch backend {
	case BackendBwrap:
		iso := NewBwrapIsolator(o.bwrapConfig())
		if err := iso.Available(); err != nil {
			return nil, err
		}
		return iso, nil
	case BackendDocker:
		iso := NewDockerIsolator(o.dockerConfig())
		if err := iso.Available(); err != nil {
			return nil, err
		}
		return iso, nil
	case BackendKrun:
		iso := NewKrunIsolator(o.krunConfig())
		if err := iso.Available(); err != nil {
			return nil, err
		}
		return iso, nil
	default:
		return nil, fmt.Errorf("unknown backend: %s", backend)
	}
}

// MustNew creates an isolator, detecting the backend if set to auto.
// On Linux without bwrap, returns an error with installation instructions.
func MustNew(requested Backend, opts ...Option) (Isolator, error) {
	backend, err := Detect(requested)
	if err != nil {
		return nil, err
	}

	iso, err := New(backend, opts...)
	if err != nil && runtime.GOOS == "linux" && requested == BackendAuto {
		return nil, fmt.Errorf("bwrap not available on Linux\n\n%s", err)
	}
	return iso, err
}
