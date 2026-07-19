package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEgressLockdownRequested(t *testing.T) {
	tests := []struct {
		name   string
		set    bool
		value  string
		expect bool
	}{
		{name: "unset", set: false, expect: false},
		{name: "empty", set: true, value: "", expect: false},
		{name: "zero", set: true, value: "0", expect: false},
		{name: "other", set: true, value: "true", expect: false},
		{name: "one", set: true, value: "1", expect: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv(envEgressLockdown, tt.value)
			} else {
				t.Setenv(envEgressLockdown, "")
			}
			if got := egressLockdownRequested(); got != tt.expect {
				t.Errorf("egressLockdownRequested() = %v, want %v (value=%q set=%v)", got, tt.expect, tt.value, tt.set)
			}
		})
	}
}

// TestWaitForEgressSentinel_Present asserts the wait returns nil promptly once
// the sentinel exists.
func TestWaitForEgressSentinel_Present(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, egressSentinelName)
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := waitForEgressSentinel(path, time.Second); err != nil {
		t.Fatalf("expected nil for existing sentinel, got %v", err)
	}
}

// TestWaitForEgressSentinel_AppearsLater asserts the wait succeeds when the
// sentinel shows up partway through the timeout (the host writes it after boot).
func TestWaitForEgressSentinel_AppearsLater(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, egressSentinelName)

	done := make(chan error, 1)
	go func() { done <- waitForEgressSentinel(path, 5*time.Second) }()

	time.Sleep(400 * time.Millisecond)
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil once sentinel appears, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("wait did not return after sentinel appeared")
	}
}

// TestWaitForEgressSentinel_Timeout asserts the wait fails closed when the
// sentinel never appears - the caller must abort rather than run with open
// egress.
func TestWaitForEgressSentinel_Timeout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, egressSentinelName)
	if err := waitForEgressSentinel(path, 300*time.Millisecond); err == nil {
		t.Fatal("expected timeout error for missing sentinel, got nil (egress would be unconfirmed)")
	}
}

// TestWaitForEgressSentinel_RejectsDirectory is the regression guard for the
// stale-directory sentinel spoof. The sentinel lives in the persistent,
// guest-writable home, so an untrusted run can pre-create a DIRECTORY at the
// sentinel path. The old Stat-only gate accepted anything that existed and would
// release the workload while direct egress was still open. Only a regular file -
// which only the host's writeEgressSentinel produces - must satisfy the gate; a
// directory must be ignored so the wait times out fail-closed.
func TestWaitForEgressSentinel_RejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, egressSentinelName)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := waitForEgressSentinel(path, 300*time.Millisecond); err == nil {
		t.Fatal("expected timeout error for a directory at the sentinel path, got nil (stale-directory spoof would open egress)")
	}
}

// TestWaitForEgressSentinel_RejectsSymlinkToRegularFile hardens the gate against
// a symlink spoof. The sentinel path lives in the persistent, guest-writable home,
// so an untrusted run could plant a symlink there pointing at a real regular file
// it controls. os.Stat would FOLLOW the link and its IsRegular() check would pass,
// releasing the workload while direct egress is still open. os.Lstat inspects the
// link itself (ModeSymlink, not regular), so the gate must keep polling and time
// out fail-closed.
func TestWaitForEgressSentinel_RejectsSymlinkToRegularFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "attacker-file")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, egressSentinelName)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := waitForEgressSentinel(path, 300*time.Millisecond); err == nil {
		t.Fatal("expected timeout for a symlink-to-regular-file at the sentinel path, got nil (symlink spoof would open egress)")
	}
}

// TestWaitForEgressSentinel_AcceptsRegularFileOverStaleDirectory asserts that
// once the host replaces a stale directory with a real regular-file sentinel the
// gate opens - the regular-file requirement must not deadlock the legitimate
// lockdown path.
func TestWaitForEgressSentinel_AcceptsRegularFileOverStaleDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, egressSentinelName)
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := waitForEgressSentinel(path, time.Second); err != nil {
		t.Fatalf("expected nil for a regular-file sentinel, got %v", err)
	}
}
