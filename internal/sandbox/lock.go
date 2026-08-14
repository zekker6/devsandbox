package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// LockFileName is the name of the session lock file within a sandbox directory.
const LockFileName = ".lock"

// PrimaryLockFileName is the name of the lock that designates the primary
// session. Every launch holds LockFileName shared for its lifetime; exactly one
// of them can hold this one exclusively, which is what makes the designation
// atomic across processes.
const PrimaryLockFileName = ".primary.lock"

// ErrSandboxBusy reports that the sandbox state is held exclusively by another
// devsandbox process - a --rm teardown removing it, or a command probing
// whether it is idle.
var ErrSandboxBusy = errors.New("sandbox state is held by another devsandbox process")

// SessionHandle is one launch's hold on a sandbox: the shared lock every
// session takes, plus the exclusive primary lock for the launch that won the
// designation. The handle must stay open for the session's lifetime; the kernel
// drops both locks if the process dies.
type SessionHandle struct {
	mu       sync.Mutex
	shared   *os.File
	primary  *os.File
	released bool
}

// AcquireSession takes this launch's hold on the sandbox and reports through
// IsPrimary whether it owns the sandbox's persistent state.
//
// Both locks are taken here, before anything samples whether another session
// exists, and that ordering is the point: probing IsSessionActive first and
// locking later is a check-then-act race in which two launches started together
// both conclude they are alone and then both write the same persistent overlay
// upper and work directories. The exclusive flock designates exactly one
// primary however the launches interleave.
func AcquireSession(sandboxRoot string) (*SessionHandle, error) {
	shared, err := AcquireSessionLock(sandboxRoot)
	if err != nil {
		return nil, err
	}

	primary, err := acquireExclusiveLock(filepath.Join(sandboxRoot, PrimaryLockFileName))
	if err != nil && !errors.Is(err, ErrSandboxBusy) {
		_ = shared.Close()
		return nil, err
	}

	return &SessionHandle{shared: shared, primary: primary}, nil
}

// IsPrimary reports whether this launch owns the sandbox's persistent state. A
// launch that lost the designation writes to session-scoped overlay dirs and
// must not remove the sandbox state on exit. A released handle is not primary -
// the designation is available to another launch the moment it is dropped.
func (h *SessionHandle) IsPrimary() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.released && h.primary != nil
}

// Release drops both locks. Safe to call more than once: the --rm teardown
// releases the handle explicitly before probing whether any other session is
// still live, and the launch's own deferred release then finds nothing to do.
func (h *SessionHandle) Release() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.released {
		return nil
	}
	h.released = true

	var errs []error
	if h.primary != nil {
		if err := h.primary.Close(); err != nil {
			errs = append(errs, fmt.Errorf("release primary lock: %w", err))
		}
		h.primary = nil
	}
	if h.shared != nil {
		if err := h.shared.Close(); err != nil {
			errs = append(errs, fmt.Errorf("release session lock: %w", err))
		}
		h.shared = nil
	}
	return errors.Join(errs...)
}

// DesignateSession acquires this launch's session hold and applies the
// resulting role to the config: a launch that loses the primary designation
// gets a session ID and so is routed onto session-scoped overlay dirs, leaving
// the primary's persistent upper and work directories to the primary alone.
//
// Only bwrap participates in the rerouting. Docker and krun sandboxes take
// their isolation from the container/microVM rather than from the overlay
// layout, so a second launch there keeps the persistent overlay it has always
// used. Both backends still take the hold, which is what makes the --rm
// teardown able to tell whether anyone else is live.
func (c *Config) DesignateSession() (*SessionHandle, error) {
	handle, err := AcquireSession(c.SandboxRoot)
	if err != nil {
		return nil, err
	}
	if handle.IsPrimary() || c.Isolation != IsolationBwrap {
		return handle, nil
	}

	sessionID, err := GenerateSessionID()
	if err != nil {
		_ = handle.Release()
		return nil, err
	}
	c.SessionID = sessionID
	c.IsConcurrent = true
	return handle, nil
}

// RemoveSandboxIfIdle removes the sandbox state only while no session holds it,
// and reports whether it removed anything. The exclusive lock is held across the
// removal so a launch cannot take a hold on state that is halfway gone.
//
// The caller must release its own handle first: its own shared lock is
// indistinguishable from another session's here.
func RemoveSandboxIfIdle(sandboxRoot string) (bool, error) {
	if _, err := os.Stat(sandboxRoot); err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("failed to stat sandbox root: %w", err)
	}

	lock, err := acquireExclusiveLock(filepath.Join(sandboxRoot, LockFileName))
	if err != nil {
		if errors.Is(err, ErrSandboxBusy) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = lock.Close() }()

	if err := RemoveSandbox(sandboxRoot); err != nil {
		return false, err
	}
	return true, nil
}

// AcquireSessionLock acquires a shared lock on the sandbox.
// The caller must keep the returned file open for the session duration.
// The lock is automatically released when the file is closed or process exits.
func AcquireSessionLock(sandboxRoot string) (*os.File, error) {
	lockPath := filepath.Join(sandboxRoot, LockFileName)

	// Create or open the lock file
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open lock file: %w", err)
	}

	// Acquire shared lock (non-blocking)
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrSandboxBusy, sandboxRoot)
		}
		return nil, fmt.Errorf("failed to acquire lock: %w", err)
	}

	return f, nil
}

// acquireExclusiveLock opens path and takes a non-blocking exclusive flock on
// it, reporting ErrSandboxBusy when another holder has it.
func acquireExclusiveLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open lock file: %w", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrSandboxBusy, path)
		}
		return nil, fmt.Errorf("failed to acquire lock: %w", err)
	}

	return f, nil
}

// IsSessionActive checks if any session holds a lock on the sandbox.
// Returns true if a session is active (lock is held).
func IsSessionActive(sandboxRoot string) bool {
	lockPath := filepath.Join(sandboxRoot, LockFileName)

	// Try to open the lock file
	f, err := os.OpenFile(lockPath, os.O_RDWR, 0o644)
	if err != nil {
		// File doesn't exist or can't be opened - no active session
		return false
	}
	defer func() { _ = f.Close() }()

	// Try to acquire exclusive lock (non-blocking)
	// If this fails, someone else holds a shared lock
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		// EWOULDBLOCK means lock is held by another process
		return true
	}

	// We got the lock, so no one else has it - release immediately
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}
