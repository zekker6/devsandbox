package fsutil

import (
	"errors"
	"io/fs"
	"path/filepath"
	"time"
)

// ModifiedSince reports whether path, or anything beneath it, changed after
// cutoff.
//
// The whole subtree is judged, not the directory alone: a directory's own
// mtime moves only when a direct child is added or removed, so a tenant
// writing deep inside a tree it created days ago looks stale to a shallow
// check.
//
// A walk error answers "modified". An entry that cannot be read has an unknown
// age, and unknown must never authorize a deletion. An entry that vanished
// mid-walk is the exception: whoever removed it did the caller's job.
func ModifiedSince(path string, cutoff time.Time) bool {
	recent := false
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			recent = true
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			recent = true
			return filepath.SkipAll
		}
		if info.ModTime().After(cutoff) {
			recent = true
			return filepath.SkipAll
		}
		return nil
	})
	return recent || err != nil
}
