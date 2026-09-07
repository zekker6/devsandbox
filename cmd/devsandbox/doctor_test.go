package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"devsandbox/internal/egress"
	"devsandbox/internal/isolator"
	"devsandbox/internal/logrotate"
	"devsandbox/internal/sandbox"
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

// TestCheckKrun_NoFirewallRow guards the move: the firewall prerequisite gates
// bwrap proxy mode too, so nesting it under "krun:" would hide a hard abort of
// the DEFAULT backend behind a section bwrap users skip.
func TestCheckKrun_NoFirewallRow(t *testing.T) {
	for _, r := range checkKrun() {
		if strings.Contains(r.name, "firewall") {
			t.Errorf("firewall row %q is still nested under the krun section", r.name)
		}
	}
}

// TestCheckEgressFirewall asserts the proxy firewall row is top-level,
// backend-neutral, and advisory: proxy mode is opt-in, so an unmet prerequisite
// must never fail a doctor run for users who do not enable it.
func TestCheckEgressFirewall(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the egress lockdown, and its row, are Linux-only")
	}

	r := checkEgressFirewall()
	if r.name != "proxy: firewall" {
		t.Errorf("name = %q, want a backend-neutral %q", r.name, "proxy: firewall")
	}
	if strings.HasPrefix(r.name, "krun") {
		t.Errorf("name = %q, want a row not scoped to krun", r.name)
	}
	if r.status != "ok" && r.status != "warn" {
		t.Errorf("status = %q, want ok/warn; proxy mode is opt-in and must not fail doctor", r.status)
	}
	if r.message == "" {
		t.Error("row has an empty message")
	}
	if r.status == "warn" {
		for _, want := range []string{"modprobe", "bwrap", "krun"} {
			if !strings.Contains(r.hint, want) {
				t.Errorf("hint = %q, want it to mention %q", r.hint, want)
			}
		}
	}
}

// TestCheckEgressFirewall_NotInDoctorErrors pins the severity end to end: a host
// that cannot apply the lockdown produces a warn row, and doctor still exits
// zero on it. Driving the real function with a failing check is what makes this
// meaningful - the row is built on a developer machine whose netfilter works, so
// asserting on the live result could never observe the failing case.
func TestCheckEgressFirewall_NotInDoctorErrors(t *testing.T) {
	prev := egressCheck
	egressCheck = func() egress.CheckResult {
		return egress.CheckResult{OK: false, Summary: "no nft or iptables", Hint: egress.CheckHint}
	}
	t.Cleanup(func() { egressCheck = prev })

	row := checkEgressFirewall()
	if row.status != "warn" {
		t.Errorf("status = %q for an unenforceable host, want warn; proxy mode is opt-in and must not fail doctor", row.status)
	}
	if row.hint == "" {
		t.Error("a failing row carries no remediation")
	}

	rows := []checkResult{{name: "bwrap", status: "ok"}, row}
	if _, failed := doctorSummary(rows); failed {
		t.Error("a failing proxy firewall row failed the doctor run; it is advisory")
	}
}

// TestCheckEgressFirewall_OKRow asserts the satisfied case reports ok and
// carries no remediation, so doctor does not tell a working host to install
// packages it already has.
func TestCheckEgressFirewall_OKRow(t *testing.T) {
	prev := egressCheck
	egressCheck = func() egress.CheckResult {
		return egress.CheckResult{OK: true, Summary: "/usr/sbin/nft (lockdown rules apply)"}
	}
	t.Cleanup(func() { egressCheck = prev })

	row := checkEgressFirewall()
	if row.status != "ok" {
		t.Errorf("status = %q for an enforceable host, want ok", row.status)
	}
	if row.hint != "" {
		t.Errorf("hint = %q on a satisfied row, want none", row.hint)
	}
}

func TestDoctorSummary(t *testing.T) {
	tests := []struct {
		name       string
		results    []checkResult
		wantFailed bool
		want       string
	}{
		{
			name:    "all ok",
			results: []checkResult{{name: "bwrap", status: "ok"}},
			want:    "All checks passed!",
		},
		{
			name: "advisory warning with hint points at the How to fix block",
			results: []checkResult{
				{name: "bwrap", status: "ok"},
				{name: "krun: system pasta", status: "warn", hint: "install passt"},
			},
			want: `All required checks passed (1 advisory warning(s) - see "How to fix" above).`,
		},
		{
			name: "advisory warning without hint does not reference the block",
			results: []checkResult{
				{name: "docker-image", status: "warn"},
			},
			want: "All required checks passed (1 advisory warning(s)).",
		},
		{
			name: "error outranks warnings and the advisory-only block is labelled",
			results: []checkResult{
				{name: "krun: system pasta", status: "warn", hint: "install passt"},
				{name: "shell", status: "error", missingDep: true},
			},
			wantFailed: true,
			want:       `Some checks failed: shell. Please install the missing dependencies. The "How to fix" block above covers advisory warnings only.`,
		},
		{
			name: "failure with no hints anywhere does not mention the block",
			results: []checkResult{
				{name: "bwrap", status: "error", missingDep: true},
			},
			wantFailed: true,
			want:       "Some checks failed: bwrap. Please install the missing dependencies.",
		},
		{
			name: "failing row with its own hint points at the block",
			results: []checkResult{
				{name: "krun: system pasta", status: "warn", hint: "install passt"},
				{name: "bwrap", status: "error", hint: "install bubblewrap", missingDep: true},
			},
			wantFailed: true,
			want:       `Some checks failed: bwrap. Please install the missing dependencies. See "How to fix" above.`,
		},
		{
			name: "failure that is not a missing dependency does not advise installing anything",
			results: []checkResult{
				{name: "directories", status: "error"},
				{name: "config", status: "error"},
			},
			wantFailed: true,
			want:       "Some checks failed: directories, config.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, failed := doctorSummary(tt.results)
			if failed != tt.wantFailed {
				t.Errorf("failed = %v, want %v", failed, tt.wantFailed)
			}
			if got != tt.want {
				t.Errorf("msg = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCheckRecentLogsStaysAdvisory pins the logs row to a warning no matter how
// many errors past runs recorded. Errors already written to a log file say
// nothing about whether this host can launch a sandbox, so failing the run on
// them would break `devsandbox doctor` as a CI/script gate.
func TestCheckRecentLogsStaysAdvisory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	logDir := filepath.Join(home, ".local", "share", sandbox.SandboxBaseDir, "proj", "logs", "internal")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	now := time.Now()
	for i := range 50 {
		fmt.Fprintf(&b, "%s [proxy] boom\n", now.Add(-time.Duration(i)*time.Minute).Format(time.RFC3339))
	}
	if err := os.WriteFile(filepath.Join(logDir, "errors.log"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	r := checkRecentLogs()
	if r.status != "warn" {
		t.Errorf("status = %q, want %q (message: %s)", r.status, "warn", r.message)
	}
	if !strings.Contains(r.message, "50 errors in last 24h") {
		t.Errorf("message = %q, want it to report 50 recent errors", r.message)
	}
	if !strings.Contains(r.hint, "devsandbox logs internal") {
		t.Errorf("hint = %q, want it to name the logs command", r.hint)
	}
}

// TestCheckRecentLogsReadsRotatedBackups pins the scan to the files rotation
// left the entries in. Right after a rotation the live log is empty and every
// entry from the last 24h sits in the backup beside it, so a scan filtered on
// the .log suffix alone reports a clean host precisely when it is not one.
func TestCheckRecentLogsReadsRotatedBackups(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	logDir := filepath.Join(home, ".local", "share", sandbox.SandboxBaseDir, "proj", "logs", "internal")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	live := filepath.Join(logDir, "logging-errors.log")
	if err := os.WriteFile(live, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	now := time.Now()
	for i := range 3 {
		fmt.Fprintf(&b, "%s [logging] boom\n", now.Add(-time.Duration(i)*time.Minute).Format(time.RFC3339))
	}
	if err := os.WriteFile(logrotate.BackupPath(live, 1), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	r := checkRecentLogs()
	if !strings.Contains(r.message, "3 errors in last 24h") {
		t.Errorf("message = %q, want it to count the errors in the rotated backup", r.message)
	}
	if !strings.Contains(r.message, "logging:3") {
		t.Errorf("message = %q, want the component breakdown from the backup", r.message)
	}
}

// TestCheckRecentLogsReadsBackupWithoutLiveFile pins discovery to the backups
// themselves. logrotate.Rotate renames the live file aside before its caller
// recreates it, so a process killed between the two leaves logging-errors.log.1
// alone in the directory - and a scan that only ever starts from a .log entry
// reports a clean host while every recent error sits right there.
func TestCheckRecentLogsReadsBackupWithoutLiveFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	logDir := filepath.Join(home, ".local", "share", sandbox.SandboxBaseDir, "proj", "logs", "internal")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	live := filepath.Join(logDir, "logging-errors.log")

	var b strings.Builder
	now := time.Now()
	for i := range 2 {
		fmt.Fprintf(&b, "%s [logging] boom\n", now.Add(-time.Duration(i)*time.Minute).Format(time.RFC3339))
	}
	if err := os.WriteFile(logrotate.BackupPath(live, 1), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(live); !os.IsNotExist(err) {
		t.Fatalf("live log must not exist for this case, Lstat err = %v", err)
	}

	r := checkRecentLogs()
	if !strings.Contains(r.message, "2 errors in last 24h") {
		t.Errorf("message = %q, want it to count the errors in the orphaned backup", r.message)
	}
	if !strings.Contains(r.message, "logging:2") {
		t.Errorf("message = %q, want the component breakdown from the backup", r.message)
	}
}

func TestLiveLogName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{name: "sandbox.log", want: "sandbox.log"},
		{name: "sandbox.log.1", want: "sandbox.log"},
		{name: "sandbox.log.2", want: "sandbox.log"},
		// One past the last slot rotation uses, derived so a change to the file
		// limit moves the case rather than breaking it.
		{name: fmt.Sprintf("sandbox.log.%d", logrotate.DefaultMaxFiles), want: ""},
		{name: "sandbox.log.lock", want: ""},
		{name: "sandbox.txt", want: ""},
		{name: "sandbox", want: ""},
	}
	for _, tt := range tests {
		if got := liveLogName(tt.name); got != tt.want {
			t.Errorf("liveLogName(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// TestCheckTIOCSTI drives the tty row through a file the test controls, since
// the live /proc value on a developer machine reports only one of the four
// states. Every unmet case is a warn, never an error: the gap is a kernel
// property that bwrap cannot close, so doctor must not fail on it.
func TestCheckTIOCSTI(t *testing.T) {
	writeProc := func(t *testing.T, content string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "legacy_tiocsti")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	tests := []struct {
		name        string
		path        func(t *testing.T) string
		wantStatus  string
		wantMessage []string
		wantHint    []string
	}{
		{
			name:        "disabled",
			path:        func(t *testing.T) string { return writeProc(t, "0\n") },
			wantStatus:  "ok",
			wantMessage: []string{"TIOCSTI disabled by the kernel"},
		},
		{
			name:        "enabled",
			path:        func(t *testing.T) string { return writeProc(t, "1\n") },
			wantStatus:  "warn",
			wantMessage: []string{"legacy_tiocsti=1"},
			wantHint:    []string{"dev.tty.legacy_tiocsti=0"},
		},
		{
			name:        "missing file is a pre-6.2 kernel",
			path:        func(t *testing.T) string { return filepath.Join(t.TempDir(), "legacy_tiocsti") },
			wantStatus:  "warn",
			wantMessage: []string{"kernel before 6.2", "TIOCSTI cannot be disabled", "inject keystrokes"},
			wantHint:    []string{"6.2"},
		},
		{
			name:        "garbage names the value",
			path:        func(t *testing.T) string { return writeProc(t, "maybe\n") },
			wantStatus:  "warn",
			wantMessage: []string{`"maybe"`},
		},
		{
			name: "unreadable names the error",
			// A directory is unreadable as a file on every host, root included;
			// a 0000 file is readable by root and so would pass in CI.
			path:        func(t *testing.T) string { return t.TempDir() },
			wantStatus:  "warn",
			wantMessage: []string{"cannot read"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := checkTIOCSTI(tt.path(t))
			if r.name != "tty" {
				t.Errorf("name = %q, want %q", r.name, "tty")
			}
			if r.status != tt.wantStatus {
				t.Errorf("status = %q, want %q (message %q)", r.status, tt.wantStatus, r.message)
			}
			for _, want := range tt.wantMessage {
				if !strings.Contains(r.message, want) {
					t.Errorf("message = %q, want it to contain %q", r.message, want)
				}
			}
			for _, want := range tt.wantHint {
				if !strings.Contains(r.hint, want) {
					t.Errorf("hint = %q, want it to contain %q", r.hint, want)
				}
			}
			if r.status == "ok" && r.hint != "" {
				t.Errorf("hint = %q on a satisfied row, want none", r.hint)
			}
			if r.status == "warn" && r.hint == "" {
				t.Error("a warn row carries no remediation")
			}
			if _, failed := doctorSummary([]checkResult{r}); failed {
				t.Error("the tty row failed the doctor run; it is advisory")
			}
		})
	}
}
