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

func TestCleanupLegacyReadonlyBindOverlays_RemovesBothDirs(t *testing.T) {
	tmpDir := t.TempDir()
	sandboxHome := filepath.Join(tmpDir, "home")
	homeDir := "/home/test"

	// Seed the two legacy overlay dirs that previously existed when these
	// paths were persistent overlays. Include a file inside to simulate a
	// stale shadow entry (the exact scenario that broke claude execution).
	legacyBinDir := filepath.Join(sandboxHome, "overlay", "home_test_.local_bin", "upper")
	legacyClaudeDir := filepath.Join(sandboxHome, "overlay", "home_test_.local_share_claude", "upper", "versions")
	for _, d := range []string{legacyBinDir, legacyClaudeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(legacyClaudeDir, "2.1.96"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	removed, err := CleanupLegacyReadonlyBindOverlays(sandboxHome, homeDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 2 {
		t.Errorf("expected 2 removed, got %d", removed)
	}

	binParent := filepath.Join(sandboxHome, "overlay", "home_test_.local_bin")
	claudeParent := filepath.Join(sandboxHome, "overlay", "home_test_.local_share_claude")
	for _, p := range []string{binParent, claudeParent} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("legacy overlay dir should be removed: %s", p)
		}
	}
}

func TestCleanupLegacyReadonlyBindOverlays_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	sandboxHome := filepath.Join(tmpDir, "home")
	if err := os.MkdirAll(filepath.Join(sandboxHome, "overlay"), 0o755); err != nil {
		t.Fatal(err)
	}

	// First call: nothing to remove.
	removed, err := CleanupLegacyReadonlyBindOverlays(sandboxHome, "/home/test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 0 {
		t.Errorf("expected 0 removed on empty sandbox, got %d", removed)
	}

	// Second call on entirely missing sandbox home must also succeed.
	removed, err = CleanupLegacyReadonlyBindOverlays(filepath.Join(tmpDir, "does-not-exist"), "/home/test")
	if err != nil {
		t.Fatalf("unexpected error for missing sandbox home: %v", err)
	}
	if removed != 0 {
		t.Errorf("expected 0 removed for missing sandbox home, got %d", removed)
	}
}

func TestCleanupLegacyReadonlyBindOverlays_LeavesOtherOverlaysAlone(t *testing.T) {
	tmpDir := t.TempDir()
	sandboxHome := filepath.Join(tmpDir, "home")
	homeDir := "/home/test"

	// Legacy dirs we intend to remove.
	legacyBin := filepath.Join(sandboxHome, "overlay", "home_test_.local_bin", "upper")
	// An unrelated persistent overlay dir that must be preserved.
	unrelated := filepath.Join(sandboxHome, "overlay", "home_test_.cache_mise", "upper")
	for _, d := range []string{legacyBin, unrelated} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := CleanupLegacyReadonlyBindOverlays(sandboxHome, homeDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(sandboxHome, "overlay", "home_test_.local_bin")); !os.IsNotExist(err) {
		t.Error("legacy ~/.local/bin overlay should be removed")
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Errorf("unrelated overlay dir should be preserved: %v", err)
	}
}
