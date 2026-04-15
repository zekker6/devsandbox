package overlay

import (
	"fmt"
	"path/filepath"
	"strings"
)

// SafePath converts an absolute mount destination into the directory name
// used under <sandboxHome>/overlay/.
//
// It strips the leading "/" and replaces remaining "/" with "_". Rejects
// relative paths, empty paths, and paths containing ".." segments.
func SafePath(dest string) (string, error) {
	if dest == "" {
		return "", fmt.Errorf("overlay dest is empty")
	}
	// Check for ".." segments before cleaning, since filepath.Clean resolves them.
	for _, seg := range strings.Split(dest, string(filepath.Separator)) {
		if seg == ".." {
			return "", fmt.Errorf("overlay dest contains path traversal: %s", dest)
		}
	}
	cleanDest := filepath.Clean(dest)
	if !filepath.IsAbs(cleanDest) {
		return "", fmt.Errorf("overlay dest must be absolute path, got: %s", dest)
	}
	return strings.ReplaceAll(strings.TrimPrefix(cleanDest, "/"), "/", "_"), nil
}
