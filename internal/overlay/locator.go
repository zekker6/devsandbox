package overlay

import (
	"os"
	"path/filepath"
	"sort"
	"time"
)

// UpperKind distinguishes the primary persistent upper from per-session uppers.
type UpperKind int

const (
	UpperPrimary UpperKind = iota
	UpperSession
)

// UpperSource is one upper directory participating in a merge.
type UpperSource struct {
	Kind        UpperKind
	Path        string    // absolute path to the upper dir
	SessionID   string    // empty for UpperPrimary
	SandboxID   string    // set by callers that aggregate across sandboxes; left empty here
	SourceLabel string    // human-readable label used in preview output
	ModTime     time.Time // directory mtime used for session ordering
}

// LocateUppers returns the ordered list of upper directories for hostPath
// under sandboxHome. Primary is always first (if it exists); sessions follow
// in ascending session-dir mtime order.
func LocateUppers(sandboxHome, hostPath string, primaryOnly bool) ([]UpperSource, error) {
	safe, err := SafePath(hostPath)
	if err != nil {
		return nil, err
	}

	var sources []UpperSource

	primary := filepath.Join(sandboxHome, "overlay", safe, "upper")
	if fi, err := os.Stat(primary); err == nil && fi.IsDir() {
		sources = append(sources, UpperSource{
			Kind:    UpperPrimary,
			Path:    primary,
			ModTime: fi.ModTime(),
		})
	}

	if primaryOnly {
		return sources, nil
	}

	sessionsRoot := filepath.Join(sandboxHome, "overlay", "sessions")
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return sources, nil
		}
		return nil, err
	}

	var sessions []UpperSource
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionDir := filepath.Join(sessionsRoot, e.Name(), safe)
		upper := filepath.Join(sessionDir, "upper")
		fi, err := os.Stat(upper)
		if err != nil || !fi.IsDir() {
			continue
		}
		sessDirFI, err := os.Stat(sessionDir)
		if err != nil {
			continue
		}
		sessions = append(sessions, UpperSource{
			Kind:      UpperSession,
			Path:      upper,
			SessionID: e.Name(),
			ModTime:   sessDirFI.ModTime(),
		})
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModTime.Before(sessions[j].ModTime)
	})
	return append(sources, sessions...), nil
}
