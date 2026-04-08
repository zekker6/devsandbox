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

// legacyReadonlyBindOverlayDests lists paths that used to be persistent overlays
// but are now mounted as read-only bind mounts. Their per-project overlay
// upper-dirs can contain stale shadow files (e.g. 0-byte copies of a tool's
// binary from a failed in-sandbox self-update) that override the real host
// content even after the code switched to a bind mount. Values are joined
// with the caller-supplied homeDir at cleanup time.
var legacyReadonlyBindOverlayDests = []string{
	".local/bin",
	".local/share/claude",
}

// CleanupLegacyReadonlyBindOverlays removes persistent overlay upper-dirs for
// paths that were migrated from persistent overlays to read-only bind mounts.
// Safe to call on every primary-session startup — it is a no-op once the
// legacy dirs are gone. Missing sandboxHome is not an error. Returns the
// number of legacy dirs removed.
func CleanupLegacyReadonlyBindOverlays(sandboxHome, homeDir string) (int, error) {
	removed := 0
	for _, rel := range legacyReadonlyBindOverlayDests {
		dest := filepath.Join(homeDir, rel)
		// Derive the overlay dir path from persistentOverlayUpperDir so both
		// the cleanup and the mount code use the same safePath encoding.
		overlayDir := filepath.Dir(persistentOverlayUpperDir(sandboxHome, dest, ""))
		info, err := os.Stat(overlayDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, fmt.Errorf("stat legacy overlay %s: %w", overlayDir, err)
		}
		if !info.IsDir() {
			continue
		}
		if err := forceRemoveAll(overlayDir); err != nil {
			return removed, fmt.Errorf("remove legacy overlay %s: %w", overlayDir, err)
		}
		removed++
	}
	return removed, nil
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
