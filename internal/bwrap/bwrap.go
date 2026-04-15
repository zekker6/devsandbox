package bwrap

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"devsandbox/internal/embed"
	"devsandbox/internal/network"
)

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
		time.Sleep(pollInterval)
	}

	if lastReadErr != nil {
		return 0, fmt.Errorf("read %s: %w", path, lastReadErr)
	}
	return 0, fmt.Errorf("no child of PID %d within %s", parentPID, timeout)
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

func Exec(bwrapArgs []string, shellCmd []string) error {
	bwrapPath, err := embed.BwrapPath()
	if err != nil {
		return fmt.Errorf("bwrap not available: %w", err)
	}

	args := make([]string, 0, len(bwrapArgs)+len(shellCmd)+2)
	args = append(args, "bwrap")
	args = append(args, bwrapArgs...)
	args = append(args, "--")
	args = append(args, shellCmd...)

	return syscall.Exec(bwrapPath, args, os.Environ())
}

// ExecRun runs bwrap using exec.Command instead of syscall.Exec.
// Unlike Exec, this keeps the parent process alive, which is necessary
// when background goroutines (like ActiveTool proxies) need to keep running.
func ExecRun(bwrapArgs []string, shellCmd []string) error {
	bwrapPath, err := embed.BwrapPath()
	if err != nil {
		return fmt.Errorf("bwrap not available: %w", err)
	}

	args := make([]string, 0, len(bwrapArgs)+len(shellCmd)+2)
	args = append(args, bwrapArgs...)
	args = append(args, "--")
	args = append(args, shellCmd...)

	cmd := exec.Command(bwrapPath, args...)
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
func StartWithPasta(bwrapArgs []string, shellCmd []string, portForwardArgs []string) (*SandboxProcess, error) {
	pastaPath, err := embed.PastaPath()
	if err != nil {
		return nil, fmt.Errorf("pasta not available (required for proxy mode): %w\nRun 'devsandbox doctor' for details", err)
	}

	bwrapPath, err := embed.BwrapPath()
	if err != nil {
		return nil, fmt.Errorf("bwrap not available: %w", err)
	}

	// Build pasta command with network isolation:
	// pasta --config-net [--map-host-loopback 10.0.2.2] -f -- sh -c '...' _ bwrap [args] -- shell
	//
	// --config-net: Configure tap interface in namespace (required for network to work)
	// --map-host-loopback 10.0.2.2: Map 10.0.2.2 to host's 127.0.0.1 (for proxy access)
	//   Note: This option is not available in older pasta versions (pre-2023)
	// -f: Run in foreground (pasta exits when child exits)
	//
	// The wrapper script restricts network to proxy-only:
	// 1. Add a host route to gateway via the tap device
	// 2. Delete the default route to block direct internet access
	// This forces all traffic through our proxy - direct connections to external IPs will fail.
	wrapperScript := fmt.Sprintf(`
		dev=$(ip -o route show default | awk '{print $5}')
		ip route add %s/32 dev "$dev" 2>/dev/null
		ip route del default 2>/dev/null
		exec "$@"
	`, network.PastaGatewayIP)

	args := make([]string, 0, len(bwrapArgs)+len(shellCmd)+len(portForwardArgs)+16)
	args = append(args, "--config-net") // Configure network interface

	// Use --map-host-loopback if supported.
	// For embedded pasta, we know the version at build time.
	// For system pasta (fallback), check at runtime.
	supportsMapHostLoopback := embed.PastaHasMapHostLoopback
	if !embed.IsEmbedded(pastaPath) {
		supportsMapHostLoopback = pastaSupportsMapHostLoopback(pastaPath)
	}
	if supportsMapHostLoopback {
		args = append(args, "--map-host-loopback", network.PastaGatewayIP)
	}

	// Add port forwarding arguments
	args = append(args, portForwardArgs...)

	args = append(args, "-f") // Foreground mode
	args = append(args, "--")
	args = append(args, "sh", "-c", wrapperScript, "_") // Wrapper to capture PID and delete default route
	args = append(args, bwrapPath)
	args = append(args, bwrapArgs...)
	args = append(args, "--")
	args = append(args, shellCmd...)

	// Use exec.Command instead of syscall.Exec so the parent process stays alive.
	// This is necessary because we have a proxy server goroutine running.
	cmd := exec.Command(pastaPath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start pasta/bwrap: %w", err)
	}

	// Pasta creates a new PID namespace for its child, so `echo $$` inside the
	// wrapper would record PID 1 (namespace-local), not a host-visible PID.
	// Instead, find pasta's direct child via procfs — that PID is host-visible
	// and lives inside pasta's network namespace, which is exactly what callers
	// need for /proc/<pid>/ns/net and liveness checks.
	namespacePID, err := waitForFirstChildPID(cmd.Process.Pid, 2*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("locate sandbox PID under pasta: %w", err)
	}

	return &SandboxProcess{
		Cmd:          cmd,
		NamespacePID: namespacePID,
	}, nil
}

// ExecWithPasta wraps bwrap execution inside pasta for network namespace isolation.
// This creates an isolated network namespace where all traffic must go through
// pasta's gateway, which we configure to route through our proxy.
//
// The portForwardArgs parameter accepts pasta port forwarding arguments (e.g., -t, -u, -T, -U).
// Pass nil if no port forwarding is needed.
//
// Unlike the regular Exec function, this uses exec.Command instead of syscall.Exec
// so that the calling process (and its proxy server goroutine) stays alive.
func ExecWithPasta(bwrapArgs []string, shellCmd []string, portForwardArgs []string) error {
	proc, err := StartWithPasta(bwrapArgs, shellCmd, portForwardArgs)
	if err != nil {
		return err
	}
	return proc.Wait()
}
