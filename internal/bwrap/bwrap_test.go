package bwrap

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestCheckInstalled(t *testing.T) {
	// With embedded binaries, CheckInstalled should always succeed
	// (extraction to cache dir or system fallback).
	// It can only fail if both embedded extraction AND system LookPath fail.
	err := CheckInstalled()
	// Don't assert success/failure — depends on test environment.
	// Just verify it doesn't panic.
	t.Logf("CheckInstalled() error: %v", err)
}

func TestPastaSupportsMapHostLoopback(t *testing.T) {
	// Test with a known path — just verify it doesn't panic.
	// With embedded binary, this should use the compile-time constant.
	result := pastaSupportsMapHostLoopback("/nonexistent/pasta")
	if result {
		t.Error("pastaSupportsMapHostLoopback should return false for nonexistent path")
	}
}

func TestWaitForFirstChildPID(t *testing.T) {
	// Spawn a real child process and verify waitForFirstChildPID finds it.
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start child: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	parentPID := syscall.Getpid()

	pid, err := waitForFirstChildPID(parentPID, 2*time.Second)
	if err != nil {
		t.Fatalf("waitForFirstChildPID returned error: %v", err)
	}
	if pid != cmd.Process.Pid {
		t.Errorf("waitForFirstChildPID = %d, want %d", pid, cmd.Process.Pid)
	}
}

func TestWaitForFirstChildPID_Timeout(t *testing.T) {
	// A fresh sleeper with no children of its own; polling its children must time out.
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start child: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	_, err := waitForFirstChildPID(cmd.Process.Pid, 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestWaitForFirstChildPID_NonexistentParent(t *testing.T) {
	// PID 0 is invalid; must return error rather than hang or return 0.
	_, err := waitForFirstChildPID(0, 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected error for invalid parent PID, got nil")
	}
}
