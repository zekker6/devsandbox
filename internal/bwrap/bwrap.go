package bwrap

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func CheckInstalled() error {
	_, err := exec.LookPath("bwrap")
	if err != nil {
		return errors.New("bubblewrap (bwrap) is not installed\nRun 'devsandbox doctor' for installation instructions")
	}
	return nil
}

// pastaSupportsMapHostLoopback checks if pasta supports --map-host-loopback option.
// This option was added in newer versions of passt/pasta.
func pastaSupportsMapHostLoopback() bool {
	cmd := exec.Command("pasta", "--help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(output), "--map-host-loopback")
}

func Exec(bwrapArgs []string, shellCmd []string) error {
	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		return err
	}

	args := make([]string, 0, len(bwrapArgs)+len(shellCmd)+2)
	args = append(args, "bwrap")
	args = append(args, bwrapArgs...)
	args = append(args, "--")
	args = append(args, shellCmd...)

	return syscall.Exec(bwrapPath, args, os.Environ())
}

// ExecWithPasta wraps bwrap execution inside pasta for network namespace isolation.
// This creates an isolated network namespace where all traffic must go through
// pasta's gateway, which we configure to route through our proxy.
//
// Unlike the regular Exec function, this uses exec.Command instead of syscall.Exec
// so that the calling process (and its proxy server goroutine) stays alive.
func ExecWithPasta(bwrapArgs []string, shellCmd []string) error {
	pastaPath, err := exec.LookPath("pasta")
	if err != nil {
		return errors.New("pasta is not installed (required for proxy mode)\nRun 'devsandbox doctor' for installation instructions")
	}

	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		return err
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
	// 1. Add a host route to gateway (10.0.2.2) via the tap device
	// 2. Delete the default route to block direct internet access
	// This forces all traffic through our proxy - direct connections to external IPs will fail.
	const wrapperScript = `
		dev=$(ip -o route show default | awk '{print $5}')
		ip route add 10.0.2.2/32 dev "$dev" 2>/dev/null
		ip route del default 2>/dev/null
		exec "$@"
	`

	args := make([]string, 0, len(bwrapArgs)+len(shellCmd)+16)
	args = append(args, "--config-net") // Configure network interface

	// Use --map-host-loopback if available (newer pasta versions)
	// This maps 10.0.2.2 to host's 127.0.0.1 for proxy access
	if pastaSupportsMapHostLoopback() {
		args = append(args, "--map-host-loopback", "10.0.2.2")
	}

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
