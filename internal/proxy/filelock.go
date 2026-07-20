package proxy

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
)

// FileLock provides file-based mutual exclusion using flock.
//
// The lock file is created once and intentionally never unlinked: flock is keyed
// on the inode, so a stable, never-removed path is the source of truth, and the
// kernel auto-releases the lock when its holder exits (even on SIGKILL) - there is
// no stale lock file to clean up. Unlinking on release would reopen a split-lock
// race: a second process can acquire the flock on the old inode after the holder
// unlocks but before it unlinks, the holder's unlink then removes a path that a
// third process recreates and flocks independently, and two holders run at once.
// The PID written into the file is purely diagnostic.
type FileLock struct {
	path string
	file *os.File
}

// AcquireFileLock acquires an exclusive lock, blocking until it is acquired.
func AcquireFileLock(path string) (*FileLock, error) {
	return acquireFileLock(path, syscall.LOCK_EX)
}

// TryFileLock attempts to acquire the lock without blocking.
// Returns an error if the lock is already held.
func TryFileLock(path string) (*FileLock, error) {
	return acquireFileLock(path, syscall.LOCK_EX|syscall.LOCK_NB)
}

func acquireFileLock(path string, how int) (*FileLock, error) {
	// O_NOFOLLOW: the lock lives at a predictable path in the shared temp dir, so a
	// co-tenant could pre-create a symlink there pointing at a file they want the
	// (possibly privileged) lock holder to truncate/write. O_NOFOLLOW makes the
	// open fail (ELOOP) on a final-component symlink rather than follow it; a
	// regular lock file is unaffected.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("failed to open lock file: %w", err)
	}

	if err := syscall.Flock(int(file.Fd()), how); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("failed to acquire lock: %w", err)
	}

	// Record the holder PID for diagnostics. flock - not this content - enforces
	// the exclusion, so a leftover PID from a crashed prior holder is harmless.
	_ = file.Truncate(0)
	_, _ = file.WriteString(strconv.Itoa(os.Getpid()))
	_ = file.Sync()

	return &FileLock{path: path, file: file}, nil
}

// Release releases the lock. The lock file is deliberately left in place so the
// flocked inode stays stable for the next holder; see the FileLock doc comment.
func (l *FileLock) Release() error {
	if l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	err := l.file.Close()
	l.file = nil
	return err
}
