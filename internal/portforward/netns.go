package portforward

import (
	"fmt"
	"os"
	"syscall"
)

// SharesHostNetNS reports whether the given /proc/<pid>/ns/net path refers to
// the same kernel network namespace as the current process.
//
// Userland port forwarding is only meaningful across distinct network
// namespaces. When the sandbox shares the host netns (e.g. bwrap without
// pasta), any sandbox listener is already directly reachable on the host's
// 127.0.0.1, and a forwarder trying to bind the same port on the "host" side
// hits EADDRINUSE against the sandbox tool itself — they are the same socket
// in the same kernel namespace.
//
// Returns an error only if the namespace cannot be inspected at all (e.g. the
// path does not exist, or /proc is not mounted). Callers can treat that as
// "isolated by default" since an uninspectable state is not the shared-netns
// bug this guard is protecting against.
func SharesHostNetNS(nsPath string) (bool, error) {
	sandboxIno, sandboxDev, err := netNSIdent(nsPath)
	if err != nil {
		return false, fmt.Errorf("inspect sandbox netns: %w", err)
	}
	hostIno, hostDev, err := netNSIdent("/proc/self/ns/net")
	if err != nil {
		return false, fmt.Errorf("inspect host netns: %w", err)
	}
	return sandboxIno == hostIno && sandboxDev == hostDev, nil
}

func netNSIdent(path string) (ino uint64, dev uint64, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("%s: stat returned unexpected type %T", path, info.Sys())
	}
	return st.Ino, st.Dev, nil
}
