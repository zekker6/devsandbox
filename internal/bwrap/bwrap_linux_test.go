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

// The first child listed stays in the host netns, as pasta's user namespace
// holder does; the PID returned must be the later child that has its own netns.
func TestWaitForSandboxChildPID(t *testing.T) {
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skipf("unshare not available: %v", err)
	}
	if out, err := exec.Command(unshare, "-Urn", "true").CombinedOutput(); err != nil {
		t.Skipf("cannot create an unprivileged user+net namespace here: %v: %s", err, out)
	}

	// Use a dedicated single-threaded parent. A child launched directly by the
	// Go test process may belong to any runtime thread, while the function reads
	// the named parent's main-thread children file.
	cmd := exec.Command("/bin/sh", "-c",
		`/bin/sleep 5 & "$1" -Urn /bin/sleep 5 & child=$!; printf '%s\n' "$child"; wait "$child"`, "_", unshare)
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

	pid, err := waitForSandboxChildPID(cmd.Process.Pid, 2*time.Second)
	if err != nil {
		t.Fatalf("waitForSandboxChildPID returned error: %v", err)
	}
	if pid != childPID {
		t.Errorf("waitForSandboxChildPID = %d, want %d", pid, childPID)
	}
}

func TestWaitForSandboxChildPID_Timeout(t *testing.T) {
	// A fresh sleeper with no children of its own; polling its children must time out.
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start child: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	_, err := waitForSandboxChildPID(cmd.Process.Pid, 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestWaitForSandboxChildPID_NonexistentParent(t *testing.T) {
	// PID 0 is invalid; must return error rather than hang or return 0.
	_, err := waitForSandboxChildPID(0, 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected error for invalid parent PID, got nil")
	}
}
