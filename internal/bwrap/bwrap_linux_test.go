//go:build linux

package bwrap

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

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
