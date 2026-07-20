package main

import (
	"runtime"
	"strings"
	"testing"

	"devsandbox/internal/isolator"
)

func TestCheckGit(t *testing.T) {
	r := checkGit()
	if r.name != "git" {
		t.Errorf("name = %q, want %q", r.name, "git")
	}
	switch r.status {
	case "ok", "warn", "error":
	default:
		t.Errorf("unexpected status %q", r.status)
	}
	if r.message == "" {
		t.Error("message is empty")
	}
}

// TestMicroVMResults verifies the structured-to-row mapping: satisfied
// prerequisites become "ok" rows, unmet ones become "warn" (never "error", so a
// krun-less host does not fail doctor), and every row is prefixed/grouped.
func TestMicroVMResults(t *testing.T) {
	in := []isolator.MicroVMCheck{
		{Name: "podman", OK: false, Summary: "podman not installed"},
		{Name: "runtime", OK: false, Summary: "krun OCI runtime not found"},
		{Name: "kvm", OK: true, Summary: "/dev/kvm accessible"},
	}

	got := microVMResults(in)
	if len(got) != len(in) {
		t.Fatalf("got %d rows, want %d", len(got), len(in))
	}

	want := []checkResult{
		{name: "krun: podman", status: "warn", message: "podman not installed"},
		{name: "krun: runtime", status: "warn", message: "krun OCI runtime not found"},
		{name: "krun: kvm", status: "ok", message: "/dev/kvm accessible"},
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("row %d = %+v, want %+v", i, got[i], w)
		}
	}
}

// TestMicroVMResults_NeverError guards the opt-in invariant: krun rows must
// never carry "error" status, otherwise doctor would exit non-zero for users
// who only run bwrap/docker.
func TestMicroVMResults_NeverError(t *testing.T) {
	in := []isolator.MicroVMCheck{
		{Name: "podman", OK: false, Summary: "missing"},
		{Name: "kvm", OK: false, Summary: "missing"},
	}
	for _, r := range microVMResults(in) {
		if r.status == "error" {
			t.Errorf("row %q has error status; krun rows must be ok/warn only", r.name)
		}
	}
}

// TestMicroVMResults_CarriesHint asserts the remediation a check builds survives
// the mapping into doctor rows. The DETAILS column only fits the summary, so a
// dropped Hint would silently strip every krun fix instruction from the output.
func TestMicroVMResults_CarriesHint(t *testing.T) {
	in := []isolator.MicroVMCheck{
		{Name: "system pasta", OK: false, Summary: "pasta not installed", Hint: "install the passt package"},
		{Name: "podman", OK: true, Summary: "/usr/bin/podman"},
	}

	rows := microVMResults(in)
	if rows[0].hint != "install the passt package" {
		t.Errorf("hint = %q, want the check's remediation", rows[0].hint)
	}
	if rows[1].hint != "" {
		t.Errorf("satisfied check produced hint %q, want empty", rows[1].hint)
	}
}

// TestCheckKrun_Rows exercises the live doctor section: rows are grouped under
// "krun:", statuses are ok/warn only, and the KVM plus rootless-prerequisite rows
// are Linux-only.
func TestCheckKrun_Rows(t *testing.T) {
	rows := checkKrun()
	if len(rows) == 0 {
		t.Fatal("checkKrun returned no rows")
	}

	linuxOnly := map[string]bool{
		"krun: kvm":                 false,
		"krun: firewall":            false,
		"krun: system pasta":        false,
		"krun: rootless id mapping": false,
	}
	hasKVM := false
	for _, r := range rows {
		if _, ok := linuxOnly[r.name]; ok {
			linuxOnly[r.name] = true
		}
		if !strings.HasPrefix(r.name, "krun: ") {
			t.Errorf("row name %q is not grouped under 'krun: '", r.name)
		}
		if r.status != "ok" && r.status != "warn" {
			t.Errorf("row %q status = %q, want ok/warn", r.name, r.status)
		}
		if r.message == "" {
			t.Errorf("row %q has empty message", r.name)
		}
		if r.name == "krun: kvm" {
			hasKVM = true
		}
	}

	switch runtime.GOOS {
	case "linux":
		if !hasKVM {
			t.Error("expected a 'krun: kvm' row on linux")
		}
		for name, present := range linuxOnly {
			if !present {
				t.Errorf("expected a %q row on linux", name)
			}
		}
	case "darwin":
		if hasKVM {
			t.Error("'krun: kvm' row must be absent on darwin (HVF has no /dev/kvm)")
		}
		for name, present := range linuxOnly {
			if present {
				t.Errorf("%q row must be absent on darwin (no rootless podman/egress lockdown there)", name)
			}
		}
	}
}
