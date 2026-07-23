package egress

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// probeTimeout bounds the probe. It runs a handful of nft/iptables invocations,
// so anything approaching this is a wedged binary - and the probe runs both from
// `doctor` and from the launch preflight, neither of which may hang on it.
const probeTimeout = 10 * time.Second

// probeWaitDelay bounds the wait for the probe's output pipes after the deadline
// has already killed the shell. It exists because killing the shell does not
// close a pipe a surviving grandchild still holds, and Wait would otherwise
// block past probeTimeout indefinitely.
const probeWaitDelay = 2 * time.Second

// probeGateway and probeProxyPort parameterize the rule set the probe applies.
// The values are placeholders: the probe answers whether the rules APPLY on this
// host, not what they permit, and the throwaway namespace it applies them in
// carries no traffic. They are still a valid address/port pair so the rules the
// probe exercises are the ones a real launch renders.
const (
	probeGateway   = "10.0.2.2"
	probeProxyPort = 3128
)

// ProbeStage names which part of the enforceability check failed, so a caller
// can tell "this host has no nftables" from "nftables is here but the kernel
// refused the rules" - the second is the nf_conntrack case, and the two have
// different remediations.
type ProbeStage int

const (
	// ProbeStageTools covers resolving `ip` and the firewall binary and
	// building the rule set from them.
	ProbeStageTools ProbeStage = iota
	// ProbeStageNamespace covers creating the throwaway user+network namespace
	// the rules are applied in. A failure here means unprivileged user
	// namespaces are unavailable, which is a sandbox prerequisite of its own.
	ProbeStageNamespace
	// ProbeStageRules covers applying the rendered rule set. This is where a
	// host with nf_tables loaded but nf_conntrack absent fails, because the
	// rule set matches on `ct state`.
	ProbeStageRules
)

func (s ProbeStage) String() string {
	switch s {
	case ProbeStageTools:
		return "firewall binary"
	case ProbeStageNamespace:
		return "test namespace"
	case ProbeStageRules:
		return "rule application"
	default:
		return "unknown"
	}
}

// ProbeError reports a failed enforceability check, classified by stage. Detail
// carries the tool's own diagnostic (typically the first line of its stderr),
// which is where the kernel names the missing piece.
type ProbeError struct {
	Stage  ProbeStage
	Detail string
	Err    error
}

func (e *ProbeError) Error() string {
	msg := fmt.Sprintf("%s failed", e.Stage)
	if e.Err != nil {
		msg += ": " + collapse(e.Err.Error())
	}
	if e.Detail != "" {
		msg += ": " + collapse(e.Detail)
	}
	return msg
}

func (e *ProbeError) Unwrap() error { return e.Err }

// collapse folds a multi-line diagnostic into a single line. Both the doctor
// table cell and the launch abort message are one-liners, and a raw nft error
// spans several lines.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ProbeLockdown returns the lockdown the probe applies. It is enabled and
// otherwise representative so the probe exercises the same rule set a real
// proxy launch renders - probing with a subset (creating a table and stopping)
// would pass on a host that cannot satisfy the `ct state` rules.
func ProbeLockdown() Lockdown {
	return Lockdown{Enabled: true, Gateway: probeGateway, ProxyPort: probeProxyPort}
}

// probeScript renders the firewall rule set as a fail-closed shell script for
// the probe. Unlike Script it emits no route surgery: the throwaway namespace
// has no default route to operate on, and routing is not the part of the
// lockdown whose availability is in question.
func probeScript(t Tools, l Lockdown) (string, error) {
	cmds, err := FirewallCommands(t, l)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("set -e\n")
	for _, cmd := range cmds {
		b.WriteString(quoteArgv(cmd))
		b.WriteString("\n")
	}
	return b.String(), nil
}

// CheckResult is the host-capability report `doctor` renders as a row. It is
// deliberately backend-neutral: bwrap and krun proxy mode both hard-abort
// without a working firewall backend, so the row must not read as a krun-only
// prerequisite.
type CheckResult struct {
	OK      bool
	Summary string
	Hint    string
}

// CheckHint is the remediation for a failing check. It names every half of the
// requirement - iproute2, the firewall binary, and the kernel modules - because
// the check fails on any of the three and the last is the one a host silently
// lacks: nftables installed with nf_conntrack unloaded passes every
// binary-presence test and still cannot apply the rule set. iproute2 is named
// too because ResolveTools fails on a missing `ip` before it ever looks for a
// firewall backend, and "install nftables" is the wrong thing to do about that.
const CheckHint = "Install iproute2 and nftables (or iptables), and make sure the netfilter modules are loadable:\n" +
	"  sudo modprobe nf_tables nf_conntrack\n" +
	"bwrap AND krun proxy mode both need this - a proxy launch aborts without it.\n" +
	"Launches without --proxy are unaffected."

// Check reports whether this host can apply the proxy-mode egress lockdown, by
// actually applying it in a throwaway namespace rather than by looking for
// binaries. It is advisory at doctor time: proxy mode is opt-in, and a launch
// that needs the lockdown fails closed on its own.
func Check() CheckResult {
	return check(exec.LookPath, Probe)
}

// check is Check with the host-touching seams injected so the reporting is
// unit-testable on a host with or without netfilter.
func check(lookPath func(string) (string, error), probe func(Tools, Lockdown) error) CheckResult {
	tools, err := ResolveTools(lookPath)
	if err != nil {
		return CheckResult{OK: false, Summary: collapse(err.Error()), Hint: CheckHint}
	}
	if err := probe(tools, ProbeLockdown()); err != nil {
		return CheckResult{
			OK:      false,
			Summary: fmt.Sprintf("%s: %s", tools.Firewall, collapse(err.Error())),
			Hint:    CheckHint,
		}
	}
	return CheckResult{OK: true, Summary: tools.Firewall + " (lockdown rules apply)"}
}
