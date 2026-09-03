// Package logrotate bounds what a single append-only log path can hold on the
// host: once the live file reaches a size limit it is renamed aside and older
// backups beyond the file limit are deleted.
//
// It imports only internal/fsutil so that both internal/notice and
// internal/logging can rotate their logs through one implementation.
package logrotate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"

	"devsandbox/internal/fsutil"
)

const (
	// DefaultMaxSize is the size at which the live log is rotated.
	DefaultMaxSize int64 = 8 << 20
	// DefaultMaxFiles is the number of files kept per log path.
	DefaultMaxFiles = 3
)

// Options tunes a rotation. A zero value selects the defaults.
type Options struct {
	// MaxSize is the size in bytes at which the live log is rotated.
	MaxSize int64
	// MaxFiles is the total number of files kept for the path - the live log
	// plus its backups - so the bytes on disk are bounded by MaxFiles*MaxSize.
	MaxFiles int
}

// Rotate renames path to path.1 when it has reached the size limit, shifting the
// existing backups up and deleting the ones beyond the file limit. It returns
// the number of files deleted. The caller opens its append handle afterwards; a
// path that does not exist, is not a regular file, or is under the limit is left
// alone and reported as zero removals.
func Rotate(path string, opts Options) (int, error) {
	maxSize := opts.MaxSize
	if maxSize <= 0 {
		maxSize = DefaultMaxSize
	}
	maxFiles := opts.MaxFiles
	if maxFiles <= 0 {
		maxFiles = DefaultMaxFiles
	}

	if !overLimit(path, maxSize) {
		return 0, nil
	}

	lock, err := fsutil.AcquireFileLock(path + ".lock")
	if err != nil {
		return 0, fmt.Errorf("lock log %s for rotation: %w", path, err)
	}
	defer func() { _ = lock.Release() }()

	// Re-stat under the lock. Two processes that both saw an oversized log queue
	// on this lock; without the second look the one that waited would shift the
	// freshly emptied log to .1 and the rotated one to .2, losing a backup and
	// leaving the real content one slot further from where it is looked for.
	if !overLimit(path, maxSize) {
		return 0, nil
	}

	backups := maxFiles - 1
	if backups <= 0 {
		if err := os.Remove(path); err != nil {
			return 0, fmt.Errorf("remove log %s: %w", path, err)
		}
		return 1, nil
	}

	deleted := 0
	for i := backups; ; i++ {
		old := backupPath(path, i)
		if err := os.Remove(old); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				break
			}
			return deleted, fmt.Errorf("remove old log %s: %w", old, err)
		}
		deleted++
	}

	for i := backups - 1; i >= 1; i-- {
		from, to := backupPath(path, i), backupPath(path, i+1)
		if err := os.Rename(from, to); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return deleted, fmt.Errorf("shift log %s to %s: %w", from, to, err)
		}
	}

	if err := os.Rename(path, backupPath(path, 1)); err != nil {
		return deleted, fmt.Errorf("rotate log %s: %w", path, err)
	}
	return deleted, nil
}

func backupPath(path string, n int) string {
	return path + "." + strconv.Itoa(n)
}

func overLimit(path string, maxSize int64) bool {
	// Lstat, not Stat: a symlink at the log path is not something to rename
	// aside, and following it would rotate a file the path does not own.
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return info.Size() >= maxSize
}
