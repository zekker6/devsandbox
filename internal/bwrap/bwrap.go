package bwrap

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"devsandbox/internal/cgroups"
	"devsandbox/internal/egress"
	"devsandbox/internal/embed"
	"devsandbox/internal/network"
)

// wrapLimits places a launch inside a systemd transient scope carrying the
// sandbox resource limits, returning the program and its arguments unchanged
// when no limits are configured. It is a variable so the argv assembly tests
// stay deterministic on hosts without systemd-run installed.
var wrapLimits = cgroups.Wrap

func CheckInstalled() error {
	_, err := embed.BwrapPath()
	if err != nil {
		return fmt.Errorf("bubblewrap (bwrap) is not available: %w\nRun 'devsandbox doctor' for details", err)
	}
	return nil
}

// waitForFirstChildPID polls /proc/<parentPID>/task/<parentPID>/children until
// it contains at least one child PID or the timeout elapses. Returns the first
// child PID (host-visible) or an error on timeout / unreadable procfs entry.
func waitForFirstChildPID(parentPID int, timeout time.Duration) (int, error) {
	if parentPID <= 0 {
		return 0, fmt.Errorf("invalid parent PID %d", parentPID)
	}

	path := fmt.Sprintf("/proc/%d/task/%d/children", parentPID, parentPID)
	const pollInterval = 10 * time.Millisecond
	deadline := time.Now().Add(timeout)

	var lastReadErr error
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err != nil {
			lastReadErr = err
			time.Sleep(pollInterval)
			continue
		}
		for _, field := range strings.Fields(string(data)) {
			pid, parseErr := strconv.Atoi(field)
			if parseErr == nil && pid > 0 {
				return pid, nil
			}
		}
		// A process that died before forking still has a children file, and it
		// reads empty - identical to one that has not forked yet. Without this
		// check a launcher that failed outright is waited out for the whole
		// budget and then reported as a missing sandbox PID, hiding the real
		// cause behind a timeout.
		if processExited(parentPID) {
			return 0, fmt.Errorf("process %d exited without a visible child; the sandbox either failed to start or finished before it could be observed", parentPID)
		}
		time.Sleep(pollInterval)
	}

	if lastReadErr != nil {
		return 0, fmt.Errorf("read %s: %w", path, lastReadErr)
	}
	return 0, fmt.Errorf("no child of PID %d within %s", parentPID, timeout)
}

// processExited reports whether pid has terminated. A process the caller has
// not reaped yet is a zombie, which still has a /proc entry, so the state field
// is what distinguishes it from one that is merely slow to fork.
func processExited(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	// Field 2 is the executable name in parentheses and may itself contain
	// spaces and parentheses, so the state field is the first one after the
	// final ')'.
	i := strings.LastIndexByte(string(data), ')')
	if i < 0 || i+2 >= len(data) {
		return false
	}
	switch data[i+2] {
	case 'Z', 'X', 'x':
		return true
	default:
		return false
	}
}

// pastaSupportsMapHostLoopback checks if the pasta binary at the given path
// supports --map-host-loopback. For embedded binaries, use embed.PastaHasMapHostLoopback instead.
func pastaSupportsMapHostLoopback(pastaPath string) bool {
	cmd := exec.Command(pastaPath, "--help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(output), "--map-host-loopback")
}

// bwrapCmdline assembles bwrap's own arguments, excluding argv[0].
func bwrapCmdline(bwrapArgs, shellCmd []string) []string {
	args := make([]string, 0, len(bwrapArgs)+len(shellCmd)+1)
	args = append(args, bwrapArgs...)
	args = append(args, "--")
	args = append(args, shellCmd...)
	return args
}

// execInvocation returns the program to exec and the full argv including
// argv[0], which syscall.Exec requires the caller to supply. Without limits the
// program is bwrap and argv[0] is "bwrap", exactly as before.
func execInvocation(limits cgroups.Limits, bwrapPath string, bwrapArgs, shellCmd []string) (string, []string, error) {
	prog, args, err := wrapLimits(limits, bwrapPath, bwrapCmdline(bwrapArgs, shellCmd))
	if err != nil {
		return "", nil, err
	}
	return prog, append([]string{filepath.Base(prog)}, args...), nil
}

// runInvocation returns the program and the arguments after argv[0], which
// exec.Command derives from the program path itself.
func runInvocation(limits cgroups.Limits, bwrapPath string, bwrapArgs, shellCmd []string) (string, []string, error) {
	return wrapLimits(limits, bwrapPath, bwrapCmdline(bwrapArgs, shellCmd))
}

func Exec(limits cgroups.Limits, bwrapArgs []string, shellCmd []string) error {
	bwrapPath, err := embed.BwrapPath()
	if err != nil {
		return fmt.Errorf("bwrap not available: %w", err)
	}

	prog, args, err := execInvocation(limits, bwrapPath, bwrapArgs, shellCmd)
	if err != nil {
		return err
	}

	return syscall.Exec(prog, args, os.Environ())
}

// ExecRun runs bwrap using exec.Command instead of syscall.Exec.
// Unlike Exec, this keeps the parent process alive, which is necessary
// when background goroutines (like ActiveTool proxies) need to keep running.
func ExecRun(limits cgroups.Limits, bwrapArgs []string, shellCmd []string) error {
	bwrapPath, err := embed.BwrapPath()
	if err != nil {
		return fmt.Errorf("bwrap not available: %w", err)
	}

	prog, args, err := runInvocation(limits, bwrapPath, bwrapArgs, shellCmd)
	if err != nil {
		return err
	}

	cmd := exec.Command(prog, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	return cmd.Run()
}

// SandboxProcess holds a running sandbox process started by StartWithPasta.
type SandboxProcess struct {
	Cmd          *exec.Cmd
	NamespacePID int // PID inside the sandbox network namespace
}

// NamespacePath returns the /proc path to the sandbox network namespace.
func (p *SandboxProcess) NamespacePath() string {
	return fmt.Sprintf("/proc/%d/ns/net", p.NamespacePID)
}

// Wait waits for the sandbox process to exit.
func (p *SandboxProcess) Wait() error {
	return p.Cmd.Wait()
}

// StartWithPasta starts bwrap inside pasta for network namespace isolation and
// returns immediately after the process is running. Call Wait() on the returned
// SandboxProcess to block until the sandbox exits.
//
// The portForwardArgs parameter accepts pasta port forwarding arguments (e.g., -t, -u, -T, -U).
// Pass nil if no port forwarding is needed.
//
// lockdown describes the proxy-only egress restriction. When it is enabled the
// wrapper script applies a deny-by-default lockdown before it execs the
// workload, and the launch aborts rather than starting the workload with egress
// open; a zero value leaves the namespace exactly as it was before.
//
// tools are the host binaries that lockdown renders into the wrapper script,
// resolved by the caller. They are passed in rather than resolved here so the
// binaries the caller's preflight proved usable are the exact ones the script
// runs; a second resolution could disagree with the one that was verified. They
// are unused when the lockdown is disabled.
func StartWithPasta(limits cgroups.Limits, bwrapArgs []string, shellCmd []string, portForwardArgs []string, lockdown egress.Lockdown, tools egress.Tools) (*SandboxProcess, error) {
	pastaPath, err := embed.PastaPath()
	if err != nil {
		return nil, fmt.Errorf("pasta not available (required for proxy mode): %w\nRun 'devsandbox doctor' for details", err)
	}

	bwrapPath, err := embed.BwrapPath()
	if err != nil {
		return nil, fmt.Errorf("bwrap not available: %w", err)
	}

	// Use --map-host-loopback if supported.
	// For embedded pasta, we know the version at build time.
	// For system pasta (fallback), check at runtime.
	supportsMapHostLoopback := embed.PastaHasMapHostLoopback
	if !embed.IsEmbedded(pastaPath) {
		supportsMapHostLoopback = pastaSupportsMapHostLoopback(pastaPath)
	}

	prog, args, err := pastaInvocation(limits, pastaPath, bwrapPath, bwrapArgs, shellCmd, portForwardArgs, supportsMapHostLoopback, lockdown, tools)
	if err != nil {
		return nil, err
	}

	// Use exec.Command instead of syscall.Exec so the parent process stays alive.
	// This is necessary because we have a proxy server goroutine running.
	cmd := exec.Command(prog, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start pasta/bwrap: %w", err)
	}

	// Pasta creates a new PID namespace for its child, so `echo $$` inside the
	// wrapper would record PID 1 (namespace-local), not a host-visible PID.
	// Instead, find pasta's direct child via procfs - that PID is host-visible
	// and lives inside pasta's network namespace, which is exactly what callers
	// need for /proc/<pid>/ns/net and liveness checks.
	namespacePID, err := waitForFirstChildPID(cmd.Process.Pid, pastaStartTimeout(limits))
	if err != nil {
		_ = cmd.Process.Kill()
		waitErr := cmd.Wait()
		// A lockdown that aborts before the child is observable exits the wrapper
		// with LockdownExitCode, and that status is the only evidence of what
		// happened. Discarding it here reports an unapplied security control as a
		// PID-discovery timeout, so it is surfaced for the caller to map back to a
		// named lockdown error instead.
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) && ee.ExitCode() == egress.LockdownExitCode {
			return nil, waitErr
		}
		return nil, fmt.Errorf("locate sandbox PID under pasta: %w", err)
	}

	return &SandboxProcess{
		Cmd:          cmd,
		NamespacePID: namespacePID,
	}, nil
}

// pastaInvocation returns the program and the arguments after argv[0] for the
// proxy launch path, where pasta is the outermost process and therefore the one
// the scope must contain.
func pastaInvocation(limits cgroups.Limits, pastaPath, bwrapPath string, bwrapArgs, shellCmd, portForwardArgs []string, mapHostLoopback bool, lockdown egress.Lockdown, tools egress.Tools) (string, []string, error) {
	args, err := pastaCmdline(bwrapPath, bwrapArgs, shellCmd, portForwardArgs, mapHostLoopback, lockdown, tools)
	if err != nil {
		return "", nil, err
	}
	return wrapLimits(limits, pastaPath, args)
}

// Budgets for pasta to fork the sandbox process. A systemd transient scope adds
// a D-Bus round trip to the user manager before systemd-run execs pasta, so the
// limited path needs headroom the original budget - sized for pasta starting
// immediately - does not have.
const (
	pastaStartBudget       = 2 * time.Second
	pastaScopedStartBudget = 10 * time.Second
)

func pastaStartTimeout(limits cgroups.Limits) time.Duration {
	if limits.IsZero() {
		return pastaStartBudget
	}
	return pastaScopedStartBudget
}

// pastaCmdline assembles pasta's arguments, excluding argv[0].
func pastaCmdline(bwrapPath string, bwrapArgs, shellCmd, portForwardArgs []string, mapHostLoopback bool, lockdown egress.Lockdown, tools egress.Tools) ([]string, error) {
	// Build pasta command with network isolation:
	// pasta --config-net [-4] [--map-host-loopback 10.0.2.2] -f -- sh -c '...' _ bwrap [args] -- shell
	//
	// --config-net: Configure tap interface in namespace (required for network to work)
	// -4: IPv4 only, emitted with the lockdown (see below)
	// --map-host-loopback 10.0.2.2: Map 10.0.2.2 to host's 127.0.0.1 (for proxy access)
	//   Note: This option is not available in older pasta versions (pre-2023)
	// -f: Run in foreground (pasta exits when child exits)
	wrapperScript, err := pastaWrapperScript(lockdown, tools)
	if err != nil {
		return nil, err
	}

	// The lockdown's only egress accept points at the gateway, and the gateway is
	// reachable only because --map-host-loopback maps it to the host's loopback.
	// A pasta too old for that option would therefore leave the sandbox with no
	// default route, a DROP policy, and one accept for an address that maps to
	// nothing - every connection, the proxy's included, hanging until it times
	// out with nothing said about why. Refuse instead, naming the missing
	// prerequisite. Only proxy mode gains this dependency; a non-proxy launch
	// still runs on such a pasta exactly as before.
	if lockdown.Enabled && !mapHostLoopback {
		return nil, fmt.Errorf("proxy mode needs a pasta that supports --map-host-loopback, and this one does not: "+
			"without it nothing maps the proxy gateway %s, so the egress lockdown would leave the sandbox unable to "+
			"reach anything at all\nUse the embedded pasta (remove 'use_embedded = false') or upgrade passt", lockdown.Gateway)
	}

	args := make([]string, 0, len(bwrapArgs)+len(shellCmd)+len(portForwardArgs)+16)
	args = append(args, "--config-net") // Configure network interface

	if lockdown.Enabled {
		// IPv4 only, matching internal/isolator/docker.go's krun invocation. The
		// lockdown's ruleset is IPv4 (the proxy gateway is 10.0.2.2, so every
		// permitted flow is), and `ip route del default` is IPv4-only too. On a
		// dual-stack host pasta would otherwise configure IPv6 in the namespace
		// and leave a second family for which nothing is filtered and no route is
		// removed - an IPv4-only ruleset with an IPv6-capable namespace enforces
		// nothing. Removing the family is what keeps the two in sync.
		args = append(args, "-4")
	}

	if mapHostLoopback {
		// With a lockdown, map the address the rules were built from rather than
		// the package constant. The two agree today because the only network
		// provider is pasta, but nothing enforces that, and a disagreement would
		// map the host loopback at one address while the firewall permitted only
		// another - the same total, undiagnosed egress loss the guard above
		// refuses, arriving silently instead.
		gateway := network.PastaGatewayIP
		if lockdown.Enabled {
			gateway = lockdown.Gateway
		}
		args = append(args, "--map-host-loopback", gateway)
	}

	// Add port forwarding arguments
	args = append(args, portForwardArgs...)

	if lockdown.Enabled {
		// Turn off pasta's automatic namespace->init forwarding for every protocol
		// the rules above did not already pin. Left at its `auto` default it binds
		// each host-listening port inside the namespace on loopback, where the
		// firewall's mandatory `oif lo accept` waves it through - so the host's own
		// 127.0.0.1 services would stay directly reachable from the sandbox while
		// every other direct destination was refused.
		args = append(args, egress.NoAutoForwardArgs(portForwardArgs)...)
	}

	args = append(args, "-f") // Foreground mode
	args = append(args, "--")
	args = append(args, "sh", "-c", wrapperScript, "_") // Wrapper to capture PID and delete default route
	args = append(args, bwrapPath)
	args = append(args, bwrapArgs...)
	args = append(args, "--")
	args = append(args, shellCmd...)

	return args, nil
}

// pastaWrapperScript returns the shell prologue pasta runs before it execs
// bwrap. With a lockdown it is the fail-closed egress prologue: the wrapper runs
// as root in pasta's user namespace, holding CAP_NET_ADMIN over the netns,
// before the workload exists - so there is no window in which sandboxed code
// runs with egress open, and any failing step aborts instead of exec'ing.
//
// Without one (a non-proxy launch) it is the historical best-effort route
// surgery, unchanged: the ip calls discard errors and the exec is unconditional.
// That path has no proxy to steer traffic to, so nothing about it changes here.
func pastaWrapperScript(lockdown egress.Lockdown, tools egress.Tools) (string, error) {
	if !lockdown.Enabled {
		return fmt.Sprintf(`
		dev=$(ip -o route show default | awk '{print $5}')
		ip route add %s/32 dev "$dev" 2>/dev/null
		ip route del default 2>/dev/null
		exec "$@"
	`, network.PastaGatewayIP), nil
	}

	script, err := egress.Script(tools, lockdown)
	if err != nil {
		return "", fmt.Errorf("render the egress lockdown: %w", err)
	}
	return script, nil
}

// ExecWithPasta wraps bwrap execution inside pasta for network namespace isolation.
// This creates an isolated network namespace whose only interface is pasta's tap
// device. With a lockdown, StartWithPasta additionally applies a deny-by-default
// egress restriction inside that namespace before the workload starts, so the
// only reachable destination is the proxy port on the gateway.
//
// It returns the wrapper's exit error as-is, which for a lockdown launch is
// ambiguous: an abort and a workload that exits egress.LockdownExitCode look
// identical here. A caller that needs to tell them apart must check
// lockdown.ReadyFile with egress.LockdownApplied, as internal/isolator does.
//
// The portForwardArgs parameter accepts pasta port forwarding arguments (e.g., -t, -u, -T, -U).
// Pass nil if no port forwarding is needed.
//
// Unlike the regular Exec function, this uses exec.Command instead of syscall.Exec
// so that the calling process (and its proxy server goroutine) stays alive.
func ExecWithPasta(limits cgroups.Limits, bwrapArgs []string, shellCmd []string, portForwardArgs []string, lockdown egress.Lockdown, tools egress.Tools) error {
	proc, err := StartWithPasta(limits, bwrapArgs, shellCmd, portForwardArgs, lockdown, tools)
	if err != nil {
		return err
	}
	return proc.Wait()
}
