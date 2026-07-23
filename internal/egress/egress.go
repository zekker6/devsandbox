// Package egress builds the command lists that lock a sandbox network namespace
// down to the proxy gateway. It is deliberately neutral: internal/isolator
// imports internal/bwrap, so rules shared by both backends cannot live in
// either. Keeping one rule set here means a divergence between krun's and
// bwrap's security posture requires deliberately editing shared code rather
// than happening by drift.
//
// The rule builders are pure argv construction, so the security-relevant
// content and ordering are unit-testable without a real namespace. Probe and
// Check are the exception: they deliberately apply the rendered rule set in a
// throwaway user + network namespace, because that is the only check that
// catches a host with the binaries present and the netfilter modules missing.
package egress

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Backend identifies the host firewall tool used to port-scope sandbox access
// to the proxy gateway inside the network namespace.
type Backend int

const (
	BackendNone Backend = iota
	BackendNft
	BackendIptables
)

// FirewallTable is the nft table the lockdown installs in the namespace.
const FirewallTable = "devsandbox_egress"

// Forward describes an outbound port forward configured on the pasta
// invocation (`-T`/`-U`). The lockdown must not break forwards the user asked
// for, so the rule builders are given them explicitly rather than guessing.
type Forward struct {
	Port int
	UDP  bool
}

// proto names the forward's protocol as both nft and iptables spell it.
func (f Forward) proto() string {
	if f.UDP {
		return "udp"
	}
	return "tcp"
}

// validate refuses a port the firewall binary would reject mid-script. Config
// validation already bounds the configured rules, but the rule builders are
// shared and a forward that renders an unusable rule would abort the launch from
// inside the namespace, where the diagnostic is far worse.
func (f Forward) validate() error {
	if f.Port <= 0 || f.Port > 65535 {
		return fmt.Errorf("egress: invalid forwarded port %d in the egress lockdown, must be 1-65535", f.Port)
	}
	return nil
}

// Lockdown is the full description of a proxy-only egress restriction: which
// gateway address and port stay reachable, and which forwards must keep
// working. Enabled is false for non-proxy launches, where no lockdown is
// rendered at all.
//
// ReadyFile is used only by Script (the bwrap wrapper prologue), which creates
// that path immediately before it execs the workload - see markerComment. krun
// applies the rule lists directly and observes each one's status, so it needs no
// such signal and leaves the field empty.
type Lockdown struct {
	Enabled   bool
	Gateway   string
	ProxyPort int
	Forwards  []Forward
	ReadyFile string
}

// ErrNoFirewallBackend is returned when the host provides neither nft nor
// iptables. The lockdown cannot be applied without one, so both backends turn
// this into an aborted launch rather than running with the gateway reachable on
// every port.
var ErrNoFirewallBackend = errors.New("no firewall backend (nft or iptables) available to lock guest egress to the proxy port; install nftables or iptables")

// ErrNoIPBinary is returned when `ip` cannot be resolved to an absolute path.
// Without it there is no way to perform the route surgery, so the launch aborts.
var ErrNoIPBinary = errors.New("no `ip` binary available to apply the proxy-only route surgery; install iproute2")

// sbinDirs are probed when a PATH lookup fails. On many distributions iproute2
// and nftables live only in /usr/sbin (or /sbin), which is not on an ordinary
// user's PATH - the reason the bwrap wrapper script's bare `ip` calls have
// silently never applied on those hosts.
var sbinDirs = []string{"/usr/sbin", "/sbin"}

// Tools holds the host binaries the lockdown invokes, resolved to ABSOLUTE
// paths. Nothing emitted by the rule builders may be a bare name: the bwrap
// wrapper script and krun's nsenter argv both run with a PATH that is not the
// one this process was launched with, so a bare `ip` or `nft` is a silent
// non-application of a security control.
type Tools struct {
	IP       string
	Firewall string
	Backend  Backend
}

// ResolveTools resolves `ip` and the firewall binary (nft preferred, iptables
// fallback) to absolute paths, failing closed when either is missing. lookPath
// is injected so resolution is unit-testable without the host binaries being
// present; it is also the seam the /usr/sbin fallback goes through, since
// exec.LookPath on a path containing a slash checks that file directly rather
// than consulting PATH.
func ResolveTools(lookPath func(string) (string, error)) (Tools, error) {
	ip, ok := resolveBinary(lookPath, "ip")
	if !ok {
		return Tools{}, ErrNoIPBinary
	}
	backend, firewall := DetectBackend(lookPath)
	if backend == BackendNone {
		return Tools{}, ErrNoFirewallBackend
	}
	return Tools{IP: ip, Firewall: firewall, Backend: backend}, nil
}

// resolveBinary resolves name to an absolute path, falling back to an explicit
// probe of the sbin directories when the PATH lookup fails. A lookup that
// succeeds with a relative path (PATH containing "." or a relative entry) is
// rejected rather than used: the rendered command runs from a working directory
// this code does not control.
func resolveBinary(lookPath func(string) (string, error), name string) (string, bool) {
	if path, err := lookPath(name); err == nil && filepath.IsAbs(path) {
		return path, true
	}
	for _, dir := range sbinDirs {
		if path, err := lookPath(filepath.Join(dir, name)); err == nil && filepath.IsAbs(path) {
			return path, true
		}
	}
	return "", false
}

// DetectBackend prefers nft and falls back to iptables, based on which binary
// the host provides, returning both the chosen backend and its resolved
// absolute path so callers can reuse the path without a second lookup. lookPath
// is injected so the choice is unit-testable without the host binaries actually
// being present.
func DetectBackend(lookPath func(string) (string, error)) (Backend, string) {
	if path, ok := resolveBinary(lookPath, "nft"); ok {
		return BackendNft, path
	}
	if path, ok := resolveBinary(lookPath, "iptables"); ok {
		return BackendIptables, path
	}
	return BackendNone, ""
}

// RouteCommands returns the argv lists that lock egress to the proxy gateway:
// add a /32 host route to the gateway via the default device, then delete the
// default route. Ordering is significant - the gateway route MUST be added
// before the default route is deleted so the proxy stays reachable.
func RouteCommands(t Tools, gateway, dev string) [][]string {
	return [][]string{
		{t.IP, "route", "add", gateway + "/32", "dev", dev},
		{t.IP, "route", "del", "default"},
	}
}

// FirewallCommands returns the argv lists that lock egress to the proxy gateway
// with a DENY-BY-DEFAULT firewall in the network namespace. The route surgery
// alone only removes the default route: the connected on-link subnet route
// survives, so LAN hosts (the router UI, a NAS, the LAN DNS resolver - direct
// DNS exfiltration - and, on a cloud host, the 169.254.169.254 metadata service
// and IAM credentials) stay directly reachable, and pasta's --map-host-loopback
// maps every port of the gateway to the host's 127.0.0.1. Enumerating what to
// drop misses all of that, so instead the chain DROPS by default and allows
// only: established/related return traffic, loopback, and TCP to
// gateway:proxyPort. Everything else - every other host, every other port of
// the gateway, DNS - has no path out of the namespace. DNS is deliberately not
// excepted: the proxy resolves hostnames itself and all traffic goes through
// HTTP(S)_PROXY.
//
// What this table does NOT do is decide destinations. It reduces the sandbox to
// one way out; where that way leads is internal/proxy's filter, which allows
// everything until default_action is set. So a metadata or LAN address is
// refused as a direct socket and still served when it is asked for through the
// proxy - the difference being that the proxy sees, logs and can refuse it.
// Read "closed" here as "has no unmediated path", never as "unreachable".
// pasta has no port-scoped host-loopback option (--map-host-loopback takes an
// address only, confirmed against the current pasta(1) man page), which is why
// the scoping is done here. This is IPv4 only, matching the family the sandbox
// is given: the pasta invocation passes -4, so there is no IPv6 route or IPv6
// host-loopback map for this table to need to cover.
//
// Lockdown.Forwards adds one accept per outbound port forward the user
// configured, and nothing wider. pasta's -T/-U do bind those ports inside the
// namespace, where the loopback accept would already cover them, but that is not
// the path devsandbox documents or tests: `--map-host-loopback` is what makes
// the host reachable at the gateway, and a forward is used by connecting to
// gateway:port. That is the same address-scoped mapping this table exists to
// narrow, so without an explicit per-port accept a forward the user configured
// would silently stop working. Each accept permits exactly one port of the
// gateway - never the gateway as a whole.
func FirewallCommands(t Tools, l Lockdown) ([][]string, error) {
	port := strconv.Itoa(l.ProxyPort)
	fw := t.Firewall
	switch t.Backend {
	case BackendNft:
		if fw == "" {
			return nil, fmt.Errorf("firewall backend nft selected without a resolved binary path: %w", ErrNoFirewallBackend)
		}
		cmds := [][]string{
			{fw, "add", "table", "ip", FirewallTable},
			{fw, "add", "chain", "ip", FirewallTable, "output", "{ type filter hook output priority 0 ; policy drop ; }"},
			{fw, "add", "rule", "ip", FirewallTable, "output", "ct", "state", "established,related", "accept"},
			{fw, "add", "rule", "ip", FirewallTable, "output", "oif", "lo", "accept"},
			{fw, "add", "rule", "ip", FirewallTable, "output", "ip", "daddr", l.Gateway, "tcp", "dport", port, "accept"},
		}
		for _, f := range l.Forwards {
			if err := f.validate(); err != nil {
				return nil, err
			}
			cmds = append(cmds, []string{fw, "add", "rule", "ip", FirewallTable, "output", "ip", "daddr", l.Gateway, f.proto(), "dport", strconv.Itoa(f.Port), "accept"})
		}
		return cmds, nil
	case BackendIptables:
		if fw == "" {
			return nil, fmt.Errorf("firewall backend iptables selected without a resolved binary path: %w", ErrNoFirewallBackend)
		}
		// Append the accepts before flipping the policy to DROP so no in-flight
		// return traffic is dropped in the window between the two.
		cmds := [][]string{
			{fw, "-A", "OUTPUT", "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
			{fw, "-A", "OUTPUT", "-o", "lo", "-j", "ACCEPT"},
			{fw, "-A", "OUTPUT", "-d", l.Gateway, "-p", "tcp", "--dport", port, "-j", "ACCEPT"},
		}
		for _, f := range l.Forwards {
			if err := f.validate(); err != nil {
				return nil, err
			}
			cmds = append(cmds, []string{fw, "-A", "OUTPUT", "-d", l.Gateway, "-p", f.proto(), "--dport", strconv.Itoa(f.Port), "-j", "ACCEPT"})
		}
		return append(cmds, []string{fw, "-P", "OUTPUT", "DROP"}), nil
	default:
		return nil, ErrNoFirewallBackend
	}
}

// pastaNsForwardFlags are pasta's namespace->init port forwarding options, in
// both spellings, grouped per protocol. Only the short forms are emitted by
// sandbox.BuildPastaPortArgs today; the long forms are recognised so a future
// change there cannot silently revoke a forward the user configured.
var pastaNsForwardFlags = [][]string{
	{"-T", "--tcp-ns"},
	{"-U", "--udp-ns"},
}

// NoAutoForwardArgs returns the pasta options that switch OFF its automatic
// namespace->init port forwarding, for each protocol `configured` does not
// already pin explicitly.
//
// pasta defaults -T and -U to `auto`, which binds every port listening in the
// INIT namespace inside the sandbox namespace on loopback. Reaching one of those
// leaves through `lo`, which FirewallCommands must accept, so with the default
// left in place the host's own 127.0.0.1 services - a local database, another
// app's dev API, a model server - stay directly reachable from inside the
// sandbox at 127.0.0.1:<port>, mediated by nothing. That is precisely the
// exposure the single gateway accept exists to close, arriving on the one
// interface the rules cannot filter, so the lockdown removes the forwarding
// rather than trying to police it.
//
// A protocol the user configured an outbound forward for is left alone: an
// explicit -T/-U already overrides pasta's default, and appending `none` after
// it would revoke the forward. Inbound forwarding (-t/-u) is deliberately
// untouched - it produces connections ARRIVING in the namespace, which the
// OUTPUT hook does not filter and this lockdown does not scope.
func NoAutoForwardArgs(configured []string) []string {
	var args []string
	for _, spellings := range pastaNsForwardFlags {
		pinned := slices.ContainsFunc(configured, func(arg string) bool {
			return slices.ContainsFunc(spellings, func(f string) bool { return pinsForward(arg, f) })
		})
		if !pinned {
			args = append(args, spellings[0], "none")
		}
	}
	return args
}

// pinsForward reports whether arg already pins the forwarding option spelled f.
// getopt_long takes an option's argument attached as well as separated, so
// `--tcp-ns=5000` and `-T5000` override pasta's `auto` default exactly as
// `-T 5000` does. Matching only the bare token would let a future change in
// sandbox.BuildPastaPortArgs revoke a forward the user configured - a `none`
// appended after an unrecognised spelling wins, because pasta takes the last
// occurrence - which is the silent-limit-loss this recognition exists to
// prevent. No other pasta option starts with -T or -U, so the short-form prefix
// test cannot capture an unrelated flag.
func pinsForward(arg, f string) bool {
	if arg == f {
		return true
	}
	if strings.HasPrefix(f, "--") {
		return strings.HasPrefix(arg, f+"=")
	}
	return strings.HasPrefix(arg, f)
}

// LockdownExitCode is the status the rendered prologue exits with when any
// lockdown step fails. 78 is EX_CONFIG from sysexits(3), which is what every
// failure here is: a host that cannot apply the lockdown (no default route
// device in the namespace, an unusable firewall binary, a missing kernel
// module). A value in the sysexits range is unlikely to collide with a
// workload's own status, which matters because the script's caller maps this
// code back to a named lockdown error - an abort must never be reported as if
// the sandboxed program had exited.
const LockdownExitCode = 78

// LockdownApplied reports whether the prologue rendered by Script got as far as
// exec'ing the workload, by testing for the marker file it creates there.
//
// The exit code alone cannot answer this. 78 is rare for a workload to choose
// but not reserved, and the prologue clears its EXIT trap before `exec "$@"` so
// the workload's own status passes through untouched - meaning a workload that
// exits 78 is, on status alone, indistinguishable from an abort. Reporting an
// applied lockdown as an unapplied one is as wrong as the reverse: it turns a
// normal command exit into a security error and replaces the command's status
// with 1. The marker settles it, because it is written after every rule
// succeeded and before anything untrusted runs.
//
// An empty path (no marker was requested) reads as "not applied": the caller
// that renders the script always supplies one, so an empty path means the signal
// is missing rather than negative, and the fail-closed reading of a 78 is that
// the lockdown aborted.
func LockdownApplied(readyFile string) bool {
	if readyFile == "" {
		return false
	}
	_, err := os.Stat(readyFile)
	return err == nil
}

// scriptErrPrefix is emitted on every failure path in the rendered script so an
// aborted launch is greppable from the terminal.
const scriptErrPrefix = "devsandbox: egress lockdown:"

// scriptDevPlaceholder stands in for the default-route device while the shared
// route commands are built. The device is only known inside the namespace at
// run time, so the renderer substitutes the shell variable holding it instead of
// quoting this literal.
const scriptDevPlaceholder = "@DEVSANDBOX_DEFAULT_ROUTE_DEV@"

// Bounded retry for the default-route device lookup: pasta configures the
// namespace and execs its child, and the ordering between the two is not
// guaranteed. Failing on the first empty read would turn that race into an
// aborted launch, so the lookup retries for deviceRetries*deviceRetryDelay
// before giving up.
//
// POSIX only requires `sleep` to accept an integer operand; the fractional form
// is a GNU coreutils / busybox extension. A `sleep 0.1` that is rejected exits
// non-zero, which `set -e` turns into an aborted launch on a host that can apply
// the lockdown perfectly well - and the preflight cannot catch it, because
// probeScript renders only the firewall half. So the prologue tests the
// fractional form once and falls back to the same wall-clock budget at integer
// granularity.
const (
	deviceRetries    = 50
	deviceRetryDelay = "0.1"
	// deviceSleepProbe is the fractional argument the prologue tests with. It is
	// short enough that the one-time cost where it works is noise.
	deviceSleepProbe    = "0.01"
	deviceRetriesInt    = "5"
	deviceRetryDelayInt = "1"
)

// sleepProbeLine selects the retry granularity. It is the ONE place the prologue
// discards a diagnostic, and deliberately so: the probe's exit status is the
// signal, and the "invalid time interval" a POSIX-strict `sleep` writes would be
// noise on a launch that then proceeds normally. A `sleep` missing outright
// fails this line too and the integer fallback it selects fails inside the loop,
// so the fail-closed path is unchanged - only the granularity adapts.
const sleepProbeLine = "sleep " + deviceSleepProbe + " 2>/dev/null || { naptime=" +
	deviceRetryDelayInt + "; naps=" + deviceRetriesInt + "; }\n"

// awkFirstDefaultRouteDev prints the token following the first "dev" keyword in
// `ip -o route show default` output and exits immediately. The exit matters: a
// host with multiple default routes emits several lines, and without it the
// device name would be multi-line. This matches parseDefaultRouteDevice, which
// also takes the first.
const awkFirstDefaultRouteDev = `{for(n=1;n<NF;n++) if($n=="dev"){print $(n+1); exit}}`

// shellQuote renders s as a single shell word: wrapped in single quotes, with
// any embedded single quote closed, backslash-escaped, and reopened. It ALWAYS
// quotes, including input that looks safe.
//
// internal/sandbox/shell.go:35 has a shellQuote that returns its input unquoted
// when nothing in it needs escaping. That fast path is fine for generating
// readable shell-config snippets and wrong here: this rendering is a security
// boundary, so it must have exactly one code path whose correctness does not
// depend on a character classifier staying complete. Do not "simplify" this by
// reusing the fast-path version.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// quoteArgv renders an argv list as one shell command line, quoting every
// element. The device placeholder is substituted with the shell variable
// holding the device discovered at run time rather than being quoted as a
// literal.
func quoteArgv(argv []string) string {
	return joinQuotedArgv(argv, " ")
}

// joinQuotedArgv quotes every element of argv (substituting the device
// placeholder for the shell variable) and joins them with sep. sep is a
// separator in the RENDERED text, so passing a quoted space yields one
// concatenated shell word instead of several - which is what the diagnostic
// needs, since `fail` reads a single argument.
func joinQuotedArgv(argv []string, sep string) string {
	words := make([]string, 0, len(argv))
	for _, a := range argv {
		if a == scriptDevPlaceholder {
			words = append(words, `"$dev"`)
			continue
		}
		words = append(words, shellQuote(a))
	}
	return strings.Join(words, sep)
}

// renderCommand renders an argv list as one shell command, quoting every
// element, and aborts the script with the lockdown exit code if it fails.
//
// The diagnostic is built from the same quoted forms as the command rather than
// from the raw argv, so the one step that depends on runtime discovery names the
// device it actually used instead of the internal placeholder. That message is
// what the docs and ErrEgressLockdown point the user at.
func renderCommand(argv []string) string {
	return quoteArgv(argv) + " || fail " + shellQuote("failed: ") + joinQuotedArgv(argv, shellQuote(" "))
}

// Script renders the lockdown as a POSIX shell prologue for the bwrap pasta
// wrapper, ending in `exec "$@"`. The wrapper runs as root in pasta's user
// namespace, holding CAP_NET_ADMIN over the netns, BEFORE it execs the
// workload - so applying the rules here means there is no window in which
// untrusted code runs with egress open.
//
// The prologue is fail-closed by construction: `set -e` plus an explicit
// `|| fail` on every mutation, so any failing step exits LockdownExitCode with a
// diagnostic and never reaches the exec. An EXIT trap covers what `|| fail`
// cannot - a `set -e` abort inside the device lookup, which would otherwise
// leave with the failing command's own status and be read as a workload exit -
// and is cleared immediately before the exec. The three orderings the rule
// builders establish are preserved: the gateway route is added before the
// default route is deleted, and the firewall rules follow both.
//
// The last thing before the exec is l.ReadyFile, the marker LockdownApplied
// reads to tell an abort from a workload that exits LockdownExitCode itself.
func Script(t Tools, l Lockdown) (string, error) {
	if !l.Enabled {
		return "", errors.New("egress: Script called for a disabled lockdown")
	}
	if t.IP == "" {
		return "", ErrNoIPBinary
	}
	if l.Gateway == "" {
		return "", errors.New("egress: lockdown gateway address is empty")
	}
	// A port outside the valid range renders a rule that permits nothing (and
	// leaves the sandbox with no path to the proxy at all) or is rejected by the
	// firewall binary mid-script. Both are worse than refusing to render.
	if l.ProxyPort <= 0 || l.ProxyPort > 65535 {
		return "", fmt.Errorf("egress: invalid proxy port %d for the egress lockdown, must be 1-65535", l.ProxyPort)
	}
	// Refusing to render without a marker path is what keeps LockdownApplied
	// meaningful. A script with no marker exits 78 on abort and leaves nothing
	// behind on success, so its caller has only the ambiguous status to go on -
	// and would have to guess, which is the failure this signal exists to remove.
	if l.ReadyFile == "" {
		return "", errors.New("egress: lockdown ReadyFile is empty; the prologue needs a marker path so an abort can be told apart from a workload that exits " + strconv.Itoa(LockdownExitCode))
	}

	// The firewall backend is resolved into the rule list BEFORE any
	// namespace-mutating line is emitted: a host without a usable backend must
	// fail while the namespace still has its default route, not after the
	// surgery has already cut egress.
	firewall, err := FirewallCommands(t, l)
	if err != nil {
		return "", err
	}
	routes := RouteCommands(t, l.Gateway, scriptDevPlaceholder)

	var b strings.Builder
	b.WriteString("set -e\n")
	fmt.Fprintf(&b, "fail() { trap - EXIT; echo %s\"$1\" >&2; exit %d; }\n", shellQuote(scriptErrPrefix+" "), LockdownExitCode)
	// Everything before `exec` must leave with LockdownExitCode, not with the
	// status of whatever failed. `set -e` on its own exits with the failing
	// command's status - 127 for a `sh` that cannot find `awk` or `sleep`, both
	// of which are resolved through PATH here - and the caller maps only
	// LockdownExitCode back to a named lockdown error, so any other status is
	// reported as if the sandboxed program had exited with it. The trap is
	// cleared immediately before the exec so a workload's own status is never
	// rewritten, and `fail` clears it before exiting so the handler cannot
	// re-enter itself.
	fmt.Fprintf(&b, "aborted() { fail %s; }\n", shellQuote("aborted before the lockdown was applied"))
	b.WriteString("trap aborted EXIT\n")
	b.WriteString("dev=\"\"\n")
	b.WriteString("i=0\n")
	fmt.Fprintf(&b, "naptime=%s\n", deviceRetryDelay)
	fmt.Fprintf(&b, "naps=%d\n", deviceRetries)
	b.WriteString(sleepProbeLine)
	b.WriteString("while [ \"$i\" -lt \"$naps\" ]; do\n")
	// The query and the parse are two steps because a pipeline takes the status
	// of its LAST command: `ip | awk` reports awk's success even when `ip`
	// failed, so a broken iproute2 would be swallowed, retried until the loop
	// gave up, and then reported as "no default route device" - blaming pasta
	// for a missing prerequisite. An `ip` that exits non-zero is a broken tool,
	// not a namespace that has not settled yet (no default route is reported as
	// empty output with status 0), so it aborts instead of retrying.
	fmt.Fprintf(&b, "routes=$(%s -o route show default) || fail %s\n", shellQuote(t.IP), shellQuote("failed to query the default route in the pasta network namespace: "+t.IP+" -o route show default"))
	fmt.Fprintf(&b, "dev=$(printf '%%s\\n' \"$routes\" | awk %s) || fail %s\n", shellQuote(awkFirstDefaultRouteDev), shellQuote("failed to parse the default route device"))
	b.WriteString("if [ -n \"$dev\" ]; then break; fi\n")
	b.WriteString("i=$((i+1))\n")
	b.WriteString("sleep \"$naptime\"\n")
	b.WriteString("done\n")
	fmt.Fprintf(&b, "if [ -z \"$dev\" ]; then fail %s; fi\n", shellQuote("no default route device in the pasta network namespace"))
	for _, cmd := range append(routes, firewall...) {
		fmt.Fprintf(&b, "%s\n", renderCommand(cmd))
	}
	// The marker is created after the last rule and before the exec, so its
	// existence means exactly "everything the lockdown does, it did". It is a
	// shell redirection rather than a command so the signal does not depend on
	// finding `touch` on PATH - the same reason the abort path needs the EXIT
	// trap. A failing redirection aborts like any other step: the launch is
	// refused rather than started with a signal nobody can read.
	fmt.Fprintf(&b, ": > %s || fail %s\n", shellQuote(l.ReadyFile), shellQuote("failed to record that the lockdown was applied: "+l.ReadyFile))
	b.WriteString("trap - EXIT\n")
	b.WriteString(`exec "$@"`)

	return b.String(), nil
}
