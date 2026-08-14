package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// LockFileName is the name of the session lock file within a sandbox directory.
const LockFileName = ".lock"

// PrimaryLockFileName is the name of the designation gate. It is held
// exclusively only across the designation itself - the probe of
// SessionsLockFileName plus this launch's registration in it - and released
// before the workload starts. Serializing that pair is the whole job: it is what
// makes "am I alone" and "I am here now" one indivisible step, so no two
// launches can answer the question against different views of the sandbox.
//
// It deliberately does not stay held for the primary's lifetime. Doing that made
// the gate unavailable to every other launch, so a concurrent launch registered
// itself without holding it - and a launch that won the gate could then observe
// that registration and stand down, leaving the sandbox with no primary at all.
const PrimaryLockFileName = ".primary.lock"

// SessionsLockFileName is the name of the lock that answers "is another session
// live". Every launch holds it shared for its lifetime, and the only exclusive
// taker is the designation probe in AcquireSession, which holds
// PrimaryLockFileName while it runs. That is the whole reason it is not
// LockFileName: IsSessionActive and RemoveSandboxIfIdle take LockFileName
// exclusively too, so a probe there cannot tell a live session apart from a
// `sandboxes list` sampling liveness or a --rm teardown mid-removal - and
// reading either of those as a live session would silently demote the launch to
// a concurrent one, whose overlay changes are discarded at exit.
const SessionsLockFileName = ".sessions.lock"

// ErrSandboxBusy reports that the sandbox state is held exclusively by another
// devsandbox process - a --rm teardown removing it, or a command probing
// whether it is idle.
var ErrSandboxBusy = errors.New("sandbox state is held by another devsandbox process")

// SessionHandle is one launch's hold on a sandbox: the shared lock every
// session takes, the shared liveness registration, and whether this launch won
// the designation. The handle must stay open for the session's lifetime; the
// kernel drops both locks if the process dies.
type SessionHandle struct {
	mu       sync.Mutex
	shared   *os.File
	live     *os.File
	primary  bool
	released bool
}

// AcquireSession takes this launch's hold on the sandbox and reports through
// IsPrimary whether it owns the sandbox's persistent state.
//
// Every lock is taken here, before anything samples whether another session
// exists, and that ordering is the point: probing IsSessionActive first and
// locking later is a check-then-act race in which two launches started together
// both conclude they are alone and then both write the same persistent overlay
// upper and work directories.
//
// What makes the designation sound is that the whole of it runs under the
// designation gate: every launch waits for PrimaryLockFileName, then probes
// whether anyone else is live, then registers itself in SessionsLockFileName,
// then releases the gate. Probe and registration are therefore indivisible, so a
// launch is either fully registered before another one looks, or has not started
// looking yet - never halfway. Being alone at that moment *is* being primary,
// and since only one launch is ever inside the gate, exactly one can be.
//
// Splitting the pair is what broke: while the gate was also the primary's
// lifetime lock, a launch that lost it registered itself outside the gate, so
// the winner's probe could see that registration and stand down - electing no
// primary at all - and, in the mirror case, a launch could be elected while a
// concurrent session that had not yet registered was already starting up.
//
// Being alone is required, not just a free gate. When the primary exits while a
// concurrent session it started before is still live, the gate frees under that
// session. A launch taking it on that alone would treat itself as the sole
// occupant - wiping the live session's overlay dirs on startup and writing the
// persistent upper the live session has mounted as a read-only lower layer. Not
// alone means concurrent.
func AcquireSession(sandboxRoot string) (handle *SessionHandle, err error) {
	shared, err := acquireSharedLock(sandboxRoot, LockFileName)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = shared.Close()
		}
	}()

	gate, err := acquireDesignationGate(sandboxRoot)
	if err != nil {
		return nil, err
	}
	// Released as soon as the designation is settled: it serializes the
	// decision, it does not record its outcome.
	defer func() { _ = gate.Close() }()

	alone, err := noOtherSessionIsLive(sandboxRoot)
	if err != nil {
		return nil, err
	}

	live, err := acquireSharedLock(sandboxRoot, SessionsLockFileName)
	if err != nil {
		return nil, err
	}

	return &SessionHandle{shared: shared, live: live, primary: alone}, nil
}

// acquireDesignationGate takes the lock that serializes the designation,
// retrying while another launch is inside it. The hold is short and bounded -
// two flock calls and a file open - so the wait a launch can observe here is a
// few other launches' designations, not any part of a workload's lifetime.
func acquireDesignationGate(sandboxRoot string) (*os.File, error) {
	path := filepath.Join(sandboxRoot, PrimaryLockFileName)

	var lastErr error
	for attempt := range sharedLockRetries {
		if attempt > 0 {
			time.Sleep(sharedLockRetryDelay)
		}
		f, err := acquireExclusiveLock(path)
		if err == nil {
			return f, nil
		}
		// Only contention is worth waiting out. The shared lock above already
		// retried the sandbox root into existence, so anything else here is a
		// real failure and retrying it just delays the report.
		if !errors.Is(err, ErrSandboxBusy) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

// noOtherSessionIsLive reports whether any other launch still holds the sandbox.
// The caller must hold the designation gate and must not yet hold the sessions
// lock itself - its own hold would be indistinguishable from another session's.
func noOtherSessionIsLive(sandboxRoot string) (bool, error) {
	probe, err := acquireExclusiveLock(filepath.Join(sandboxRoot, SessionsLockFileName))
	if err != nil {
		if errors.Is(err, ErrSandboxBusy) {
			return false, nil
		}
		return false, err
	}
	return true, probe.Close()
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
	return !h.released && h.primary
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
	h.primary = false
	if h.live != nil {
		if err := h.live.Close(); err != nil {
			errs = append(errs, fmt.Errorf("release sessions lock: %w", err))
		}
		h.live = nil
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

// sharedLockRetries and sharedLockRetryDelay bound the wait for the exclusive
// holders a launch legitimately races: a --rm teardown, which holds the lock
// across a chmod walk and a recursive removal, and the momentary probes
// IsSessionActive and the primary designation take. Failing outright is what a
// launch issued straight after `devsandbox --rm ...` used to hit.
const (
	sharedLockRetries    = 50
	sharedLockRetryDelay = 40 * time.Millisecond
)

// AcquireSessionLock acquires a shared lock on the sandbox.
// The caller must keep the returned file open for the session duration.
// The lock is automatically released when the file is closed or process exits.
func AcquireSessionLock(sandboxRoot string) (*os.File, error) {
	return acquireSharedLock(sandboxRoot, LockFileName)
}

// acquireSharedLock takes one of the locks a session holds for its lifetime,
// retrying briefly while another process holds it exclusively.
func acquireSharedLock(sandboxRoot, name string) (*os.File, error) {
	lockPath := filepath.Join(sandboxRoot, name)

	var lastErr error
	for attempt := range sharedLockRetries {
		if attempt > 0 {
			time.Sleep(sharedLockRetryDelay)
		}

		// Reopened each attempt: a --rm teardown may have removed the directory
		// the previous handle pointed into.
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			lastErr = fmt.Errorf("failed to open lock file: %w", err)
			continue
		}

		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
			_ = f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				lastErr = fmt.Errorf("%w: %s", ErrSandboxBusy, sandboxRoot)
				continue
			}
			return nil, fmt.Errorf("failed to acquire lock: %w", err)
		}

		return f, nil
	}

	return nil, lastErr
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
