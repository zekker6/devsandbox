package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// createOverlayDirs creates upper and work directories for an overlay mount.
// Returns the paths to upper and work directories.
// subdir is an optional subdirectory under "overlay/" (e.g., "custom").
// sessionID, when non-empty, routes the overlay under "overlay/sessions/<sessionID>/[subdir/]<safePath>"
// instead of the default "overlay/[subdir/]<safePath>".
func createOverlayDirs(sandboxHome, dest, subdir, sessionID string) (upper, work string, err error) {
	cleanDest := filepath.Clean(dest)

	if !filepath.IsAbs(cleanDest) {
		return "", "", fmt.Errorf("overlay dest must be absolute path, got: %s", dest)
	}
	// After filepath.Clean on an absolute path, ".." as a path segment should be
	// impossible. Check anyway as defense-in-depth, but only match ".." as an actual
	// path component — not as a substring of filenames like "..cache".
	for seg := range strings.SplitSeq(cleanDest, string(filepath.Separator)) {
		if seg == ".." {
			return "", "", fmt.Errorf("overlay dest contains path traversal: %s", dest)
		}
	}

	// Convert dest path to safe directory name
	safePath := strings.ReplaceAll(strings.TrimPrefix(cleanDest, "/"), "/", "_")

	// Build overlay directory path
	var overlayDir string
	if sessionID != "" {
		if subdir != "" {
			overlayDir = filepath.Join(sandboxHome, "overlay", "sessions", sessionID, subdir, safePath)
		} else {
			overlayDir = filepath.Join(sandboxHome, "overlay", "sessions", sessionID, safePath)
		}
	} else if subdir != "" {
		overlayDir = filepath.Join(sandboxHome, "overlay", subdir, safePath)
	} else {
		overlayDir = filepath.Join(sandboxHome, "overlay", safePath)
	}

	upper = filepath.Join(overlayDir, "upper")
	work = filepath.Join(overlayDir, "work")

	if err := os.MkdirAll(upper, 0o755); err != nil {
		return "", "", fmt.Errorf("failed to create overlay upper dir: %w", err)
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", "", fmt.Errorf("failed to create overlay work dir: %w", err)
	}

	return upper, work, nil
}

// persistentOverlayUpperDir returns the path to the primary session's persistent
// upper directory for a given overlay destination. Does not create any directories.
func persistentOverlayUpperDir(sandboxHome, dest, subdir string) string {
	cleanDest := filepath.Clean(dest)
	safePath := strings.ReplaceAll(strings.TrimPrefix(cleanDest, "/"), "/", "_")

	if subdir != "" {
		return filepath.Join(sandboxHome, "overlay", subdir, safePath, "upper")
	}
	return filepath.Join(sandboxHome, "overlay", safePath, "upper")
}
