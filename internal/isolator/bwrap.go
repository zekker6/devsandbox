package isolator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"devsandbox/internal/bwrap"
	"devsandbox/internal/cgroups"
	"devsandbox/internal/egress"
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
	startWithPasta func(cgroups.Limits, []string, []string, []string, egress.Lockdown, egress.Tools) (*bwrap.SandboxProcess, error)
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
		lockdown := egressLockdown(cfg)
		tools, err := preflightEgressLockdown(lockdown)
		if err != nil {
			return err
		}
		// The wrapper prologue creates this file immediately before it execs the
		// workload, which is what lets asLockdownOrCommandExit tell an aborted
		// lockdown from a workload that exits with the same status. It lives
		// where the sandbox cannot write, and is removed when the launch returns.
		readyDir, err := egressMarkerDir()
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(readyDir) }()
		lockdown.ReadyFile = filepath.Join(readyDir, "applied")

		proc, err := launchers.startWithPasta(b.config.Limits, bwrapArgs, shellCmd, portForwardArgs, lockdown, tools)
		if err != nil {
			// A lockdown that aborted before the sandbox PID was observable
			// surfaces here as the wrapper's exit status rather than as a start
			// failure, so it goes through the same mapping as an abort seen by
			// Wait.
			return asLockdownOrCommandExit(err, lockdown.ReadyFile)
		}
		if cfg.OnSandboxStart != nil {
			cfg.OnSandboxStart(proc.NamespacePID, proc.NamespacePath())
		}
		return asLockdownOrCommandExit(proc.Wait(), lockdown.ReadyFile)

	case cfg.HasActiveTools || cfg.RemoveOnExit || cfg.SandboxCfg.IsConcurrent:
		return asCommandExit(launchers.execRun(b.config.Limits, bwrapArgs, shellCmd))

	default:
		// bwrap.Exec replaces this process via syscall.Exec, so the child's exit
		// status becomes ours automatically; a returned error is an exec failure.
		return launchers.exec(b.config.Limits, bwrapArgs, shellCmd)
	}
}

// egressMarkerDir creates the host directory the lockdown marker is written in,
// rooted at $XDG_STATE_HOME/devsandbox (falling back to ~/.local/state/devsandbox)
// the way every other host-owned record in this project is.
//
// Not $TMPDIR, which is what os.MkdirTemp("", ...) resolves against: $TMPDIR is
// whatever the invoking user set, so it can name a directory bound read-write
// into the sandbox - and a workload that can delete the marker can make its own
// exit code 78 read as an aborted lockdown, which is exactly the signal the
// marker exists to give. The sandbox repoints XDG_STATE_HOME at its synthetic
// home, so this path is unreachable from inside no matter how the host is
// configured.
func egressMarkerDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve the home directory for the egress lockdown marker: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	base = filepath.Join(base, "devsandbox", "egress")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", fmt.Errorf("create the egress lockdown marker directory %s: %w", base, err)
	}
	dir, err := os.MkdirTemp(base, "lockdown-")
	if err != nil {
		return "", fmt.Errorf("create the egress lockdown marker directory: %w", err)
	}
	return dir, nil
}

// egressLockdown describes the proxy-only egress restriction the pasta wrapper
// applies before it execs the workload. A non-proxy launch gets the zero value,
// which renders no lockdown and leaves the pasta invocation exactly as it was -
// the lockdown must never become a dependency of the default path.
func egressLockdown(cfg *RunConfig) egress.Lockdown {
	if !cfg.SandboxCfg.ProxyEnabled {
		return egress.Lockdown{}
	}
	return egress.Lockdown{
		Enabled:   true,
		Gateway:   cfg.SandboxCfg.GatewayIP,
		ProxyPort: cfg.SandboxCfg.ProxyPort,
		Forwards:  outboundForwards(cfg),
	}
}

// outboundForwards collects the ports pasta forwards out of the sandbox toward
// the host (-T/-U). Only outbound rules are relevant: the lockdown filters the
// OUTPUT hook, and an inbound forward is an arriving connection whose replies
// already match the established/related accept.
func outboundForwards(cfg *RunConfig) []egress.Forward {
	if cfg.AppCfg == nil || !cfg.AppCfg.PortForwarding.IsEnabled() {
		return nil
	}
	var forwards []egress.Forward
	for _, rule := range cfg.AppCfg.PortForwarding.Rules {
		if rule.Direction != "outbound" {
			continue
		}
		forwards = append(forwards, egress.Forward{Port: rule.HostPort, UDP: rule.Protocol == "udp"})
	}
	return forwards
}

// The two host-touching halves of the preflight, indirected so a test can drive
// both outcomes on any host: the check runs before every proxy launch, so a test
// that could not stub it would depend on whether the machine running it has
// netfilter.
var (
	resolveEgressTools  = func() (egress.Tools, error) { return egress.ResolveTools(exec.LookPath) }
	probeEgressLockdown = egress.Probe
)

// ErrEgressPreflight reports that proxy mode was requested on a host that cannot
// apply the egress lockdown. Without the preflight the same host still fails -
// the wrapper script aborts - but only after pasta and bwrap have started, and
// the user sees an exit code where they should see a missing prerequisite.
var ErrEgressPreflight = errors.New("proxy mode needs an enforceable egress lockdown, and this host cannot apply one")

// preflightEgressLockdown verifies the lockdown is applicable before pasta and
// bwrap are started, and returns the resolved binaries for the launch to render.
// The proxy server and the CA are already up by this point - it is the sandbox,
// and with it the workload, that has not started yet. It
// applies the rule set in a throwaway namespace, so a missing binary, an
// unloadable netfilter module, or a refused rule is named here rather than
// surfacing as an opaque abort mid-launch. Returning the tools rather than
// letting the wrapper resolve its own is what makes "the same binaries the
// wrapper script will run" literally true: a second resolution could pick up a
// different PATH and run something the probe never verified. A launch with no
// lockdown - every non-proxy launch - skips it entirely and gets a zero Tools:
// the lockdown must not become a dependency of the default path.
func preflightEgressLockdown(l egress.Lockdown) (egress.Tools, error) {
	if !l.Enabled {
		return egress.Tools{}, nil
	}
	tools, err := resolveEgressTools()
	if err != nil {
		return egress.Tools{}, preflightError(err)
	}
	if err := probeEgressLockdown(tools, l); err != nil {
		return egress.Tools{}, preflightError(err)
	}
	return tools, nil
}

// preflightError attaches the remediation to whatever the preflight found. The
// underlying error already names which piece is missing - the binary, the
// namespace, or the rule application - so this only adds what to do about it.
func preflightError(err error) error {
	return fmt.Errorf("%w: %w\n\n%s\nRun 'devsandbox doctor' and see the 'proxy: firewall' row", ErrEgressPreflight, err, egress.CheckHint)
}

// ErrEgressLockdown reports that the egress lockdown aborted the launch from
// inside the pasta wrapper. The workload never started on this path.
var ErrEgressLockdown = errors.New("the proxy-only egress lockdown could not be applied inside the sandbox network namespace, so the sandbox was never started")

// asLockdownOrCommandExit is asCommandExit for the pasta launch path. The
// wrapper script exits egress.LockdownExitCode when a lockdown step fails, and
// `sh` is pasta's first child - so waitForFirstChildPID has typically already
// returned and the abort arrives here, via Wait, carrying the same shape as a
// workload exit status. Mapping it back to a named error is what keeps an
// unapplied security control from being reported as if the sandboxed program had
// exited 78. The script writes its own diagnostic to stderr first, so the error
// points there rather than restating it.
//
// The status alone does not settle it, and readyFile is why this is not a guess.
// The script clears its EXIT trap before `exec "$@"`, so a workload that exits 78
// of its own accord reaches here with exactly the same status as an abort -
// while the marker exists only on the path that got as far as the exec. So a 78
// with the marker present is the workload's own status and propagates silently,
// like any other; a 78 without it is the lockdown, named. Getting this backwards
// is harmful in both directions: it either hides an unenforced security control
// behind a plausible exit code, or turns a normal command exit into a security
// error and replaces its status with 1.
func asLockdownOrCommandExit(err error, readyFile string) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == egress.LockdownExitCode && !egress.LockdownApplied(readyFile) {
		return fmt.Errorf("%w (see the 'devsandbox: egress lockdown:' message above): %w", ErrEgressLockdown, err)
	}
	return asCommandExit(err)
}

// Cleanup performs any post-sandbox cleanup.
// BwrapIsolator has no cleanup requirements.
func (b *BwrapIsolator) Cleanup() error {
	return nil
}
