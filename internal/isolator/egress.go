package isolator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

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
// its shared pasta netns, applied one layer out. The route surgery is paired
// with a port-scoped firewall (buildFirewallCommands) that restricts the one
// remaining path - the gateway - to the proxy port, so the guest cannot reach
// other host-loopback services that --map-host-loopback would otherwise expose.

// buildEgressCommands returns the argv lists that lock guest egress to the proxy
// gateway: add a /32 host route to the gateway via the default device, then
// delete the default route. Ordering is significant - the gateway route MUST be
// added before the default route is deleted so the proxy stays reachable.
func buildEgressCommands(gateway, dev string) [][]string {
	return [][]string{
		{"ip", "route", "add", gateway + "/32", "dev", dev},
		{"ip", "route", "del", "default"},
	}
}

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
func applyEgressCommands(pid int, gateway, dev string, runFn func(name string, args ...string) error) error {
	return applyNetnsCommands(pid, buildEgressCommands(gateway, dev), runFn)
}

// firewallBackend identifies the host firewall tool used to port-scope guest
// access to the proxy gateway inside the VMM netns.
type firewallBackend int

const (
	firewallNone firewallBackend = iota
	firewallNft
	firewallIptables
)

// egressFirewallTable is the nft table the lockdown installs in the VMM netns.
const egressFirewallTable = "devsandbox_egress"

// detectFirewallBackend prefers nft and falls back to iptables, based on which
// binary the host provides, returning both the chosen backend and its resolved
// path so callers can reuse the path without a second lookup. lookPath is
// injected so the choice is unit-testable without the host binaries actually
// being present.
func detectFirewallBackend(lookPath func(string) (string, error)) (firewallBackend, string) {
	if path, err := lookPath("nft"); err == nil {
		return firewallNft, path
	}
	if path, err := lookPath("iptables"); err == nil {
		return firewallIptables, path
	}
	return firewallNone, ""
}

// CheckFirewallBackend reports whether the host provides a firewall backend (nft
// or iptables) that the krun proxy-mode egress lockdown needs to port-scope guest
// access to the proxy gateway. It is advisory: only krun + proxy on Linux uses it,
// and the launch already fails closed if it is missing, so doctor surfaces it as a
// warn to catch the gap before launch rather than at it. Non-proxy krun does not
// need a firewall backend.
func CheckFirewallBackend() MicroVMCheck {
	return checkFirewallBackend(exec.LookPath)
}

func checkFirewallBackend(lookPath func(string) (string, error)) MicroVMCheck {
	switch backend, path := detectFirewallBackend(lookPath); backend {
	case firewallNft, firewallIptables:
		return MicroVMCheck{Name: "firewall", OK: true, Summary: path}
	default:
		return MicroVMCheck{
			Name:    "firewall",
			OK:      false,
			Summary: "no nft or iptables (needed for krun proxy-mode egress lockdown)",
			Hint: "Install nftables or iptables; krun + proxy uses it to restrict guest egress to the\n" +
				"proxy port. Usually already present. Non-proxy krun does not need it.",
		}
	}
}

// buildFirewallCommands returns the argv lists that port-scope guest egress to
// the proxy gateway. The route surgery alone keeps a /32 to the gateway on ALL
// ports, and pasta's --map-host-loopback maps every port of that gateway to the
// host's 127.0.0.1, so without these rules the guest could reach ANY host
// loopback service through the gateway - bypassing the proxy filter. pasta has
// no port-scoped host-loopback option (--map-host-loopback takes an address
// only, confirmed against the current pasta(1) man page), so the scoping is done
// here with a fail-closed firewall in the VMM netns: allow established/related
// return traffic and new TCP to gateway:proxyPort, drop every other new
// connection to the gateway. DNS is intentionally NOT excepted - the proxy
// resolves hostnames itself and all traffic goes through HTTP(S)_PROXY, so DNS to
// the gateway has (and must have) no path out. The accept rules precede the drop.
func buildFirewallCommands(backend firewallBackend, gateway string, proxyPort int) ([][]string, error) {
	port := strconv.Itoa(proxyPort)
	switch backend {
	case firewallNft:
		return [][]string{
			{"nft", "add", "table", "ip", egressFirewallTable},
			{"nft", "add", "chain", "ip", egressFirewallTable, "output", "{ type filter hook output priority 0 ; policy accept ; }"},
			{"nft", "add", "rule", "ip", egressFirewallTable, "output", "ct", "state", "established,related", "accept"},
			{"nft", "add", "rule", "ip", egressFirewallTable, "output", "ip", "daddr", gateway, "tcp", "dport", port, "accept"},
			{"nft", "add", "rule", "ip", egressFirewallTable, "output", "ip", "daddr", gateway, "drop"},
		}, nil
	case firewallIptables:
		return [][]string{
			{"iptables", "-A", "OUTPUT", "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
			{"iptables", "-A", "OUTPUT", "-d", gateway, "-p", "tcp", "--dport", port, "-j", "ACCEPT"},
			{"iptables", "-A", "OUTPUT", "-d", gateway, "-j", "DROP"},
		}, nil
	default:
		return nil, fmt.Errorf("no firewall backend (nft or iptables) available to port-scope guest egress to the proxy port; install nftables or iptables")
	}
}

// lockdownGuestEgress resolves the default-route device inside the VMM's network
// namespace and applies, via nsenter, both the proxy-only route surgery and a
// port-scoped firewall restricting the gateway to the proxy port. It is
// fail-closed: every error is returned so the caller aborts the launch rather
// than run with open (or port-wide host-loopback) egress.
func lockdownGuestEgress(pid int, gateway string, proxyPort int) error {
	return lockdownGuestEgressWith(pid, gateway, proxyPort, exec.LookPath, defaultRouteDeviceInNetns, runEgressCommand)
}

// lockdownGuestEgressWith is lockdownGuestEgress with the host-touching seams
// injected - the firewall-binary lookup, the netns device resolver, and the
// command runner - so the security-relevant sequencing is unit-testable without a
// real nsenter or a running microVM. Two orderings matter and are asserted by the
// tests through these seams: the firewall backend is resolved BEFORE any netns
// command runs (a host lacking both nft and iptables fails closed without
// half-applying the lockdown), and the route surgery runs BEFORE the port-scoped
// firewall.
func lockdownGuestEgressWith(
	pid int,
	gateway string,
	proxyPort int,
	lookPath func(string) (string, error),
	resolveDev func(pid int) (string, error),
	runFn func(name string, args ...string) error,
) error {
	if gateway == "" {
		return fmt.Errorf("egress lockdown requested but proxy gateway is empty")
	}
	if proxyPort <= 0 {
		return fmt.Errorf("egress lockdown requested but proxy port is invalid: %d", proxyPort)
	}
	// Resolve the firewall backend BEFORE mutating the netns so a host with
	// neither nft nor iptables fails closed without half-applying the lockdown.
	backend, _ := detectFirewallBackend(lookPath)
	firewallCmds, err := buildFirewallCommands(backend, gateway, proxyPort)
	if err != nil {
		return err
	}
	dev, err := resolveDev(pid)
	if err != nil {
		return err
	}
	// Route surgery first (keep a /32 to the gateway, delete the default route),
	// then the port-scoped firewall (restrict that gateway to the proxy port).
	if err := applyEgressCommands(pid, gateway, dev, runFn); err != nil {
		return err
	}
	return applyNetnsCommands(pid, firewallCmds, runFn)
}

// defaultRouteDeviceInNetns resolves the default-route device inside the target
// PID's network namespace.
func defaultRouteDeviceInNetns(pid int) (string, error) {
	out, err := exec.Command("nsenter", nsenterArgv(pid, "ip", "-o", "route", "show", "default")...).Output()
	if err != nil {
		return "", fmt.Errorf("query default route in netns of pid %d: %w", pid, err)
	}
	return parseDefaultRouteDevice(string(out))
}

// runEgressCommand executes a lockdown command, surfacing its stderr for
// diagnostics on failure.
func runEgressCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stderr = os.Stderr
	return cmd.Run()
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
