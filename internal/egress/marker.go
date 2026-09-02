package egress

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"devsandbox/internal/procstate"
)

// legacyMarkerPrefix is the name shape markers had before they recorded their
// owner: os.MkdirTemp(root, "lockdown-") appended a random suffix, so nothing
// on disk under this prefix can be tied to a process. Such a marker may still
// belong to a session running on an older binary, so it is reclaimed by age
// rather than on sight.
const legacyMarkerPrefix = "lockdown-"

// markerStaleAge bounds a legacy marker's lifetime. A marker is written at
// launch and touched once more when the prologue creates the ready file, so
// anything untouched for this long belongs to a launch that ended long ago.
const markerStaleAge = 7 * 24 * time.Hour

// MarkerRoot returns the directory lockdown markers are created in:
// $XDG_STATE_HOME/devsandbox/egress, falling back to
// <homeDir>/.local/state/devsandbox/egress when XDG_STATE_HOME is unset. It
// does not create the directory.
//
// The root is host-owned state, the same place every other host-trusted record
// in this project lives. It is deliberately not $TMPDIR: that is whatever the
// invoking user set, so it can name a directory bound read-write into the
// sandbox, and a workload that can delete the marker can make its own exit
// code 78 read as an aborted lockdown - the exact signal the marker exists to
// give. The sandbox repoints XDG_STATE_HOME at its synthetic home, so this
// path is unreachable from inside no matter how the host is configured.
func MarkerRoot(homeDir string) string {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = filepath.Join(homeDir, ".local", "state")
	}
	return filepath.Join(stateHome, "devsandbox", "egress")
}

// NewMarkerDir creates the marker directory for this process under root,
// creating root itself if needed, and returns its path. The directory is
// named by the caller's pid, which is what lets SweepMarkers tell a marker a
// killed launch left behind from one a live launch is using.
//
// A directory already there under this pid can only be a leftover from a
// launch that died and whose pid the kernel then handed to this process: the
// sweep keeps it because its pid is alive, and that pid is us. It is replaced
// rather than reused, because a ready file still inside it would answer
// LockdownApplied before the prologue has run a single rule.
func NewMarkerDir(root string) (string, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("egress: create marker root %s: %w", root, err)
	}
	dir := filepath.Join(root, strconv.Itoa(os.Getpid()))
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("egress: replace stale marker %s: %w", dir, err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("egress: create marker %s: %w", dir, err)
	}
	return dir, nil
}

// SweepMarkers removes the markers under root that no launch can still be
// using and reports how many it removed. A pid-named marker goes once its pid
// is dead; a legacy lockdown-* marker goes once nothing in it has been
// modified for markerStaleAge; a name of any other shape is left alone. A
// missing root is not an error - it is the normal state of a host that has
// never run proxy mode.
//
// The pid probe answers an uncertain result as alive, so a pid-named marker
// whose owner answers EPERM is kept, with no age backstop: sweeping one under
// a live session would make that session's own exit 78 read as an aborted
// lockdown, and the leak it would prevent is one empty directory.
//
// A marker that cannot be removed is reported and the sweep continues, so one
// stuck entry never hides the rest.
func SweepMarkers(root string) (int, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("egress: read marker root %s: %w", root, err)
	}
	cutoff := time.Now().Add(-markerStaleAge)
	removed := 0
	var errs []error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(root, e.Name())
		if !markerStale(path, e.Name(), cutoff) {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, fmt.Errorf("egress: remove marker %s: %w", e.Name(), err))
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// markerStale decides whether the marker at path may be removed, by the rule
// its name selects.
func markerStale(path, name string, cutoff time.Time) bool {
	if pid, ok := markerPID(name); ok {
		return !procstate.Alive(pid)
	}
	if strings.HasPrefix(name, legacyMarkerPrefix) {
		return !modifiedSince(path, cutoff)
	}
	return false
}

// markerPID reads a pid-named marker. Only the exact spelling NewMarkerDir
// produces counts: a sign, leading zeros or anything Atoi would tolerate is
// some other shape and is not probed.
func markerPID(name string) (int, bool) {
	pid, err := strconv.Atoi(name)
	if err != nil || pid <= 0 || strconv.Itoa(pid) != name {
		return 0, false
	}
	return pid, true
}

// modifiedSince reports whether path, or anything beneath it, changed after
// cutoff. The whole subtree is judged, not the directory alone: a directory's
// mtime moves only when a direct child is added or removed, and the ready
// file is written after the directory is. An entry that cannot be read has
// an unknown age, and unknown must not authorize a deletion, so a walk error
// answers "modified".
//
// internal/sandbox/tools carries the same walk for the shared temp directory;
// it is not shared because this package imports nothing internal beyond the
// pid probe, so the bwrap isolator's darwin build stays free of Linux-only
// packages.
func modifiedSince(path string, cutoff time.Time) bool {
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
