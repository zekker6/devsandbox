package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupSessionOverlays(t *testing.T) {
	tmpDir := t.TempDir()
	sandboxHome := filepath.Join(tmpDir, "home")
	sessionID := "abc12345"

	sessionDir := filepath.Join(sandboxHome, "overlay", "sessions", sessionID)
	if err := os.MkdirAll(filepath.Join(sessionDir, "some_path", "upper"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := CleanupSessionOverlays(sandboxHome, sessionID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Error("session dir should be removed")
	}

	sessionsDir := filepath.Join(sandboxHome, "overlay", "sessions")
	if _, err := os.Stat(sessionsDir); os.IsNotExist(err) {
		t.Error("sessions parent dir should still exist")
	}
}

func TestCleanupSessionOverlays_NonexistentSession(t *testing.T) {
	tmpDir := t.TempDir()
	sandboxHome := filepath.Join(tmpDir, "home")

	if err := CleanupSessionOverlays(sandboxHome, "nonexistent"); err != nil {
		t.Fatalf("unexpected error for non-existent session: %v", err)
	}
}

func TestCleanupStaleSessionDirs(t *testing.T) {
	tmpDir := t.TempDir()
	sandboxHome := filepath.Join(tmpDir, "home")

	sessionsDir := filepath.Join(sandboxHome, "overlay", "sessions")
	for _, id := range []string{"aaa11111", "bbb22222", "ccc33333"} {
		if err := os.MkdirAll(filepath.Join(sessionsDir, id, "some_path", "upper"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := CleanupStaleSessionDirs(sandboxHome)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 3 {
		t.Errorf("expected 3 removed, got %d", removed)
	}

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		t.Fatalf("unexpected error reading sessions dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty sessions dir, got %d entries", len(entries))
	}
}

func TestCleanupStaleSessionDirs_NoSessionsDir(t *testing.T) {
	tmpDir := t.TempDir()
	sandboxHome := filepath.Join(tmpDir, "home")

	removed, err := CleanupStaleSessionDirs(sandboxHome)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 0 {
		t.Errorf("expected 0 removed, got %d", removed)
	}
}
