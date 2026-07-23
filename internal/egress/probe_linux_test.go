//go:build linux

package egress

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestProbe_ClassifiesFailure runs the real probe. Its verdict depends on the
// host (netfilter modules, unprivileged user namespaces), so the assertion is on
// the SHAPE of a failure rather than on success: whatever the answer, it must be
// a classified *ProbeError with a one-line diagnostic, because that string is
// what doctor renders and what a launch abort names. An unclassified error would
// leave a user with no idea which of the binary, the namespace, or the kernel
// refused.
func TestProbe_ClassifiesFailure(t *testing.T) {
	tools, err := ResolveTools(exec.LookPath)
	if err != nil {
		t.Skipf("host has no lockdown tools: %v", err)
	}

	if err := Probe(tools, ProbeLockdown()); err != nil {
		var pe *ProbeError
		if !errors.As(err, &pe) {
			t.Fatalf("Probe() error = %T (%v), want *ProbeError", err, err)
		}
		if pe.Stage.String() == "unknown" {
			t.Errorf("Probe() error stage is unclassified: %v", err)
		}
		if strings.Contains(err.Error(), "\n") {
			t.Errorf("Probe() error = %q, want a single line", err)
		}
		t.Logf("probe reported an unenforceable host (expected on a host without netfilter): %v", err)
	}
}

// TestProbe_UnusableBinaryIsRuleFailure asserts a firewall binary that cannot
// apply the rules is reported as a rule-application failure carrying the tool's
// own diagnostic, not as a success. /bin/false stands in for a binary that
// resolves and then refuses everything - the observable shape of a host missing
// the kernel modules.
func TestProbe_UnusableBinaryIsRuleFailure(t *testing.T) {
	tools := Tools{IP: "/bin/false", Firewall: "/bin/false", Backend: BackendNft}

	err := Probe(tools, ProbeLockdown())
	if err == nil {
		t.Fatal("Probe() = nil for a firewall binary that fails every rule")
	}
	var pe *ProbeError
	if !errors.As(err, &pe) {
		t.Fatalf("Probe() error = %T (%v), want *ProbeError", err, err)
	}
	if pe.Stage != ProbeStageRules && pe.Stage != ProbeStageNamespace {
		t.Errorf("Probe() stage = %v, want rule application (or a namespace failure on a host without unprivileged userns)", pe.Stage)
	}
}

// TestProbe_CarriesToolDiagnostic asserts the firewall tool's own stderr reaches
// ProbeError.Detail. That string is the only thing that distinguishes "nftables
// is missing" from "nftables is here but the kernel refused the rules" - the
// nf_conntrack case the probe exists to catch - and it is what doctor renders
// and what the launch preflight reports. Without it both hosts produce the same
// bare "exit status 1".
func TestProbe_CarriesToolDiagnostic(t *testing.T) {
	const diagnostic = "Error: Could not process rule: No such file or directory"
	stub := filepath.Join(t.TempDir(), "nft")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho '"+diagnostic+"' >&2\necho 'offending rule' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write stub firewall binary: %v", err)
	}

	err := Probe(Tools{IP: "/usr/sbin/ip", Firewall: stub, Backend: BackendNft}, ProbeLockdown())
	if err == nil {
		t.Fatal("Probe() = nil for a firewall binary that refuses every rule")
	}
	var pe *ProbeError
	if !errors.As(err, &pe) {
		t.Fatalf("Probe() error = %T (%v), want *ProbeError", err, err)
	}
	if pe.Stage == ProbeStageNamespace {
		t.Skipf("host cannot create an unprivileged user namespace: %v", err)
	}
	if pe.Detail != diagnostic {
		t.Errorf("ProbeError.Detail = %q, want the tool's first stderr line %q", pe.Detail, diagnostic)
	}
	if !strings.Contains(err.Error(), diagnostic) {
		t.Errorf("Probe() error = %q, want it to carry %q", err, diagnostic)
	}
}

// TestProbe_NoBackend asserts the probe refuses a Tools with no resolved backend
// instead of starting a namespace to run nothing in it and reporting success.
func TestProbe_NoBackend(t *testing.T) {
	err := Probe(Tools{IP: "/usr/sbin/ip"}, ProbeLockdown())
	if !errors.Is(err, ErrNoFirewallBackend) {
		t.Fatalf("Probe() error = %v, want ErrNoFirewallBackend", err)
	}
	var pe *ProbeError
	if !errors.As(err, &pe) || pe.Stage != ProbeStageTools {
		t.Errorf("Probe() error = %v, want it classified as a firewall-binary failure", err)
	}
}
