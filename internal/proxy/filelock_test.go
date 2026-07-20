package proxy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileLock_AcquireRelease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.lock")

	lock, err := AcquireFileLock(path)
	if err != nil {
		t.Fatalf("AcquireFileLock failed: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file should exist: %v", err)
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	// The lock file is intentionally left in place after release so the flocked
	// inode stays stable; unlinking it would reopen a split-lock race. It must
	// remain re-acquirable.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file should persist after release: %v", err)
	}

	lock2, err := AcquireFileLock(path)
	if err != nil {
		t.Fatalf("re-acquire after release failed: %v", err)
	}
	if err := lock2.Release(); err != nil {
		t.Fatalf("second Release failed: %v", err)
	}
}

func TestFileLock_Contention(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.lock")

	lock1, err := AcquireFileLock(path)
	if err != nil {
		t.Fatalf("first lock failed: %v", err)
	}
	defer func() { _ = lock1.Release() }()

	_, err = TryFileLock(path)
	if err == nil {
		t.Fatal("second lock should fail")
	}
}

func TestFileLock_LeftoverFileReacquirable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.lock")

	// A lock file left behind by a hard-killed prior holder is never unlinked.
	// The kernel auto-releases the flock when the holder's fd closes on exit, so
	// the leftover file (with stale PID content that is ignored on acquire) must
	// not block a fresh acquisition.
	if err := os.WriteFile(path, []byte("999999999"), 0o600); err != nil {
		t.Fatalf("failed to create leftover lock file: %v", err)
	}

	lock, err := AcquireFileLock(path)
	if err != nil {
		t.Fatalf("should acquire over a leftover lock file: %v", err)
	}
	defer func() { _ = lock.Release() }()
}
