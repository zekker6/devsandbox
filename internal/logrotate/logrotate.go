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
}

// Rotate renames path to path.1 when it has reached the size limit, shifting the
// existing backups up and deleting the ones beyond DefaultMaxFiles. It returns
// the number of files deleted. The caller opens its append handle afterwards; a
// path that does not exist, is not a regular file, or is under the limit is left
// alone and reported as zero removals.
func Rotate(path string, opts Options) (int, error) {
	maxSize := opts.MaxSize
	if maxSize <= 0 {
		maxSize = DefaultMaxSize
	}

	if !overLimit(path, maxSize) {
		return 0, nil
	}

	// A test interleaves a competing rotation here, between the unlocked size
	// check and the lock, so the re-check below is exercised deterministically
	// rather than by winning a race. A no-op in production.
	afterSizeCheck()

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

	// DefaultMaxFiles is at least 2, so there is always at least one backup
	// slot: a file limit that leaves none would make "rotation" mean deleting
	// the live log, which no caller wants and nothing here offers.
	backups := DefaultMaxFiles - 1

	deleted := 0
	for i := backups; ; i++ {
		old := BackupPath(path, i)
		if err := os.Remove(old); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				break
			}
			return deleted, fmt.Errorf("remove old log %s: %w", old, err)
		}
		deleted++
	}

	for i := backups - 1; i >= 1; i-- {
		from, to := BackupPath(path, i), BackupPath(path, i+1)
		if err := os.Rename(from, to); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return deleted, fmt.Errorf("shift log %s to %s: %w", from, to, err)
		}
	}

	if err := os.Rename(path, BackupPath(path, 1)); err != nil {
		return deleted, fmt.Errorf("rotate log %s: %w", path, err)
	}
	return deleted, nil
}

// afterSizeCheck runs between the unlocked size check and the lock
// acquisition. Only a test replaces it.
var afterSizeCheck = func() {}

// BackupPath names the nth backup of path, counting from 1.
//
// Exported because a reader that reports what a log occupies has to look
// where the rotation put the bytes: right after a rotation the live path does
// not exist and every byte is in path.1, so reclaim.Usage stats the backups
// beside the live file rather than inventing the naming a second time.
func BackupPath(path string, n int) string {
	return path + "." + strconv.Itoa(n)
}

// BackupPaths names every backup slot of path, oldest first, whether or not
// the file is there.
//
// A reader of a rotated log has to look where the rotation put the bytes, the
// same reason BackupPath is exported: the live path holds only what was
// written since the last rotation, and right after one it holds nothing at
// all. Oldest first, so a caller that reads each file in turn keeps the log in
// the order it was written.
func BackupPaths(path string) []string {
	paths := make([]string, 0, DefaultMaxFiles-1)
	for i := DefaultMaxFiles - 1; i >= 1; i-- {
		paths = append(paths, BackupPath(path, i))
	}
	return paths
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

// ReopenIfRotated returns a handle on path when f no longer names the file
// path currently names, and f itself otherwise.
//
// A process that holds a log open while another process rotates it keeps
// appending to the renamed inode: its lines land in the backup, which grows
// unbounded because only the live path is ever size-checked. Every appender
// therefore looks before it writes and migrates to the new inode.
//
// A failed stat or a failed open returns f unchanged, so the caller keeps a
// working handle and never drops the write - the same file the previous
// behavior would have used.
func ReopenIfRotated(f *os.File, path string, perm os.FileMode) *os.File {
	if f == nil || path == "" {
		return f
	}
	onDisk, err := os.Stat(path)
	fromFD, ferr := f.Stat()
	if err == nil && ferr == nil && os.SameFile(onDisk, fromFD) {
		return f
	}
	reopened, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, perm)
	if err != nil {
		return f
	}
	_ = f.Close()
	return reopened
}
