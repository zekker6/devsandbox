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

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("lock file should be removed after release")
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

func TestFileLock_StaleDetection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.lock")

	// Create a lock file with a non-existent PID
	if err := os.WriteFile(path, []byte("999999999"), 0o600); err != nil {
		t.Fatalf("failed to create stale lock: %v", err)
	}

	lock, err := AcquireFileLock(path)
	if err != nil {
		t.Fatalf("should acquire stale lock: %v", err)
	}
	defer func() { _ = lock.Release() }()
}
