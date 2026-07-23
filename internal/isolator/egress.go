package isolator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"devsandbox/internal/egress"
)

// egressCommandTimeout bounds each nsenter lockdown command. The lockdown runs in
// a goroutine the run path joins with wg.Wait(), so a wedged nsenter (a stuck
// netns operation) would otherwise block Run from ever returning. A handful of
// commands run, each bounded, so the whole lockdown is bounded too.
const egressCommandTimeout = 15 * time.Second

// egressWaitDelay bounds the wait for a captured output pipe after the deadline
// has already killed the command. Killing nsenter does not close a pipe the `ip`
// it spawned still holds, so without this the wait would outlast the timeout
// indefinitely.
const egressWaitDelay = 2 * time.Second

// egressSentinelName is the file the host writes into the sandbox home once it
// has locked the krun guest's egress to the proxy gateway. The in-guest shim
// waits for this file (shared into the guest over virtio-fs) before exec'ing the
// workload, so untrusted code never runs while direct egress is still open. Kept
// in sync with the shim constant in cmd/devsandbox-shim/egress.go.
const egressSentinelName = ".devsandbox-egress-locked"

// Why the lockdown runs host-side for krun (and not in the guest like bwrap):
// under libkrun the guest uses TSI (transparent socket interception) - it has no
// routable network interface, so its connect() calls are executed by the VMM
// process in the VMM's network namespace (the pasta netns). An in-guest
// `ip route del default` therefore has nothing to act on. The VMM is a normal
// process in that netns, so its TSI sockets obey the netns routing table:
// deleting the default route there (while keeping a /32 to the gateway) blocks
// direct egress yet leaves the proxy reachable - the same effect bwrap gets in
// its shared pasta netns, applied one layer out. Route surgery alone is not
// enough (the connected LAN subnet route survives it, and --map-host-loopback
// exposes every port of the gateway), so it is paired with a DENY-BY-DEFAULT
// firewall (egress.FirewallCommands) that drops all egress except loopback,
// established/related return traffic, and TCP to gateway:proxyPort. The
// guest is given IPv4 only (the pasta invocation passes -4), so there is no IPv6
// path for the IPv4 table to miss.
//
// The rule content itself lives in internal/egress, shared with the bwrap
// backend, which renders the same lists into its pasta wrapper script instead of
// applying them through nsenter. Everything below is krun's application
// mechanism: the nsenter wrapping, the sequencing, and the sentinel handshake.

// parseDefaultRouteDevice extracts the device backing the default route from the
// output of `ip -o route show default` (e.g. "default via 10.0.2.2 dev enp5s0 ...").
func parseDefaultRouteDevice(output string) (string, error) {
	fields := strings.Fields(output)
	for i, f := range fields {
		if f == "dev" && i+1 < len(fields) {
			return fields[i+1], nil
		}
	}
	return "", fmt.Errorf("no default route device in %q", strings.TrimSpace(output))
}

// nsenterArgv prefixes command with the nsenter flags that enter the target
// PID's user + network namespaces as userns-root. Unlike the port-forward dialer
// it deliberately omits --preserve-credentials: route surgery needs
// CAP_NET_ADMIN, which only uid 0 in the entered (rootless) userns holds; keeping
// the caller's uid leaves it unprivileged and the route ops fail with EPERM.
func nsenterArgv(pid int, command ...string) []string {
	return append([]string{"--target", strconv.Itoa(pid), "--user", "--net", "--"}, command...)
}

// applyNetnsCommands runs each argv (wrapped in nsenter for the target netns)
// with runFn, returning the first error so the caller fails closed - no further
// command runs after a failure, so the netns is never left half-configured (the
// default route half-removed, or the firewall partially applied). Taking runFn
// keeps this security boundary unit-testable without a real nsenter or a running
// microVM.
func applyNetnsCommands(pid int, cmds [][]string, runFn func(name string, args ...string) error) error {
	for _, argv := range cmds {
		if err := runFn("nsenter", nsenterArgv(pid, argv...)...); err != nil {
			return fmt.Errorf("egress lockdown command %v: %w", argv, err)
		}
	}
	return nil
}

// applyEgressCommands applies the proxy-only route surgery in the target netns.
func applyEgressCommands(pid int, tools egress.Tools, gateway, dev string, runFn func(name string, args ...string) error) error {
	return applyNetnsCommands(pid, egress.RouteCommands(tools, gateway, dev), runFn)
}

// The doctor row for the firewall backend used to live here as a krun-scoped
// MicroVMCheck. It is now egress.Check: bwrap proxy mode hard-aborts without a
// working backend too, and a row named "krun: firewall" is one a bwrap user
// reasonably ignores. The check also no longer stops at resolving a binary - it
// applies the rule set (egress.Probe), which is what catches a host with
// nf_tables loaded but nf_conntrack absent.

// lockdownGuestEgress resolves the default-route device inside the VMM's network
// namespace and applies, via nsenter, both the proxy-only route surgery and a
// port-scoped firewall restricting the gateway to the proxy port. It is
// fail-closed: every error is returned so the caller aborts the launch rather
// than run with open (or port-wide host-loopback) egress.
func lockdownGuestEgress(pid int, gateway string, proxyPort int) error {
	return lockdownGuestEgressWith(pid, gateway, proxyPort, exec.LookPath, defaultRouteDeviceInNetns, runEgressCommand)
}

// lockdownGuestEgressWith is lockdownGuestEgress with the host-touching seams
// injected - the binary lookup, the netns device resolver, and the command
// runner - so the security-relevant sequencing is unit-testable without a real
// nsenter or a running microVM. Two orderings matter and are asserted by the
// tests through these seams: the binaries (and with them the firewall backend)
// are resolved BEFORE any netns command runs (a host lacking `ip`, or lacking
// both nft and iptables, fails closed without half-applying the lockdown), and
// the route surgery runs BEFORE the port-scoped firewall.
func lockdownGuestEgressWith(
	pid int,
	gateway string,
	proxyPort int,
	lookPath func(string) (string, error),
	resolveDev func(pid int, ipPath string) (string, error),
	runFn func(name string, args ...string) error,
) error {
	if gateway == "" {
		return fmt.Errorf("egress lockdown requested but proxy gateway is empty")
	}
	if proxyPort <= 0 {
		return fmt.Errorf("egress lockdown requested but proxy port is invalid: %d", proxyPort)
	}
	// Resolve the binaries (and with them the firewall backend) BEFORE mutating
	// the netns so a host with no `ip`, or with neither nft nor iptables, fails
	// closed without half-applying the lockdown. Resolution yields absolute
	// paths: nsenter runs the command with a PATH this process does not control,
	// and /usr/sbin is commonly absent from it.
	tools, err := egress.ResolveTools(lookPath)
	if err != nil {
		return err
	}
	firewallCmds, err := egress.FirewallCommands(tools, egress.Lockdown{
		Enabled:   true,
		Gateway:   gateway,
		ProxyPort: proxyPort,
	})
	if err != nil {
		return err
	}
	dev, err := resolveDev(pid, tools.IP)
	if err != nil {
		return err
	}
	// Route surgery first (keep a /32 to the gateway, delete the default route),
	// then the port-scoped firewall (restrict that gateway to the proxy port).
	if err := applyEgressCommands(pid, tools, gateway, dev, runFn); err != nil {
		return err
	}
	return applyNetnsCommands(pid, firewallCmds, runFn)
}

// defaultRouteDeviceInNetns resolves the default-route device inside the target
// PID's network namespace. ipPath is the absolute path resolved on the host:
// nsenter runs the command with a PATH this process does not control, and on
// hosts where iproute2 lives only in /usr/sbin a bare `ip` would not resolve.
func defaultRouteDeviceInNetns(pid int, ipPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), egressCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nsenter", nsenterArgv(pid, ipPath, "-o", "route", "show", "default")...)
	// WaitDelay is what makes egressCommandTimeout a real bound here. The output
	// is captured, so os/exec pipes it and Wait blocks until every writer closes
	// the pipe - while the context cancellation kills only nsenter, not the `ip`
	// it spawned, which keeps the inherited fd open if it wedges on netlink.
	// Without this the lockdown goroutine would block past the deadline and with
	// it the run path's wg.Wait(). runEgressCommand needs no counterpart: it
	// assigns os.Stderr, a real *os.File, which os/exec hands to the child
	// directly rather than pumping through a pipe.
	cmd.WaitDelay = egressWaitDelay
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("query default route in netns of pid %d timed out after %s", pid, egressCommandTimeout)
		}
		return "", fmt.Errorf("query default route in netns of pid %d: %w", pid, err)
	}
	return parseDefaultRouteDevice(string(out))
}

// runEgressCommand executes a lockdown command, surfacing its stderr for
// diagnostics on failure. It is time-bounded so a wedged nsenter cannot hang the
// launch (the lockdown goroutine is joined by the run path).
func runEgressCommand(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), egressCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("%s %v timed out after %s", name, args, egressCommandTimeout)
		}
		return err
	}
	return nil
}

// writeEgressSentinel signals the guest shim that egress is locked by creating
// the sentinel file in the sandbox home (shared into the guest via virtio-fs).
//
// It creates the file with O_CREATE|O_EXCL so it never writes THROUGH a symlink:
// under O_CREATE|O_EXCL the kernel refuses to follow a final-component symlink and
// fails with EEXIST. The sandbox home is persistent and guest-writable, so an
// entry could exist at this path; removeEgressSentinel cleared it just before boot,
// so O_EXCL normally creates a fresh file and, if anything raced back into place,
// fails closed instead of following it.
func writeEgressSentinel(sandboxHome string) error {
	path := filepath.Join(sandboxHome, egressSentinelName)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create egress sentinel %s: %w", path, err)
	}
	return f.Close()
}

// removeEgressSentinel clears any stale sentinel before a launch so the guest
// waits for THIS run's lockdown rather than honoring a file left by a previous
// run of the same project.
//
// It is fail-closed: the sentinel path lives in the persistent, guest-writable
// sandbox home, so an untrusted run could have left a directory (or a populated
// subtree), or a symlink, there. os.RemoveAll clears such an entry, and the
// follow-up os.Lstat verifies the path is actually gone. Lstat (never Stat) is
// used so a leftover DANGLING symlink still counts as "present" - Stat would
// follow it, get ENOENT, and wrongly report the path clear. Any residual entry is
// returned as an error so the caller aborts the launch rather than boot the guest
// against an unclean sentinel path that could spoof the gate.
func removeEgressSentinel(sandboxHome string) error {
	path := filepath.Join(sandboxHome, egressSentinelName)
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("clear stale egress sentinel %s: %w", path, err)
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("stale egress sentinel %s still present after removal", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("verify egress sentinel %s removed: %w", path, err)
	}
	return nil
}

// prepareEgressLockdown runs the pre-boot half of the egress lockdown: it reports
// whether lockdown applies (krun microVM on Linux) and clears any stale sentinel
// so the guest waits for THIS run's lockdown rather than a file a previous run of
// the same project left behind. It is fail-closed - a non-nil error means the
// sentinel path could not be verified clean, and the caller MUST abort the launch
// BEFORE starting the guest, or untrusted code could boot against a spoofable
// sentinel while direct egress is still open. Split out from runMicroVMSession so
// that abort-before-boot contract is unit-testable without starting a microVM.
func prepareEgressLockdown(microVM bool, goos, sandboxHome string) (bool, error) {
	if !microVM || goos != "linux" {
		return false, nil
	}
	if err := removeEgressSentinel(sandboxHome); err != nil {
		return true, fmt.Errorf("krun egress lockdown failed (fail-closed): %w", err)
	}
	return true, nil
}
