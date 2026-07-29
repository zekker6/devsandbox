package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"devsandbox/internal/fsutil"
)

// sharedTmpRelPath is the root of the per-session directory shared read-write
// between the host and the sandbox. It is used as BOTH the bind source and the
// bind destination: helpers that run on the host receive paths under it as
// literal strings, so it has to resolve identically in both mount namespaces —
// hence the same string on both sides.
//
// The same path is exported as $TMPDIR for every sandboxed process. That is not
// a preference. The launchers that need the directory are third-party shell
// scripts which resolve their scratch base as "${TMPDIR:-/tmp}" and expose no
// other hook, so nothing narrower reaches them. The cost is that every
// temporary file in the sandbox lands here rather than on the tmpfs /tmp the
// builder mounts: persistent, host-visible and disk-backed instead of
// ephemeral. prepareSharedTmp is what keeps that bounded.
const sharedTmpRelPath = ".cache/devsandbox/tmp"

// legacySharedTmpRelPath is where this directory lived when it was owned by the
// revdiff tool alone. Nothing binds it any more; prepareSharedTmp reclaims it.
const legacySharedTmpRelPath = ".cache/devsandbox/revdiff-ipc"

// sharedTmpStaleAge bounds an entry's lifetime when the contents cannot simply
// be wiped because a sibling session is live. Anything untouched for this long
// cannot belong to work in progress.
const sharedTmpStaleAge = 7 * 24 * time.Hour

// ToolWithSharedTmp marks a tool that needs the host↔sandbox shared temp
// directory. Declaring it is all a tool has to do: the directory, its bind
// mount, its $TMPDIR export and its cleanup are owned here. Several tools can
// therefore depend on it without duplicating the lifecycle or racing to emit
// the same mount, which the builder refuses outright.
type ToolWithSharedTmp interface {
	Tool

	// SharedTmp is a marker with no behavior. It exists so the dependency is
	// declared in the type system rather than inferred from a tool's bindings.
	SharedTmp()
}

// NeedsSharedTmp reports whether any tool available on this host depends on the
// shared temp directory.
func NeedsSharedTmp(homeDir string) bool {
	for _, t := range Available(homeDir) {
		if _, ok := t.(ToolWithSharedTmp); ok {
			return true
		}
	}
	return false
}

// SharedTmpBinding returns the bind mount for the shared temp directory.
//
// It must stay a real bind with Source equal to Dest: an overlay would keep the
// sandbox's writes from reaching the host, which is the entire point of the
// directory, and a differing destination would break the literal paths host
// helpers are handed.
func SharedTmpBinding(homeDir, sandboxHome string) []Binding {
	if homeDir == "" || sandboxHome == "" {
		return nil
	}
	shared := SharedTmpPath(homeDir, sandboxHome)
	return []Binding{{
		Source:   shared,
		Dest:     shared,
		Type:     MountBind,
		Category: CategoryRuntime,
	}}
}

// SharedTmpEnv returns the $TMPDIR export pointing at the shared directory.
func SharedTmpEnv(homeDir, sandboxHome string) []EnvVar {
	if homeDir == "" || sandboxHome == "" {
		return nil
	}
	return []EnvVar{{Name: "TMPDIR", Value: SharedTmpPath(homeDir, sandboxHome)}}
}

// SharedTmpPath returns the shared directory for one sandbox home — an
// identical string on the host and inside the sandbox.
func SharedTmpPath(homeDir, sandboxHome string) string {
	return filepath.Join(homeDir, sharedTmpRelPath, sharedTmpSessionID(sandboxHome))
}

// SharedTmpRoot returns the directory holding one entry per sandbox home. It is
// bind-mounted into the sandbox at this same path, so anything the host must be
// able to trust has to live somewhere else.
func SharedTmpRoot(homeDir string) string {
	return filepath.Join(homeDir, sharedTmpRelPath)
}

func legacySharedTmpPath(homeDir, sandboxHome string) string {
	return filepath.Join(homeDir, legacySharedTmpRelPath, sharedTmpSessionID(sandboxHome))
}

// sharedTmpSessionID derives a stable, collision-resistant tag from the
// host-side sandbox home path, keeping sibling projects isolated under the
// shared root.
func sharedTmpSessionID(sandboxHome string) string {
	h := sha256.Sum256([]byte(sandboxHome))
	return hex.EncodeToString(h[:6]) // 12 hex chars is plenty for per-user collision resistance
}

// prepareSharedTmp creates the shared temp directory and bounds its growth.
//
// Because $TMPDIR points here, the directory collects far more than the IPC
// files it exists for: Go build temporaries, test scratch trees, agent
// scratchpads. On the tmpfs /tmp those would vanish at exit; here they survive
// on disk indefinitely unless something removes them.
//
// A cold start — no other devsandbox process sharing this sandbox home — empties
// the directory, which restores the lifetime a caller expects of $TMPDIR. When
// a sibling session is live the contents may be its working files, so only
// entries untouched for sharedTmpStaleAge are removed. Wiping unconditionally is
// what an earlier version got wrong: it yanked state out from under a running
// tenant, and a subsequent non-recursive mkdir then failed with ENOENT.
//
// This process registers itself in the run directory BEFORE looking for
// siblings. Announcing first means two sessions starting together each observe
// the other and both fall back to the conservative prune; observing first lets
// both conclude they are alone and delete each other's files.
//
// Failures that leave the directory unusable are returned. Cleanup failures are
// reported through the logger instead: they waste disk, but refusing to launch
// over them helps nobody.
func prepareSharedTmp(homeDir, sandboxHome string, logger ErrorLogger) error {
	if homeDir == "" || sandboxHome == "" {
		return nil
	}

	if _, err := ensureRunDir(sandboxHome); err != nil {
		return fmt.Errorf("shared tmp: register session: %w", err)
	}

	dir := SharedTmpPath(homeDir, sandboxHome)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("shared tmp: create %s: %w", dir, err)
	}
	// MkdirAll leaves an existing directory's mode alone, so tighten it here.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("shared tmp: chmod %s: %w", dir, err)
	}

	var errs []error
	if hasLiveSiblingSession(sandboxHome) {
		errs = append(errs, pruneStaleEntries(dir, time.Now().Add(-sharedTmpStaleAge)))
	} else {
		errs = append(errs, removeContents(dir))
		// The pre-rename directory for this same sandbox home is dead the
		// moment we run: no current devsandbox binds it.
		if err := fsutil.RemoveAllForce(legacySharedTmpPath(homeDir, sandboxHome)); err != nil {
			errs = append(errs, fmt.Errorf("shared tmp: remove legacy dir: %w", err))
		}
	}
	errs = append(errs, removeEmptyDir(filepath.Join(homeDir, legacySharedTmpRelPath)))

	if err := errors.Join(errs...); err != nil && logger != nil {
		logger.LogErrorf("tools", "shared tmp cleanup: %v", err)
	}
	return nil
}

// hasLiveSiblingSession reports whether another devsandbox process is using this
// sandbox home. Run directories are keyed on PID, the caller has already
// registered its own, and cleanupStaleRunDirs has removed the ones belonging to
// processes that are gone — so anything else still alive is a sibling.
func hasLiveSiblingSession(sandboxHome string) bool {
	entries, err := os.ReadDir(filepath.Join(sandboxHome, runDirName))
	if err != nil {
		// A missing run directory means nothing registered. Any other read
		// error leaves the question unanswered, and an unanswered question must
		// not authorize a wipe.
		return !os.IsNotExist(err)
	}
	self := os.Getpid()
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		if processAlive(pid) {
			return true
		}
	}
	return false
}

// removeContents empties dir without removing dir itself. The bind mount set up
// after this resolves the source path, so replacing the inode underneath it
// would be pointless churn.
func removeContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	var errs []error
	for _, e := range entries {
		if err := fsutil.RemoveAllForce(filepath.Join(dir, e.Name())); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", e.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// pruneStaleEntries removes top-level entries under dir with nothing modified
// since cutoff.
func pruneStaleEntries(dir string, cutoff time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	var errs []error
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if modifiedSince(path, cutoff) {
			continue
		}
		if err := fsutil.RemoveAllForce(path); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", e.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// modifiedSince reports whether path, or anything beneath it, changed after
// cutoff.
//
// The whole subtree has to be considered. A directory's own mtime moves only
// when its direct children change, so a tenant writing deep inside a tree it
// created days ago looks stale to a shallow check — and that tenant may belong
// to the very sibling session this prune exists to protect.
//
// Walk errors are treated as "modified": an entry that cannot be read is an
// entry whose age is unknown, and unknown must not mean deletable.
func modifiedSince(path string, cutoff time.Time) bool {
	recent := false
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			recent = true
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil {
			// The entry vanished mid-walk; whoever removed it did our job.
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

// removeEmptyDir removes dir only when it holds nothing, so a root left over
// from an older layout disappears once its last entry is reclaimed. A
// non-empty directory, a missing one, and a directory in use all leave it
// alone.
func removeEmptyDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", dir, err)
	}
	if len(entries) > 0 {
		return nil
	}
	if err := os.Remove(dir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", dir, err)
	}
	return nil
}
