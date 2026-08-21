// internal/sandbox/tools/setup_helpers.go
package tools

import (
	"os"
	"path/filepath"

	"devsandbox/internal/fsutil"
)

// SetupConfigWithSuffix copies a config file and appends a suffix.
// Creates destination directory if needed.
// Returns nil without error if source doesn't exist.
func SetupConfigWithSuffix(srcPath, destPath, suffix string) error {
	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		return nil
	}

	// Create destination directory
	destDir := filepath.Dir(destPath)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	// Read original
	original, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}

	// Write with suffix. destPath is inside the sandbox home, which is bound
	// read-write into the sandbox, so the sandbox can replace it with a symlink
	// to a host file between launches; os.WriteFile would follow that link and
	// truncate the target. WriteFileAtomic renames over it instead.
	modified := string(original) + suffix
	return fsutil.WriteFileAtomic(destPath, []byte(modified), 0o644)
}
