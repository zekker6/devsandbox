package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
)

// CleanupSessionOverlays removes the overlay directories for a specific session.
//
// Session overlays may contain read-only directories (e.g. Go module caches
// created by tooling running inside the sandbox), so removal must use
// forceRemoveAll rather than a plain os.RemoveAll.
func CleanupSessionOverlays(sandboxHome, sessionID string) error {
	sessionDir := filepath.Join(sandboxHome, "overlay", "sessions", sessionID)
	if err := forceRemoveAll(sessionDir); err != nil {
		return fmt.Errorf("failed to remove session overlay dir: %w", err)
	}
	return nil
}

// CleanupStaleSessionDirs removes all session overlay directories.
// Called by the primary session on startup when no other sessions are active.
// Returns the number of session dirs removed.
func CleanupStaleSessionDirs(sandboxHome string) (int, error) {
	sessionsDir := filepath.Join(sandboxHome, "overlay", "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to read sessions dir: %w", err)
	}

	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionDir := filepath.Join(sessionsDir, entry.Name())
		if err := forceRemoveAll(sessionDir); err != nil {
			return removed, fmt.Errorf("failed to remove stale session %s: %w", entry.Name(), err)
		}
		removed++
	}
	return removed, nil
}
