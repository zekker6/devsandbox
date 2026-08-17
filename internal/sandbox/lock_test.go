package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireSession_CreatesLockFile(t *testing.T) {
	tmpDir := t.TempDir()

	lockFile, err := AcquireSession(tmpDir)
	if err != nil {
		t.Fatalf("AcquireSession failed: %v", err)
	}
	defer func() { _ = lockFile.Release() }()

	// Verify lock file was created
	lockPath := filepath.Join(tmpDir, LockFileName)
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Error("Lock file was not created")
	}
}

func TestIsSessionActive_NoLock(t *testing.T) {
	tmpDir := t.TempDir()

	if IsSessionActive(tmpDir) {
		t.Error("Expected no active session for new directory")
	}
}

func TestIsSessionActive_WithLock(t *testing.T) {
	tmpDir := t.TempDir()

	// Acquire lock
	lockFile, err := AcquireSession(tmpDir)
	if err != nil {
		t.Fatalf("AcquireSession failed: %v", err)
	}

	// Check if active
	if !IsSessionActive(tmpDir) {
		t.Error("Expected session to be active while lock is held")
	}

	// Release lock
	_ = lockFile.Release()

	// Check again - should not be active
	if IsSessionActive(tmpDir) {
		t.Error("Expected no active session after lock released")
	}
}

func TestAcquireSession_MultipleSessionsShareTheLock(t *testing.T) {
	tmpDir := t.TempDir()

	// First session
	lock1, err := AcquireSession(tmpDir)
	if err != nil {
		t.Fatalf("First lock failed: %v", err)
	}
	defer func() { _ = lock1.Release() }()

	// Second session (should succeed - shared locks)
	lock2, err := AcquireSession(tmpDir)
	if err != nil {
		t.Fatalf("Second lock failed: %v", err)
	}
	defer func() { _ = lock2.Release() }()

	// Both should show as active
	if !IsSessionActive(tmpDir) {
		t.Error("Expected session to be active with two locks held")
	}

	// Close first lock
	_ = lock1.Release()

	// Should still be active (second lock held)
	if !IsSessionActive(tmpDir) {
		t.Error("Expected session to be active with one lock still held")
	}
}

// stagedPID reads the pid a teardown appended. It is only ever applied to
// entries inside the staging directory, so it infers nothing about the sandbox
// name in front of the pid - which is the whole reason staging is a directory
// rather than a sibling name prefix.
func TestStagedPID(t *testing.T) {
	tests := []struct {
		name    string
		wantPID int
		wantOK  bool
	}{
		{"proj-a1b2c3d4-4242", 4242, true},
		{"sandbox-999", 999, true},
		{"my-proj-7", 7, true},
		{"proj-a1b2c3d4-", 0, false},
		{"proj-a1b2c3d4-abc", 0, false},
		{"proj-0", 0, false},
		{"-4242", 0, false},
		{"proj", 0, false},
		{"", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pid, ok := stagedPID(tc.name)
			if ok != tc.wantOK || pid != tc.wantPID {
				t.Fatalf("stagedPID(%q) = (%d, %v), want (%d, %v)",
					tc.name, pid, ok, tc.wantPID, tc.wantOK)
			}
		})
	}
}

// A sandbox whose project basename begins with the staging directory's name
// must stay visible. The old sibling-prefix scheme hid it, and the trailing-pid
// variant of that scheme went further and had prune delete it: a sandbox
// directory is <basename>-<8 hex>, and an all-digit hash reads as a pid far
// above any pid_max, so it looked like a teardown that had died.
func TestListSandboxes_KeepsNamesResemblingStaging(t *testing.T) {
	baseDir := t.TempDir()
	for _, name := range []string{".removing-demo-33867677", ".removing-proj-a1b2c3d4", stagingDirName} {
		if err := os.MkdirAll(filepath.Join(baseDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	sandboxes, err := ListSandboxes(baseDir)
	if err != nil {
		t.Fatalf("ListSandboxes failed: %v", err)
	}
	var got []string
	for _, s := range sandboxes {
		got = append(got, filepath.Base(s.SandboxRoot))
	}
	want := []string{".removing-demo-33867677", ".removing-proj-a1b2c3d4"}
	if len(got) != len(want) {
		t.Fatalf("ListSandboxes() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListSandboxes() = %v, want %v", got, want)
		}
	}
}

// A staging tree is reclaimable only once the teardown that made it is gone;
// one still running owns its own tree and must be left alone.
func TestListAbandonedStaging(t *testing.T) {
	baseDir := t.TempDir()
	stagingRoot := filepath.Join(baseDir, stagingDirName)
	live := filepath.Join(stagingRoot, fmt.Sprintf("live-a1b2c3d4-%d", os.Getpid()))
	dead := filepath.Join(stagingRoot, "dead-a1b2c3d4-4294967")
	// A real sandbox whose name resembles the old staging spelling. It lives
	// outside the staging directory, so it is not a candidate at all.
	notStaging := filepath.Join(baseDir, ".removing-demo-33867677")
	for _, dir := range []string{live, dead, notStaging} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A regular file inside the staging directory is not a tree to reclaim.
	if err := os.WriteFile(filepath.Join(stagingRoot, "file-a1b2c3d4-4294968"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	abandoned, err := ListAbandonedStaging(baseDir)
	if err != nil {
		t.Fatalf("ListAbandonedStaging failed: %v", err)
	}
	if len(abandoned) != 1 || abandoned[0] != dead {
		t.Fatalf("got %v, want only %q", abandoned, dead)
	}
}

func TestListAbandonedStaging_MissingBaseDir(t *testing.T) {
	abandoned, err := ListAbandonedStaging(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("ListAbandonedStaging on a missing base dir failed: %v", err)
	}
	if len(abandoned) != 0 {
		t.Fatalf("got %v, want none", abandoned)
	}
}

// The shared-lock wait exists to outlast a --rm teardown, and beforeRemove -
// the worktree removal - runs under that lock capped at TeardownGracePeriod. A
// budget shorter than the thing it must wait out aborts the very launch the
// retry was added for, which is how a 2s budget met a 30s teardown.
func TestSharedLockBudgetOutlastsTeardown(t *testing.T) {
	budget := time.Duration(sharedLockRetries-1) * sharedLockRetryDelay
	if budget < TeardownGracePeriod {
		t.Errorf("shared-lock budget %v is shorter than TeardownGracePeriod %v",
			budget, TeardownGracePeriod)
	}
}

// sameFileAt is what stops a launch holding a lock on a sandbox that has been
// renamed aside: the flock succeeds on the staged inode, and only the identity
// check tells that apart from the live one.
func TestSameFileAt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	if !sameFileAt(path, f) {
		t.Fatal("sameFileAt = false for a descriptor still at its path")
	}

	staged := filepath.Join(dir, ".lock.staged")
	if err := os.Rename(path, staged); err != nil {
		t.Fatal(err)
	}
	if sameFileAt(path, f) {
		t.Error("sameFileAt = true after the path was renamed away")
	}

	// A fresh inode at the old path is the case the launch must not accept:
	// the descriptor is flocked, but it no longer guards the sandbox.
	replacement, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replacement.Close() }()
	if sameFileAt(path, f) {
		t.Error("sameFileAt = true when a different inode holds the path")
	}
	if !sameFileAt(path, replacement) {
		t.Error("sameFileAt = false for the descriptor that does hold the path")
	}
}
