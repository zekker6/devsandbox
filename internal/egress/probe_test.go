package egress

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestProbeLockdown asserts the probe exercises a rule set a real launch could
// render: enabled, with a valid gateway and port. A malformed one would make the
// probe fail on every host and report a host problem that is really a bug here.
func TestProbeLockdown(t *testing.T) {
	l := ProbeLockdown()
	if !l.Enabled {
		t.Error("ProbeLockdown() is disabled; the probe must apply the real rule set")
	}
	if l.Gateway == "" {
		t.Error("ProbeLockdown() has an empty gateway")
	}
	if l.ProxyPort <= 0 || l.ProxyPort > 65535 {
		t.Errorf("ProbeLockdown() port = %d, want a valid port", l.ProxyPort)
	}
	// Script additionally needs a marker path, which the probe itself has no use
	// for: it runs the rules directly and reads their status. Supplying one here
	// keeps this a check of the rule set, which is what the two share.
	l.ReadyFile = "/run/devsandbox-test/applied"
	if _, err := Script(Tools{IP: "/usr/sbin/ip", Firewall: "/usr/sbin/nft", Backend: BackendNft}, l); err != nil {
		t.Errorf("ProbeLockdown() does not render: %v", err)
	}
}

// TestProbeScript asserts the probe applies the FULL firewall rule set - the
// point of the probe, since a host with nf_tables but no nf_conntrack only fails
// on the `ct state` rule - and that it emits no route surgery, which the
// throwaway namespace has no default route for.
func TestProbeScript(t *testing.T) {
	tools := Tools{IP: "/usr/sbin/ip", Firewall: "/usr/sbin/nft", Backend: BackendNft}
	script, err := probeScript(tools, ProbeLockdown())
	if err != nil {
		t.Fatalf("probeScript() error = %v", err)
	}

	want, err := FirewallCommands(tools, ProbeLockdown())
	if err != nil {
		t.Fatalf("FirewallCommands() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(script), "\n")
	if lines[0] != "set -e" {
		t.Errorf("probe script does not start fail-closed: %q", lines[0])
	}
	if got := len(lines) - 1; got != len(want) {
		t.Fatalf("probe script has %d rule lines, want the full set of %d:\n%s", got, len(want), script)
	}
	for i, cmd := range want {
		if lines[i+1] != quoteArgv(cmd) {
			t.Errorf("rule %d = %q, want %q", i, lines[i+1], quoteArgv(cmd))
		}
	}
	if strings.Contains(script, "route") {
		t.Errorf("probe script performs route surgery; it must only apply firewall rules:\n%s", script)
	}
	if !strings.Contains(script, "ct") || !strings.Contains(script, "established,related") {
		t.Errorf("probe script omits the conntrack rule, the one that needs nf_conntrack:\n%s", script)
	}
}

// TestProbeScript_NoBackend asserts the probe refuses to render without a
// resolved backend rather than probing an empty rule set and reporting success.
func TestProbeScript_NoBackend(t *testing.T) {
	if _, err := probeScript(Tools{IP: "/usr/sbin/ip"}, ProbeLockdown()); !errors.Is(err, ErrNoFirewallBackend) {
		t.Errorf("probeScript() error = %v, want ErrNoFirewallBackend", err)
	}
}

// TestProbeStageString pins the stage names: they are what tells a user whether
// the binary, the namespace, or the kernel's acceptance of the rules failed.
func TestProbeStageString(t *testing.T) {
	tests := []struct {
		stage ProbeStage
		want  string
	}{
		{ProbeStageTools, "firewall binary"},
		{ProbeStageNamespace, "test namespace"},
		{ProbeStageRules, "rule application"},
		{ProbeStage(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.stage.String(); got != tt.want {
			t.Errorf("ProbeStage(%d).String() = %q, want %q", tt.stage, got, tt.want)
		}
	}
}

// TestProbeErrorFormatting asserts the error names its stage, carries the tool's
// own diagnostic, unwraps to the cause, and stays on one line - both the doctor
// table cell and the launch abort message are one-liners.
func TestProbeErrorFormatting(t *testing.T) {
	cause := errors.New("exit status 1")
	err := &ProbeError{
		Stage:  ProbeStageRules,
		Detail: "Error: Could not process rule:\nNo such file or directory",
		Err:    cause,
	}

	msg := err.Error()
	for _, want := range []string{"rule application", "exit status 1", "No such file or directory"} {
		if !strings.Contains(msg, want) {
			t.Errorf("ProbeError.Error() = %q, want it to contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "\n") {
		t.Errorf("ProbeError.Error() = %q, want a single line", msg)
	}
	if !errors.Is(err, cause) {
		t.Error("ProbeError does not unwrap to its cause")
	}
}

// TestCheck asserts the doctor row is driven by the probe, not by binary
// presence alone: a host whose binaries resolve but whose kernel refuses the
// rules must report a failure, which is the nf_conntrack case the probe exists
// for. It also pins the fail-closed reporting for a missing binary.
func TestCheck(t *testing.T) {
	probeOK := func(Tools, Lockdown) error { return nil }
	probeFails := func(Tools, Lockdown) error {
		return &ProbeError{Stage: ProbeStageRules, Detail: "No such file or directory", Err: errors.New("exit status 1")}
	}

	tests := []struct {
		name        string
		present     []string
		probe       func(Tools, Lockdown) error
		wantOK      bool
		wantSummary string
	}{
		{name: "nft applies", present: []string{"ip", "nft", "iptables"}, probe: probeOK, wantOK: true, wantSummary: "/usr/sbin/nft"},
		{name: "iptables fallback applies", present: []string{"ip", "iptables"}, probe: probeOK, wantOK: true, wantSummary: "/usr/sbin/iptables"},
		{name: "no firewall binary", present: []string{"ip"}, probe: probeOK, wantOK: false, wantSummary: "nft or iptables"},
		{name: "no ip binary", present: []string{"nft"}, probe: probeOK, wantOK: false, wantSummary: "iproute2"},
		{name: "rules refused", present: []string{"ip", "nft"}, probe: probeFails, wantOK: false, wantSummary: "rule application"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := check(lookPathFor(tt.present...), tt.probe)
			if got.OK != tt.wantOK {
				t.Errorf("OK = %v, want %v (summary %q)", got.OK, tt.wantOK, got.Summary)
			}
			if !strings.Contains(got.Summary, tt.wantSummary) {
				t.Errorf("Summary = %q, want it to mention %q", got.Summary, tt.wantSummary)
			}
			if strings.Contains(got.Summary, "\n") {
				t.Errorf("Summary = %q, want a single line for the doctor table cell", got.Summary)
			}
			if tt.wantOK {
				if got.Hint != "" {
					t.Errorf("a satisfied check carries Hint %q, want empty", got.Hint)
				}
				return
			}
			if got.Hint == "" {
				t.Fatal("a failing check must carry remediation")
			}
			for _, want := range []string{"modprobe", "nf_conntrack", "bwrap", "krun"} {
				if !strings.Contains(got.Hint, want) {
					t.Errorf("Hint = %q, want it to mention %q", got.Hint, want)
				}
			}
		})
	}
}

// TestCheck_ProbeReceivesResolvedTools asserts the probe is handed the resolved
// absolute paths rather than re-resolving them, so doctor reports on the same
// binaries a launch would use.
func TestCheck_ProbeReceivesResolvedTools(t *testing.T) {
	var got Tools
	var gotLockdown Lockdown
	check(lookPathFor("ip", "nft"), func(tools Tools, l Lockdown) error {
		got, gotLockdown = tools, l
		return nil
	})

	if got.IP != "/usr/sbin/ip" || got.Firewall != "/usr/sbin/nft" || got.Backend != BackendNft {
		t.Errorf("probe received %+v, want the resolved absolute paths", got)
	}
	if !reflect.DeepEqual(gotLockdown, ProbeLockdown()) {
		t.Errorf("probe received lockdown %+v, want ProbeLockdown()", gotLockdown)
	}
}

// TestCheck_NoProbeWithoutTools asserts an unresolvable binary short-circuits
// before the probe runs: there is nothing to apply, and running the probe anyway
// would report a namespace failure for what is really a missing package.
func TestCheck_NoProbeWithoutTools(t *testing.T) {
	probed := false
	res := check(lookPathFor(), func(Tools, Lockdown) error {
		probed = true
		return nil
	})
	if probed {
		t.Error("probe ran with no resolvable binaries")
	}
	if res.OK {
		t.Error("check reported OK with no resolvable binaries")
	}
}
