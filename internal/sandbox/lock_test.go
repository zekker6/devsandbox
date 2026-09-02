package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
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

// An abandoned tree is identified by its pid being gone. A pid that answers
// EPERM - one recycled by another user's process - is not provably gone, so
// the tree is kept rather than deleted from under a live process.
func TestListAbandonedStaging_EPERMIsKept(t *testing.T) {
	baseDir := t.TempDir()
	held := filepath.Join(baseDir, stagingDirName,
		fmt.Sprintf("held-a1b2c3d4-%d", pidAnsweringEPERM(t)))
	if err := os.MkdirAll(held, 0o755); err != nil {
		t.Fatal(err)
	}

	abandoned, err := ListAbandonedStaging(baseDir)
	if err != nil {
		t.Fatalf("ListAbandonedStaging failed: %v", err)
	}
	if len(abandoned) != 0 {
		t.Fatalf("got %v, want a tree whose pid answers EPERM to be kept", abandoned)
	}
}

// pidAnsweringEPERM returns pid 1 once it has confirmed that a signal-0 probe
// of it answers EPERM. Gating on the euid alone is not enough: inside a PID
// namespace pid 1 is the sandbox's own init and the probe succeeds, so the
// test would pass without exercising the case. Skipping names the reason.
func pidAnsweringEPERM(t *testing.T) int {
	t.Helper()
	if err := syscall.Kill(1, 0); !errors.Is(err, syscall.EPERM) {
		t.Skipf("pid 1 does not answer EPERM here (kill(1, 0) = %v): root, or own pid namespace", err)
	}
	return 1
}

// The stamp is what the stagingStaleAge backstop reads, and os.Rename leaves
// the moved directory's mtime where it was - so without it a sandbox idle for
// months would read as staged months ago the moment it was staged, and a
// stranded tree would be judged by how idle the sandbox had been rather than
// by how long the removal has been stranded. Exercised through the staging
// half of RemoveSandboxIfIdle, because the whole call deletes the tree it
// would inspect.
func TestRemoveSandboxIfIdle_StampsStagedTreeAtStaging(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "proj-a1b2c3d4")
	if err := os.MkdirAll(filepath.Join(root, "home"), 0o755); err != nil {
		t.Fatal(err)
	}
	months := time.Now().Add(-90 * 24 * time.Hour)
	if err := os.Chtimes(root, months, months); err != nil {
		t.Fatal(err)
	}

	before := time.Now().Truncate(time.Second)
	staged, stampErr, err := stageForRemoval(root, nil)
	if err != nil {
		t.Fatalf("stageForRemoval failed: %v", err)
	}
	if stampErr != nil {
		t.Fatalf("stamping the staged tree failed: %v", stampErr)
	}
	want := filepath.Join(StagingDir(base), fmt.Sprintf("proj-a1b2c3d4-%d", os.Getpid()))
	if staged != want {
		t.Fatalf("staged = %q, want %q", staged, want)
	}

	info, err := os.Stat(staged)
	if err != nil {
		t.Fatalf("stat staged tree: %v", err)
	}
	if info.ModTime().Before(before) {
		t.Errorf("staged tree mtime = %v, want stamped at staging (not before %v)", info.ModTime(), before)
	}
	if _, err := os.Stat(filepath.Join(staged, "home")); err != nil {
		t.Errorf("staged tree lost its contents: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("sandbox root still present after staging: %v", err)
	}
}

// stageTree plants a staged tree under base's staging directory as a
// teardown running as pid would have left it, with a file inside so a
// removal has something to walk, and returns its path.
func stageTree(t *testing.T, base, name string, pid int) string {
	t.Helper()
	dir := filepath.Join(StagingDir(base), fmt.Sprintf("%s-a1b2c3d4-%d", name, pid))
	if err := os.MkdirAll(filepath.Join(dir, "home"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "home", "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// ageStaged backdates the staged tree's stamp so it reads as staged age ago.
func ageStaged(t *testing.T, dir string, age time.Duration) {
	t.Helper()
	then := time.Now().Add(-age)
	if err := os.Chtimes(dir, then, then); err != nil {
		t.Fatalf("Chtimes %s: %v", dir, err)
	}
}

// deadPID is a pid above any pid_max, so no process can hold it.
const deadPID = 4294967

func TestRemoveAbandonedStaging(t *testing.T) {
	base := t.TempDir()
	live := stageTree(t, base, "live", os.Getpid())
	dead := stageTree(t, base, "dead", deadPID)
	// A real sandbox whose name resembles the old staging spelling lives
	// outside the staging directory and is not a candidate at all.
	notStaging := filepath.Join(base, ".removing-demo-33867677")
	if err := os.MkdirAll(notStaging, 0o755); err != nil {
		t.Fatal(err)
	}
	// A regular file inside the staging directory is not a tree to reclaim.
	file := filepath.Join(StagingDir(base), fmt.Sprintf("file-a1b2c3d4-%d", deadPID))
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := RemoveAbandonedStaging(base)
	if err != nil {
		t.Fatalf("RemoveAbandonedStaging failed: %v", err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("tree of a dead teardown survived: stat = %v", err)
	}
	for _, kept := range []string{live, notStaging, file} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s was removed: %v", kept, err)
		}
	}
}

func TestRemoveAbandonedStaging_MissingBaseDir(t *testing.T) {
	n, err := RemoveAbandonedStaging(filepath.Join(t.TempDir(), "absent"))
	if err != nil || n != 0 {
		t.Fatalf("RemoveAbandonedStaging on a missing base dir = (%d, %v), want (0, nil)", n, err)
	}
}

// An empty base would resolve the staging directory against the working
// directory, which is never a root this may act under.
func TestRemoveAbandonedStaging_RefusesEmptyBase(t *testing.T) {
	n, err := RemoveAbandonedStaging("")
	if err == nil {
		t.Fatal("RemoveAbandonedStaging(\"\") returned nil, want a refusal")
	}
	if n != 0 {
		t.Errorf("removed = %d, want 0", n)
	}
}

func TestRemoveAbandonedStaging_ReportsFailedRemoval(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	base := t.TempDir()
	dead := stageTree(t, base, "dead", deadPID)
	// Unlinking the staged directory itself needs write permission on the
	// staging root, which RemoveAllForce restores only inside the tree.
	if err := os.Chmod(StagingDir(base), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(StagingDir(base), 0o755) })

	n, err := RemoveAbandonedStaging(base)
	if err == nil {
		t.Fatal("RemoveAbandonedStaging returned nil for a tree it could not remove")
	}
	if n != 0 {
		t.Errorf("removed = %d, want 0", n)
	}
	if _, err := os.Stat(dead); err != nil {
		t.Errorf("the tree reported as unremovable is gone: %v", err)
	}
}

// A tree whose pid answers EPERM - one recycled by another user's process -
// is kept by the probe, so the age backstop is the only thing that reclaims
// it: kept while staged for less than 30 days, removed once past that.
func TestRemoveAbandonedStaging_UncertainPIDBackstop(t *testing.T) {
	base := t.TempDir()
	held := stageTree(t, base, "held", pidAnsweringEPERM(t))

	ageStaged(t, held, 29*24*time.Hour)
	n, err := RemoveAbandonedStaging(base)
	if err != nil {
		t.Fatalf("RemoveAbandonedStaging failed: %v", err)
	}
	if n != 0 {
		t.Errorf("removed = %d, want 0 for a tree younger than the backstop", n)
	}
	if _, err := os.Stat(held); err != nil {
		t.Fatalf("tree younger than the backstop was removed: %v", err)
	}

	ageStaged(t, held, 31*24*time.Hour)
	n, err = RemoveAbandonedStaging(base)
	if err != nil {
		t.Fatalf("RemoveAbandonedStaging failed: %v", err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1 for a tree past the backstop", n)
	}
	if _, err := os.Stat(held); !os.IsNotExist(err) {
		t.Errorf("tree past the backstop survived: stat = %v", err)
	}
}

// The backstop reads the stamp, not the probe's answer, so it is exercised
// with a pid that is certainly alive too - which is what keeps the rule
// tested where pid 1 does not answer EPERM and the case above skips.
func TestRemoveAbandonedStaging_LivePIDBackstop(t *testing.T) {
	base := t.TempDir()
	young := stageTree(t, base, "young", os.Getpid())
	old := stageTree(t, base, "old", os.Getpid())
	ageStaged(t, young, 29*24*time.Hour)
	ageStaged(t, old, 31*24*time.Hour)

	n, err := RemoveAbandonedStaging(base)
	if err != nil {
		t.Fatalf("RemoveAbandonedStaging failed: %v", err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}
	if _, err := os.Stat(young); err != nil {
		t.Errorf("tree younger than the backstop was removed: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("tree past the backstop survived: stat = %v", err)
	}
}
