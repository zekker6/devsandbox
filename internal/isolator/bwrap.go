package isolator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"devsandbox/internal/bwrap"
	"devsandbox/internal/embed"
	"devsandbox/internal/logging"
	"devsandbox/internal/network"
	"devsandbox/internal/proxy"
	"devsandbox/internal/sandbox"
)

// BwrapIsolator implements Isolator using bubblewrap.
type BwrapIsolator struct{}

// NewBwrapIsolator creates a new bwrap isolator.
func NewBwrapIsolator() *BwrapIsolator {
	return &BwrapIsolator{}
}

// Name returns the backend name.
func (b *BwrapIsolator) Name() Backend {
	return BackendBwrap
}

// Available checks if bwrap is available (embedded or system-installed).
func (b *BwrapIsolator) Available() error {
	_, err := embed.BwrapPath()
	if err != nil {
		return errors.New("bubblewrap (bwrap) is not available\n" +
			"Embedded binary extraction failed and system binary not found.\n" +
			"Install with:\n" +
			"  Arch:   pacman -S bubblewrap\n" +
			"  Debian: apt install bubblewrap\n" +
			"  Fedora: dnf install bubblewrap\n\n" +
			"Or use Docker backend: devsandbox --isolation=docker")
	}
	return nil
}

// IsolationType returns the sandbox isolation type for metadata.
func (b *BwrapIsolator) IsolationType() sandbox.IsolationType {
	return sandbox.IsolationBwrap
}

// PrepareNetwork is a no-op for bwrap. Network setup (pasta) is handled inside Run().
func (b *BwrapIsolator) PrepareNetwork(_ context.Context, _ string) (*NetworkInfo, error) {
	return nil, nil
}

// Run executes the full bwrap sandbox lifecycle.
func (b *BwrapIsolator) Run(ctx context.Context, cfg *RunConfig) error {
	if err := bwrap.CheckInstalled(); err != nil {
		return err
	}

	sandboxCfg := cfg.SandboxCfg

	// Set up structured logging
	logDir := filepath.Join(sandboxCfg.SandboxHome, proxy.LogBaseDirName, proxy.InternalLogDirName)
	sandboxLogger := cfg.SandboxLogger
	if sandboxLogger == nil {
		sandboxLogger, _ = logging.NewErrorLogger(filepath.Join(logDir, "sandbox.log"))
	}

	sandboxCfg.Logger = logging.NewComponentLogger("builder", sandboxLogger, cfg.LogDispatcher)
	if sandboxCfg.MountsConfig != nil {
		sandboxCfg.MountsConfig.SetLogger(logging.NewComponentLogger("mounts", sandboxLogger, cfg.LogDispatcher))
	}

	// Handle proxy mode — detect pasta for network isolation
	var netProvider network.Provider
	if sandboxCfg.ProxyEnabled {
		var err error
		netProvider, err = network.SelectProvider()
		if err != nil {
			return fmt.Errorf("proxy mode requires pasta: %w\nRun 'devsandbox doctor' for installation instructions", err)
		}

		sandboxCfg.NetworkIsolated = netProvider.NetworkIsolated()
		sandboxCfg.ProxyPort = cfg.ProxyPort
		sandboxCfg.GatewayIP = netProvider.GatewayIP()
		sandboxCfg.ProxyCAPath = cfg.ProxyCAPath
	}

	// Build sandbox arguments
	builder := sandbox.NewBuilder(sandboxCfg)
	builder.AddBaseArgs()
	builder.AddSystemBindings()
	builder.AddNetworkBindings()
	builder.AddLocaleBindings()
	builder.AddCABindings()
	builder.AddCustomMounts()
	builder.AddSandboxHome()
	builder.AddProjectBindings()
	builder.AddTools()
	builder.SuppressSSHAgent()
	builder.AddProxyCACertificate()
	builder.AddEnvironment()

	if err := builder.Err(); err != nil {
		return fmt.Errorf("failed to build sandbox: %w", err)
	}

	bwrapArgs := builder.Build()
	shellCmd := sandbox.BuildShellCommand(sandboxCfg, cfg.Command)

	// Debug output
	if os.Getenv("DEVSANDBOX_DEBUG") != "" {
		fmt.Fprintln(os.Stderr, "=== Sandbox Debug ===")
		fmt.Fprintln(os.Stderr, "bwrap \\")
		for _, arg := range bwrapArgs {
			fmt.Fprintf(os.Stderr, "    %s \\\n", arg)
		}
		fmt.Fprintf(os.Stderr, "    -- %v\n", shellCmd)
		fmt.Fprintln(os.Stderr, "===================")
	}

	// Validate port forwarding requirements
	if cfg.AppCfg.PortForwarding.IsEnabled() && len(cfg.AppCfg.PortForwarding.Rules) > 0 {
		if !sandboxCfg.NetworkIsolated {
			return fmt.Errorf("port forwarding requires network isolation (pasta), but network is not isolated; " +
				"without network isolation, the sandbox already has direct network access to the host; " +
				"either enable proxy mode (--proxy) or remove port_forwarding configuration")
		}
	}

	// Build port forwarding args for pasta
	var portForwardArgs []string
	if cfg.AppCfg.PortForwarding.IsEnabled() {
		portForwardArgs = sandbox.BuildPastaPortArgs(cfg.AppCfg.PortForwarding.Rules)
	}

	// Execute the sandbox
	if sandboxCfg.ProxyEnabled {
		return bwrap.ExecWithPasta(bwrapArgs, shellCmd, portForwardArgs)
	}

	if cfg.HasActiveTools || cfg.RemoveOnExit {
		return bwrap.ExecRun(bwrapArgs, shellCmd)
	}

	return bwrap.Exec(bwrapArgs, shellCmd)
}

// Cleanup performs any post-sandbox cleanup.
// BwrapIsolator has no cleanup requirements.
func (b *BwrapIsolator) Cleanup() error {
	return nil
}
