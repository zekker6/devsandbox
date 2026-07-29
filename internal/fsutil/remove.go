// Package fsutil holds filesystem helpers shared by packages that cannot
// import one another.
package fsutil

import (
	"io/fs"
	"os"
	"path/filepath"
)

// RemoveAllForce removes path, restoring write permission on every directory it
// encounters first. Removing a directory entry needs write permission on the
// *parent* directory, so a plain os.RemoveAll fails with "permission denied" on
// read-only trees — Go's module cache lays down directories with mode 0555, and
// build and test temporaries can do the same.
//
// If path does not exist, it returns nil, matching os.RemoveAll.
func RemoveAllForce(path string) error {
	// First pass: make every directory writable so unlinkat can remove its
	// children. Walk errors are tolerated here — the subsequent RemoveAll will
	// surface any real failure with full context.
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	return os.RemoveAll(path)
}
