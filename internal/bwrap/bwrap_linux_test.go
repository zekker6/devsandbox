//go:build linux

package bwrap

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestWaitForFirstChildPID(t *testing.T) {
	// Use a dedicated single-threaded parent. A child launched directly by the
	// Go test process may belong to any runtime thread, while the function reads
	// the named parent's main-thread children file.
	cmd := exec.Command("/bin/sh", "-c", `/bin/sleep 5 & child=$!; printf '%s\n' "$child"; wait "$child"`)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("create child PID pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start parent: %v", err)
	}
	t.Cleanup(func() {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Errorf("kill parent process group: %v", err)
		}
		if err := cmd.Wait(); err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Errorf("wait for parent cleanup: %v", err)
			}
		}
	})

	var childPID int
	if fields, err := fmt.Fscan(stdout, &childPID); err != nil {
		t.Fatalf("read child PID: %v", err)
	} else if fields != 1 {
		t.Fatalf("read %d child PID fields, want 1", fields)
	}

	pid, err := waitForFirstChildPID(cmd.Process.Pid, 2*time.Second)
	if err != nil {
		t.Fatalf("waitForFirstChildPID returned error: %v", err)
	}
	if pid != childPID {
		t.Errorf("waitForFirstChildPID = %d, want %d", pid, childPID)
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
