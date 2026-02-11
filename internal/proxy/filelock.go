package proxy

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// FileLock provides file-based mutual exclusion using flock.
type FileLock struct {
	path string
	file *os.File
}

// AcquireFileLock acquires an exclusive lock, removing stale locks first.
// Blocks until the lock is acquired.
func AcquireFileLock(path string) (*FileLock, error) {
	if err := removeStaleLock(path); err != nil {
		return nil, err
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("failed to open lock file: %w", err)
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("failed to acquire lock: %w", err)
	}

	_ = file.Truncate(0)
	_, _ = file.WriteString(strconv.Itoa(os.Getpid()))
	_ = file.Sync()

	return &FileLock{path: path, file: file}, nil
}

// TryFileLock attempts to acquire the lock without blocking.
// Returns error if the lock is already held.
func TryFileLock(path string) (*FileLock, error) {
	if err := removeStaleLock(path); err != nil {
		return nil, err
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("failed to open lock file: %w", err)
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock already held: %w", err)
	}

	_ = file.Truncate(0)
	_, _ = file.WriteString(strconv.Itoa(os.Getpid()))
	_ = file.Sync()

	return &FileLock{path: path, file: file}, nil
}

// Release releases the lock and removes the lock file.
func (l *FileLock) Release() error {
	if l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	_ = l.file.Close()
	l.file = nil
	_ = os.Remove(l.path)
	return nil
}

// removeStaleLock removes lock files left by dead processes.
func removeStaleLock(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		// Corrupt lock file, remove it
		_ = os.Remove(path)
		return nil
	}

	// FindProcess never errors on Unix, but we handle it for portability.
	proc, err := os.FindProcess(pid)
	if err != nil {
		_ = os.Remove(path)
		return nil
	}

	err = proc.Signal(syscall.Signal(0))
	if err != nil {
		// Process is dead, remove stale lock
		_ = os.Remove(path)
	}

	return nil
}
