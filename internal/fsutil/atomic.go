package fsutil

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to path so that no reader ever observes a partial
// file. It writes a temporary file in the same directory, flushes it to disk, and
// renames it over path - a rename within one directory is atomic, so path either
// still holds the previous content or holds all of the new content.
//
// This matters wherever a file is written while something else may read it, which
// is every state file devsandbox keeps: a torn write leaves unparseable JSON, and
// the readers treat that as absence rather than as an error - a half-written
// sandbox metadata file reads as an orphaned sandbox, which is what `prune`
// removes by default.
//
// It is not a crash-durability guarantee. The data is fsynced but the parent
// directory is not, so a power loss immediately after the rename can leave path
// with its old content. Every caller writes state that is rebuilt on the next run,
// and claiming a guarantee that is not enforced is worse than documenting the
// narrower one.
//
// The temporary file is removed on every failure path, so a failed write leaves
// nothing behind for a listing to trip over.
func WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	dir, name := filepath.Split(path)
	if dir == "" {
		dir = "."
	}

	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tmpPath := tmp.Name()

	if err := writeAndSync(tmp, data, perm); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp file for %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("commit %s: %w", path, err)
	}
	return nil
}

// writeAndSync sets the mode, writes the content and flushes it.
//
// The mode is set on the descriptor rather than left to CreateTemp's 0600: the
// temporary file becomes the real one, so it has to carry the caller's permissions
// before the rename, not after it.
func writeAndSync(f *os.File, data []byte, perm fs.FileMode) error {
	if err := f.Chmod(perm); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}
