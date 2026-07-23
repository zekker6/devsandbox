//go:build !linux

package egress

import (
	"errors"
	"strings"
	"testing"
)

// TestProbe_NonLinuxFailsClosed asserts the stub reports an error rather than
// nil. A nil would read as "the lockdown is enforceable here" on a platform
// where it is not applied at all, which is the direction that gets a user a
// sandbox weaker than the one they were promised.
func TestProbe_NonLinuxFailsClosed(t *testing.T) {
	tools := Tools{IP: "/usr/sbin/ip", Firewall: "/usr/sbin/nft", Backend: BackendNft}
	err := Probe(tools, ProbeLockdown())
	if err == nil {
		t.Fatal("Probe() = nil on a non-Linux platform, want an error")
	}
	if !strings.Contains(err.Error(), "only supported on Linux") {
		t.Errorf("Probe() error = %q, want it to name the Linux requirement", err)
	}

	var pe *ProbeError
	if !errors.As(err, &pe) {
		t.Fatalf("Probe() error = %T, want *ProbeError so callers can classify it", err)
	}
}

// TestCheck_NonLinuxReportsRemediation asserts the doctor row on a non-Linux
// host is a failure carrying remediation rather than a bare "OK".
func TestCheck_NonLinuxReportsRemediation(t *testing.T) {
	got := check(lookPathFor("ip", "nft"), Probe)
	if got.OK {
		t.Error("check() reported OK on a platform with no lockdown probe")
	}
	if got.Hint == "" {
		t.Error("a failing check must carry remediation")
	}
}
