package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// envEgressLockdown is set to "1" by the krun isolator (Linux, proxy mode
	// only) to tell the shim to wait for the host to lock guest egress before
	// running the workload. docker and non-proxy runs never set it.
	envEgressLockdown = "DEVSANDBOX_EGRESS_LOCKDOWN"
	// egressSentinelName is created in the sandbox home by the host
	// (internal/isolator) once it has locked the guest's egress to the proxy
	// gateway. Must match the isolator constant.
	egressSentinelName = ".devsandbox-egress-locked"
	// egressReadyTimeout bounds the wait. The host kills the microVM on lockdown
	// failure, so this only elapses if the host died entirely - in which case
	// aborting (fail-closed) is the correct outcome.
	egressReadyTimeout = 30 * time.Second
)

// egressLockdownRequested reports whether devsandbox asked the shim to wait for
// host-applied egress lockdown. It is set exclusively for the krun microVM
// backend in proxy mode on Linux; docker and non-proxy runs never set it.
func egressLockdownRequested() bool {
	return os.Getenv(envEgressLockdown) == "1"
}

// waitForEgressReady blocks until the host signals that the guest's egress has
// been locked to the proxy gateway, then returns.
//
// The krun egress lockdown runs HOST-side: the isolator edits the VMM's
// pasta-netns routing table (the guest kernel has no routable interface under
// libkrun TSI, so an in-guest `ip route del` has nothing to act on). The guest's
// only job is to not start the workload until the host is done - so untrusted
// code never runs while direct egress is still open. Fail-closed: a timeout is
// returned so the caller aborts rather than exec with open egress.
func waitForEgressReady() error {
	return waitForEgressSentinel(filepath.Join(sandboxHome, egressSentinelName), egressReadyTimeout)
}

// waitForEgressSentinel polls for path until it is a regular file or timeout
// elapses. Split from waitForEgressReady so it is unit-testable against an
// arbitrary path.
//
// Only a regular file is accepted. The sentinel lives in the persistent,
// guest-writable sandbox home, so a previous untrusted run could pre-create a
// directory, or a symlink pointing at a regular file it controls, at this path.
// os.Lstat (never os.Stat) inspects the entry itself, not a symlink's target, so
// a planted symlink is seen as a symlink - IsRegular() is false - and is not
// followed. This gate independently refuses any non-regular entry as the
// go-signal, regardless of the host's pre-launch clear - only the host's
// writeEgressSentinel produces a true regular file, so a spoofed directory or
// symlink is ignored and the wait times out (fail-closed).
func waitForEgressSentinel(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for host egress lockdown (%s)", timeout, path)
		}
		time.Sleep(250 * time.Millisecond)
	}
}
