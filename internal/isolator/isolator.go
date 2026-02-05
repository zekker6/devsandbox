// Package isolator provides an abstraction over sandbox backends (bwrap, docker).
package isolator

import (
	"context"
	"fmt"
	"runtime"
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

// Config contains settings passed to the isolator.
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
	// HideEnvFiles enables .env file hiding (Docker only).
	HideEnvFiles bool
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

// Isolator is the interface for sandbox backends.
type Isolator interface {
	// Name returns the backend name.
	Name() Backend
	// Available checks if this backend can be used.
	Available() error
	// Build constructs the command to run the sandbox.
	// Returns the executable path and arguments.
	Build(ctx context.Context, cfg *Config) (string, []string, error)
	// Cleanup performs any post-sandbox cleanup.
	Cleanup() error
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
func New(backend Backend, dockerCfg DockerConfig) (Isolator, error) {
	switch backend {
	case BackendBwrap:
		iso := NewBwrapIsolator()
		if err := iso.Available(); err != nil {
			return nil, err
		}
		return iso, nil
	case BackendDocker:
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
func MustNew(requested Backend, dockerCfg DockerConfig) (Isolator, error) {
	backend, err := Detect(requested)
	if err != nil {
		return nil, err
	}

	// On Linux with auto-detect, if bwrap fails, don't fall back silently
	if runtime.GOOS == "linux" && requested == BackendAuto {
		iso := NewBwrapIsolator()
		if err := iso.Available(); err != nil {
			return nil, fmt.Errorf("bwrap not available on Linux\n\n%s", err)
		}
		return iso, nil
	}

	return New(backend, dockerCfg)
}
