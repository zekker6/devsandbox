package bwrap

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

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
	pastaPath, err := embed.PastaPath()
	if err != nil {
		return fmt.Errorf("pasta not available (required for proxy mode): %w\nRun 'devsandbox doctor' for details", err)
	}

	bwrapPath, err := embed.BwrapPath()
	if err != nil {
		return fmt.Errorf("bwrap not available: %w", err)
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
	args = append(args, "sh", "-c", wrapperScript, "_") // Wrapper to delete default route
	args = append(args, bwrapPath)
	args = append(args, bwrapArgs...)
	args = append(args, "--")
	args = append(args, shellCmd...)

	// Use exec.Command instead of syscall.Exec so the parent process stays alive
	// This is necessary because we have a proxy server goroutine running
	cmd := exec.Command(pastaPath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	return cmd.Run()
}
