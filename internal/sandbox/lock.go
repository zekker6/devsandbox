package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"devsandbox/internal/procstate"
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
	for attempt := range designationGateRetries {
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

// stagingDirName is the one directory under the sandbox base that holds trees
// renamed aside for deletion. ListSandboxes skips it by exact name, so a
// removal in flight - or one a kill left half-done - is not reported as a
// sandbox.
//
// A directory rather than a sibling `.removing-<name>` prefix, because a prefix
// shares a namespace with the sandboxes themselves and no test over a name can
// separate the two reliably. A bare prefix check also hides a real sandbox whose
// project basename begins with the prefix, leaving it unlistable and unprunable;
// adding a trailing-pid check to fix that is worse, since a sandbox directory is
// `<basename>-<8 hex>` and roughly one hash in 43 is all decimal digits - so
// such a sandbox parses as a staged removal whose pid is above any pid_max, and
// prune deletes live state. Inside this directory nothing but staged trees
// exists, so the pid suffix can be read without inferring anything about the
// name in front of it. `.removing` cannot collide with a sandbox directory,
// which always carries the `-<8 hex>` suffix GenerateSandboxName appends.
//
// It is created on the first teardown and never removed - see
// RemoveSandboxIfIdle for why reclaiming it races a concurrent teardown.
const stagingDirName = ".removing"

// StagingDir returns the directory under baseDir that RemoveSandboxIfIdle
// renames sandboxes into before deleting them. It does not create it. Exported
// so the reclaim catalogue can name the location without reaching for the
// constant; nothing else should build the path by hand.
func StagingDir(baseDir string) string {
	return filepath.Join(baseDir, stagingDirName)
}

// stagingStaleAge bounds how long a staged tree survives once its teardown's
// pid can no longer be confirmed dead. The pid probe answers an uncertain
// result as alive, so a tree whose remover's pid was recycled by another
// user's process is never reclaimed on the pid alone - and such a tree is an
// entire sandbox, the largest thing any host-owned location can strand. Thirty
// days sits far past any teardown while still bounding that. The clock runs
// from the staging itself: RemoveSandboxIfIdle stamps the staged directory's
// mtime after the rename, because os.Rename leaves it at the sandbox's last
// write, and that would measure how idle the sandbox had been rather than how
// long the removal has been stranded.
const stagingStaleAge = 30 * 24 * time.Hour

// stagedPID returns the pid of the teardown that staged an entry of the staging
// directory. Only called for entries inside stagingDirName.
func stagedPID(name string) (int, bool) {
	i := strings.LastIndexByte(name, '-')
	if i <= 0 {
		return 0, false
	}
	pid, err := strconv.Atoi(name[i+1:])
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// RemoveSandboxIfIdle removes the sandbox state only while no session holds it,
// and reports whether it removed anything.
//
// The tree is renamed aside under the exclusive lock and only then deleted. The
// lock alone does not hold a launch off, because a removal unlinks the lock
// files as ordinary entries and acquireSharedLock reopens the path with O_CREATE
// on every retry: the moment `.lock` is gone a waiting launch creates a fresh
// inode, flocks that, conflicts with nothing, is designated primary by the same
// mechanism, and proceeds against a root still being deleted. Renaming first
// makes the name vanish atomically, so that launch finds no sandbox rather than
// half of one - and the slow part, a chmod walk over a tree that can hold the Go
// module and npm caches, then runs off the path nobody is racing.
//
// The caller must release its own handle first: its own shared lock is
// indistinguishable from another session's here.
// beforeRemove, when non-nil, runs once the sandbox is known to be idle and
// before any of it is taken away. It is for teardown that must not happen at
// all while another session is live, and that needs the tree still in place -
// the --rm worktree removal is both, since the worktree lives under
// sandboxRoot. It runs under the exclusive lock so no session can appear
// between the liveness answer and the work that depends on it, and it is not
// reached at all when the sandbox is busy or already gone.
func RemoveSandboxIfIdle(sandboxRoot string, beforeRemove func()) (bool, error) {
	return removeIfIdle(sandboxRoot, nil, beforeRemove)
}

// RemoveSandboxIfIdleWhen is RemoveSandboxIfIdle with a condition judged under
// the exclusive lock: when it answers false the sandbox is kept and nothing
// is removed. A condition read before the lock is stale by the time the lock
// is held - a launch can remove and recreate the sandbox in between, and the
// fresh one would be removed on the old one's facts.
func RemoveSandboxIfIdleWhen(sandboxRoot string, when func() bool) (bool, error) {
	return removeIfIdle(sandboxRoot, when, nil)
}

// errRemovalDeclined reports that the condition passed to stageForRemoval
// answered false under the lock.
var errRemovalDeclined = errors.New("sandbox removal declined")

func removeIfIdle(sandboxRoot string, when func() bool, beforeRemove func()) (bool, error) {
	res, err := stageForRemoval(sandboxRoot, when, beforeRemove)
	// Both trees are deleted off the lock, and whatever stageForRemoval
	// returned: a staging that got as far as renaming the shared temp
	// directory aside and then failed still has that directory to take away.
	sharedErr := errors.Join(res.sharedErr, removeStagedSharedTmp(res.sharedPath))
	if err != nil {
		if errors.Is(err, ErrSandboxBusy) || errors.Is(err, errRemovalDeclined) {
			return false, nil
		}
		return false, errors.Join(err, sharedErr)
	}
	if res.path == "" {
		return true, sharedErr
	}

	staged, stampErr := res.path, res.stampErr
	if err := RemoveSandbox(staged); err != nil {
		return false, errors.Join(err, stampErr, sharedErr)
	}
	// The staging root is deliberately left in place. Removing it when it looks
	// empty races another teardown for a different project under the same base:
	// that one's MkdirAll is a no-op against the directory this one created, and
	// between its MkdirAll returning and its rename issuing there is nothing
	// staged here to see - so the rmdir succeeds and its rename then fails
	// ENOENT, after it has already run beforeRemove and released its lock. It
	// would report a failed removal having already deleted the worktree. An
	// empty directory ListSandboxes skips by name is the cheaper outcome.
	return true, sharedErr
}

// stagedRemoval reports what stageForRemoval did under the exclusive lock.
//
// path is the staged tree, or "" when the root was already gone.
//
// stampErr is the failure of the mtime stamp, which is not a reason to leave
// the tree in place: the stamp only matters if the delete that follows is
// interrupted, so the caller folds it into a failed delete's error and drops
// it after a successful one.
//
// sharedPath is the shared temp directory renamed aside under the lock, for
// the caller to delete once the lock is released, or "" when there was none.
//
// sharedErr is the failure to stage the shared temp directory, which the
// caller reports whether the tree delete succeeds or not: the sandbox is what
// the user asked to remove, and the orphan sweep is the backstop for a
// directory left behind.
type stagedRemoval struct {
	path       string
	sharedPath string
	stampErr   error
	sharedErr  error
}

// stageForRemoval is the half of RemoveSandboxIfIdle that runs under the
// exclusive lock: it confirms the sandbox is idle and that when (if non-nil)
// still answers true, runs beforeRemove, renames
// the shared temp directory aside, renames the root aside under StagingDir and
// stamps the staged tree. It returns an error wrapping ErrSandboxBusy when
// another holder has the sandbox. Split out so the stamp can be observed in a
// test; the delete that follows would take the tree with it.
//
// Every path out of it returns what it staged, error or not, so the caller
// deletes both renamed trees whatever went wrong in between.
func stageForRemoval(sandboxRoot string, when func() bool, beforeRemove func()) (stagedRemoval, error) {
	if _, err := os.Stat(sandboxRoot); err != nil {
		if os.IsNotExist(err) {
			return stagedRemoval{}, nil
		}
		return stagedRemoval{}, fmt.Errorf("failed to stat sandbox root: %w", err)
	}

	lock, err := acquireExclusiveLock(filepath.Join(sandboxRoot, LockFileName))
	if err != nil {
		return stagedRemoval{}, err
	}

	if when != nil && !when() {
		_ = lock.Close()
		return stagedRemoval{}, errRemovalDeclined
	}

	if beforeRemove != nil {
		beforeRemove()
	}

	// The shared temp directory is keyed on a hash of the *path* of the
	// sandbox home, not on its inode, so staging the tree aside does not take
	// the name away from anyone: a launch waiting on this lock recreates the
	// root under the same path the moment the rename lands (acquireSharedLock
	// treats a missing root as "the sandbox is gone" and rebuilds it), hashes
	// to this same directory, and would have its $TMPDIR deleted underneath it
	// by a removal running outside the lock. Its name is therefore taken away
	// here, still holding the lock, where the sandbox is provably idle - and
	// not inside RemoveSandbox, which sees only the staged path, whose hash
	// names nothing. Only the rename runs under the lock: the delete is a chmod
	// walk over everything the sandbox put in $TMPDIR, and TeardownGracePeriod
	// is already spent in full by beforeRemove, so anything unbounded added
	// here aborts the launch acquireSharedLock's budget exists to hold on.
	sharedPath, sharedErr := stageSharedTmpFor(sandboxRoot)
	res := stagedRemoval{sharedPath: sharedPath, sharedErr: sharedErr}

	stagingRoot := StagingDir(filepath.Dir(sandboxRoot))
	if err := os.MkdirAll(stagingRoot, 0o755); err != nil {
		_ = lock.Close()
		return res, fmt.Errorf("failed to create removal staging directory: %w", err)
	}

	staged := filepath.Join(stagingRoot,
		fmt.Sprintf("%s-%d", filepath.Base(sandboxRoot), os.Getpid()))
	renameErr := os.Rename(sandboxRoot, staged)
	// Released before the removal: the name it guards no longer exists, and
	// holding it across the walk only lengthens the window nothing is watching.
	_ = lock.Close()
	if renameErr != nil {
		return res, fmt.Errorf("failed to stage sandbox root for removal: %w", renameErr)
	}

	res.path = staged
	// The stamp is what the stagingStaleAge backstop reads. It goes on before
	// the delete starts, not after, because it is for the tree a kill leaves
	// behind and a kill can land anywhere in the walk.
	now := time.Now()
	if err := os.Chtimes(staged, now, now); err != nil {
		res.stampErr = fmt.Errorf("failed to stamp staged tree %s: %w", filepath.Base(staged), err)
	}
	return res, nil
}

// ListAbandonedStaging returns the removal staging trees under baseDir whose
// teardown is no longer running.
//
// RemoveSandboxIfIdle renames a sandbox aside before deleting it, so a kill
// between the rename and the delete strands the tree under a name every listing
// path deliberately skips. Without this it is invisible to `sandboxes list`, to
// prune and to `overlay --all-sandboxes` at once, and nothing ever reclaims the
// disk. The pid in the name is what tells a stranded tree apart from a removal
// still in flight, which must be left to the process doing it. A pid the probe
// cannot inspect reads as alive, so a tree whose remover's pid was recycled to
// another user's process is kept on the pid alone - see internal/procstate for
// why - and is reclaimed instead once it has been staged for stagingStaleAge.
// That age rule applies to the uncertain answer alone: a teardown the kernel
// confirms is still running keeps its tree however long it has been staged,
// because the tree is what that process is deleting.
func ListAbandonedStaging(baseDir string) ([]string, error) {
	return listAbandonedStaging(baseDir, procstate.Probe)
}

// listAbandonedStaging is the seam ListAbandonedStaging delegates to, with the
// probe injected: the uncertain answer is the only one the backstop acts on
// and no pid produces it reliably on every host - inside a PID namespace pid 1
// is the runner's own init and answers Live.
func listAbandonedStaging(baseDir string, probe func(int) procstate.State) ([]string, error) {
	stagingRoot := StagingDir(baseDir)
	entries, err := os.ReadDir(stagingRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read removal staging directory: %w", err)
	}

	cutoff := time.Now().Add(-stagingStaleAge)
	var abandoned []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, ok := stagedPID(entry.Name())
		if !ok {
			continue
		}
		switch probe(pid) {
		case procstate.Live:
			continue
		case procstate.Unknown:
			if !stagedBefore(entry, cutoff) {
				continue
			}
		}
		abandoned = append(abandoned, filepath.Join(stagingRoot, entry.Name()))
	}
	return abandoned, nil
}

// stagedBefore reports whether the staged tree entry names was stamped before
// cutoff. It reads the directory's own mtime - the stamp stageForRemoval
// writes - and not the subtree's, which still carries the sandbox's history.
// An age that cannot be read answers false: unknown must not authorize a
// deletion.
func stagedBefore(entry fs.DirEntry, cutoff time.Time) bool {
	info, err := entry.Info()
	if err != nil {
		return false
	}
	return info.ModTime().Before(cutoff)
}

// RemoveAbandonedStaging deletes every staging tree ListAbandonedStaging
// reports under baseDir and returns how many it removed. A missing staging
// directory holds nothing and is not an error. A tree that cannot be removed
// is reported and the sweep continues, so one stuck tree never hides the rest.
//
// An empty baseDir is refused rather than resolved against the working
// directory: the caller's configured sandbox base is the only root this may
// act under.
//
// It calls plain RemoveSandbox on the staged path. The tree's original root
// may hold a new sandbox by now, so nothing keyed on that root - the shared
// temp directory in particular - may be derived from a staged name; the
// orphan sweep for that directory reclaims whatever a stranded tree left.
func RemoveAbandonedStaging(baseDir string) (int, error) {
	return removeAbandonedStaging(baseDir, procstate.Probe)
}

// removeAbandonedStaging is the seam RemoveAbandonedStaging delegates to; see
// listAbandonedStaging for why the probe is injectable.
func removeAbandonedStaging(baseDir string, probe func(int) procstate.State) (int, error) {
	if baseDir == "" {
		return 0, errors.New("failed to reclaim interrupted removals: sandbox base is empty")
	}
	abandoned, err := listAbandonedStaging(baseDir, probe)
	if err != nil {
		return 0, err
	}

	removed := 0
	var errs []error
	for _, path := range abandoned {
		if err := RemoveSandbox(path); err != nil {
			errs = append(errs, fmt.Errorf("failed to reclaim %s: %w", filepath.Base(path), err))
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// TeardownGracePeriod bounds the work RemoveSandboxIfIdle runs while holding
// the sandbox exclusively - in practice the --rm worktree removal, which
// beforeRemove carries and which is capped at exactly this. It is exported so
// that cap and the wait below are one number: a shared-lock budget shorter than
// the teardown it must outlast aborts the very launch the retry exists for.
const TeardownGracePeriod = 30 * time.Second

// sharedLockRetries and sharedLockRetryDelay bound the wait for the exclusive
// holders a launch legitimately races: a --rm teardown, which holds the lock
// across beforeRemove, and the momentary probes IsSessionActive and the primary
// designation take. Failing outright is what a launch issued straight after
// `devsandbox --rm ...` used to hit.
//
// The budget covers TeardownGracePeriod rather than a round number. The
// teardown's slow half - the chmod walk and recursive delete over the Go module
// and npm caches - runs after the rename and off the lock, but beforeRemove
// does not, and `git worktree remove --force` over a checkout carrying build
// artifacts is not bounded by anything smaller.
const (
	sharedLockRetryDelay = 40 * time.Millisecond
	// +1 because the first attempt does not sleep, so N attempts wait N-1 delays.
	sharedLockRetries = int(TeardownGracePeriod/sharedLockRetryDelay) + 1

	// designationGateRetries bounds a different wait: the gate is held only
	// across two flock calls and a file open, never across a teardown, so it
	// does not inherit the budget above.
	designationGateRetries = 50
)

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
			// O_CREATE creates the lock file, never its parent, and the
			// teardown renames the whole sandbox root away - so once that
			// rename lands every remaining attempt fails ENOENT identically and
			// the launch dies naming a missing file. Recreating the root is the
			// right answer to "the sandbox is gone": the launch goes on to build
			// a fresh one, which is what it would have done had it started a
			// moment later.
			if errors.Is(err, fs.ErrNotExist) {
				if mkErr := os.MkdirAll(sandboxRoot, 0o755); mkErr != nil {
					return nil, fmt.Errorf("failed to recreate sandbox root: %w", mkErr)
				}
			}
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

		// A successful flock says nothing about whether the descriptor still
		// lives at lockPath. RemoveSandboxIfIdle renames the root aside and only
		// then releases its own lock, so an open that lands before that rename
		// and a flock that lands after it succeed against the lock inode inside
		// the .removing-* tree: the session would hold a lock on a directory
		// that is being deleted, and acquireDesignationGate then fails ENOENT
		// opening .primary.lock under a parent nothing recreates. Retrying is
		// the fix, because the next attempt's open gets ENOENT and rebuilds the
		// root.
		if !sameFileAt(lockPath, f) {
			_ = f.Close()
			lastErr = fmt.Errorf("%w: %s was renamed aside mid-acquire", ErrSandboxBusy, sandboxRoot)
			continue
		}

		return f, nil
	}

	return nil, lastErr
}

// sameFileAt reports whether path still names the inode f holds open.
func sameFileAt(path string, f *os.File) bool {
	atPath, err := os.Stat(path)
	if err != nil {
		return false
	}
	held, err := f.Stat()
	if err != nil {
		return false
	}
	return os.SameFile(atPath, held)
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
