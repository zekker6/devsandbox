// Package isolator provides an abstraction over sandbox backends (bwrap, docker).
package isolator

import (
	"context"
	"fmt"
	"runtime"

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
}

// Isolator is the interface for sandbox backends.
type Isolator interface {
	// Name returns the backend name.
	Name() Backend
	// Available checks if this backend can be used.
	Available() error
	// IsolationType returns the sandbox isolation type for metadata.
	IsolationType() sandbox.IsolationType
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
	// SandboxHome is the per-project sandbox home directory.
	SandboxHome string
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
	// Environment variables to set.
	Environment map[string]string
	// Bindings are filesystem mounts (translated per-backend).
	Bindings []Binding
	// ToolsConfig contains tool-specific configuration from config file.
	ToolsConfig map[string]any
	// OverlayEnabled indicates if overlay mounts are enabled.
	OverlayEnabled bool
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
	dockerfile    string
	configDir     string
	memoryLimit   string
	cpuLimit      string
	keepContainer bool
}

// WithDockerConfig sets Docker-specific configuration.
// Ignored by non-Docker backends.
func WithDockerConfig(dockerfile, configDir, memLimit, cpuLimit string, keep bool) Option {
	return func(o *options) {
		o.dockerfile = dockerfile
		o.configDir = configDir
		o.memoryLimit = memLimit
		o.cpuLimit = cpuLimit
		o.keepContainer = keep
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
	case BackendAuto:
		return autoDetect()
	default:
		return "", fmt.Errorf("unknown backend: %s", requested)
	}
}

func autoDetect() (Backend, error) {
	if runtime.GOOS == "darwin" {
		return BackendDocker, nil
	}
	// Linux: prefer bwrap
	return BackendBwrap, nil
}

// New creates an isolator for the specified backend.
func New(backend Backend, opts ...Option) (Isolator, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	switch backend {
	case BackendBwrap:
		iso := NewBwrapIsolator()
		if err := iso.Available(); err != nil {
			return nil, err
		}
		return iso, nil
	case BackendDocker:
		dockerCfg := DockerConfig{
			Dockerfile:    o.dockerfile,
			ConfigDir:     o.configDir,
			MemoryLimit:   o.memoryLimit,
			CPULimit:      o.cpuLimit,
			KeepContainer: o.keepContainer,
		}
		iso := NewDockerIsolator(dockerCfg)
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
