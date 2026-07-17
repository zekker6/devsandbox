package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// reapedPID returns the PID of a process that has exited and been waited for,
// so the kernel no longer knows it.
func reapedPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait for helper process: %v", err)
	}
	return pid
}

func TestRunDir_IsScopedToProcess(t *testing.T) {
	got := runDir("/sandbox/home")
	want := filepath.Join("/sandbox/home", runDirName, strconv.Itoa(os.Getpid()))
	if got != want {
		t.Errorf("runDir = %q, want %q", got, want)
	}
}

func TestEnsureRunDir(t *testing.T) {
	sandboxHome := t.TempDir()

	dir, err := ensureRunDir(sandboxHome)
	if err != nil {
		t.Fatalf("ensureRunDir failed: %v", err)
	}
	if dir != runDir(sandboxHome) {
		t.Errorf("ensureRunDir = %q, want %q", dir, runDir(sandboxHome))
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("run dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected run dir to be a directory")
	}

	// Must be idempotent — docker and kitty both call it.
	if _, err := ensureRunDir(sandboxHome); err != nil {
		t.Errorf("second ensureRunDir failed: %v", err)
	}
}

func TestCleanupStaleRunDirs(t *testing.T) {
	sandboxHome := t.TempDir()
	root := filepath.Join(sandboxHome, runDirName)

	dead := strconv.Itoa(reapedPID(t))
	live := strconv.Itoa(os.Getppid())
	// Our own dir can only be a leftover from an earlier run with this PID:
	// cleanup runs before any tool creates sockets.
	own := strconv.Itoa(os.Getpid())

	survives := map[string]bool{
		dead:        false,
		live:        true,
		own:         false,
		"not-a-pid": true,
	}
	for name := range survives {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// A stray file in the root must not derail the sweep.
	if err := os.WriteFile(filepath.Join(root, "stray"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	removed, err := cleanupStaleRunDirs(sandboxHome)
	if err != nil {
		t.Fatalf("cleanupStaleRunDirs failed: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}

	for name, want := range survives {
		_, err := os.Stat(filepath.Join(root, name))
		if got := err == nil; got != want {
			t.Errorf("dir %q survived = %v, want %v", name, got, want)
		}
	}
}

func TestCleanupStaleRunDirs_NoRunDir(t *testing.T) {
	removed, err := cleanupStaleRunDirs(t.TempDir())
	if err != nil {
		t.Errorf("expected no error for missing run dir, got %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
}

func TestCheckSocketPath(t *testing.T) {
	if err := checkSocketPath("/tmp/short.sock"); err != nil {
		t.Errorf("expected a short path to pass, got %v", err)
	}

	long := "/tmp/" + strings.Repeat("a", maxUnixSocketPath()) + ".sock"
	err := checkSocketPath(long)
	if err == nil {
		t.Fatal("expected an over-long socket path to be rejected")
	}
	// The message has to name the cause; bind(2) only says "invalid argument".
	if !strings.Contains(err.Error(), "unix socket limit") {
		t.Errorf("expected the error to explain the limit, got %v", err)
	}

	exact := "/" + strings.Repeat("a", maxUnixSocketPath()-1)
	if err := checkSocketPath(exact); err != nil {
		t.Errorf("expected a path at exactly the limit to pass, got %v", err)
	}
}

func TestProcessAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Error("expected the test process itself to be alive")
	}
	if processAlive(reapedPID(t)) {
		t.Error("expected a reaped process to be reported dead")
	}
	// PID 1 always exists and is owned by root, so the signal-0 probe answers
	// EPERM for an unprivileged test run. That must not read as dead.
	if !processAlive(1) {
		t.Error("expected PID 1 to be reported alive")
	}
}
