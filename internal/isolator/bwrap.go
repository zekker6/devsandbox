package isolator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"devsandbox/internal/bwrap"
	"devsandbox/internal/cgroups"
	"devsandbox/internal/embed"
	"devsandbox/internal/logging"
	"devsandbox/internal/network"
	"devsandbox/internal/notice"
	"devsandbox/internal/proxy"
	"devsandbox/internal/sandbox"
)

// BwrapConfig contains bwrap-specific settings.
type BwrapConfig struct {
	// Limits are the sandbox resource caps. A zero value means unlimited, which
	// is the bwrap default: limits are opt-in.
	Limits cgroups.Limits
}

// BwrapIsolator implements Isolator using bubblewrap.
type BwrapIsolator struct {
	config BwrapConfig
}

// NewBwrapIsolator creates a new bwrap isolator.
func NewBwrapIsolator(cfg BwrapConfig) *BwrapIsolator {
	return &BwrapIsolator{config: cfg}
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

// Preflight verifies the configured resource limits can actually be enforced,
// so a limit that would be silently ignored aborts the run before any mount or
// namespace setup happens. It is otherwise a no-op: concurrent same-project
// sessions are supported via per-session overlay dirs, not refused, so there is
// no launch-time conflict to detect.
func (b *BwrapIsolator) Preflight(_ context.Context, _ string) error {
	return cgroups.Preflight(b.config.Limits)
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
	builder.AddHomeCustomMounts()
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

	// Debug output. Wrapping an argv-less "bwrap" yields exactly the systemd-run
	// scope prefix the real launch uses, or "bwrap" alone when unlimited, so the
	// dump never claims an invocation that is not the one being run.
	if os.Getenv("DEVSANDBOX_DEBUG") != "" {
		launcher, prefix, err := cgroups.Wrap(b.config.Limits, "bwrap", nil)
		if err != nil {
			return fmt.Errorf("failed to apply resource limits: %w", err)
		}
		var sb strings.Builder
		sb.WriteString("=== Sandbox Debug ===\n")
		fmt.Fprintf(&sb, "%s \\\n", launcher)
		for _, arg := range slices.Concat(prefix, bwrapArgs) {
			fmt.Fprintf(&sb, "    %s \\\n", arg)
		}
		fmt.Fprintf(&sb, "    -- %v\n", shellCmd)
		sb.WriteString("===================")
		notice.Info("%s", sb.String())
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

	return b.launch(cfg, bwrapArgs, shellCmd, portForwardArgs)
}

// bwrapLaunchers holds the three bwrap entry points the dispatch chooses
// between. They are indirected so a test can observe what each one is handed:
// the configured limits reach the sandbox through these arguments and through
// nothing else, so an argument that silently stopped carrying them would
// otherwise leave every check in this package - and in internal/bwrap - green.
type bwrapLaunchers struct {
	startWithPasta func(cgroups.Limits, []string, []string, []string) (*bwrap.SandboxProcess, error)
	execRun        func(cgroups.Limits, []string, []string) error
	exec           func(cgroups.Limits, []string, []string) error
}

// launchers is process-global and swapped by tests, so those tests must not call
// t.Parallel().
var launchers = bwrapLaunchers{
	startWithPasta: bwrap.StartWithPasta,
	execRun:        bwrap.ExecRun,
	exec:           bwrap.Exec,
}

// launch runs the sandbox through the bwrap entry point cfg selects. Every
// launch path funnels through here, so there is exactly one place a configured
// limit could fail to reach the sandbox.
func (b *BwrapIsolator) launch(cfg *RunConfig, bwrapArgs, shellCmd, portForwardArgs []string) error {
	switch {
	case cfg.SandboxCfg.ProxyEnabled:
		proc, err := launchers.startWithPasta(b.config.Limits, bwrapArgs, shellCmd, portForwardArgs)
		if err != nil {
			return err
		}
		if cfg.OnSandboxStart != nil {
			cfg.OnSandboxStart(proc.NamespacePID, proc.NamespacePath())
		}
		return asCommandExit(proc.Wait())

	case cfg.HasActiveTools || cfg.RemoveOnExit || cfg.SandboxCfg.IsConcurrent:
		return asCommandExit(launchers.execRun(b.config.Limits, bwrapArgs, shellCmd))

	default:
		// bwrap.Exec replaces this process via syscall.Exec, so the child's exit
		// status becomes ours automatically; a returned error is an exec failure.
		return launchers.exec(b.config.Limits, bwrapArgs, shellCmd)
	}
}

// Cleanup performs any post-sandbox cleanup.
// BwrapIsolator has no cleanup requirements.
func (b *BwrapIsolator) Cleanup() error {
	return nil
}
