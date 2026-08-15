package sandbox

import (
	"os"
	"path/filepath"
	"testing"
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
