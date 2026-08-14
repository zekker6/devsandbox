package overlay

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// layerEntry is one path as one upper records it, classified the way overlayfs
// reads that upper: a whiteout marker, an opaque directory, or an ordinary
// entry.
type layerEntry struct {
	source     UpperSource
	rel        string
	fi         fs.FileInfo
	isWhiteout bool
	isOpaque   bool
}

// layerMerge accumulates entries across uppers in walk order, last-write-wins,
// and reproduces the two overlayfs constructs that hide *lower* layers rather
// than replacing a single path: a whiteout, and an opaque directory.
type layerMerge struct{ byRel map[string]layerEntry }

func newLayerMerge() *layerMerge { return &layerMerge{byRel: map[string]layerEntry{}} }

// add records e as the winner for its path. When e hides everything the lower
// layers hold beneath that path - a whiteout (the delete half of a delete, or
// of a delete-then-recreate in a *later* upper) or an opaque directory (the
// recreate half, in the same upper) - every descendant recorded so far is
// dropped with it. Without that, an earlier upper's `d/file` survives a later
// upper's removal of `d` and is migrated back onto the host: state the merged
// overlay view does not contain.
//
// Only entries already recorded are pruned, which is exactly the set from lower
// layers: uppers are walked in order, and WalkDir visits a directory before its
// own children, so an opaque directory's contents are added after the prune.
func (m *layerMerge) add(e layerEntry) {
	if e.isWhiteout || e.isOpaque {
		prefix := e.rel + string(filepath.Separator)
		for rel := range m.byRel {
			if strings.HasPrefix(rel, prefix) {
				delete(m.byRel, rel)
			}
		}
	}
	m.byRel[e.rel] = e
}

// rels returns the recorded paths in lexicographic order. Every path is a
// cleaned relative path, so a parent is a prefix of its own children and sorts
// before them: the operations Apply derives from this order create a directory
// before anything beneath it, and the order is the same on every run. Emitting
// straight from map iteration made the host result depend on Go's map
// randomization when a plan held both a delete and a create under one path.
func (m *layerMerge) rels() []string {
	out := make([]string, 0, len(m.byRel))
	for rel := range m.byRel {
		out = append(out, rel)
	}
	slices.Sort(out)
	return out
}

// BuildPlan walks the upper sources in order and produces the set of
// operations needed to promote their merged state to hostPath.
//
// Later sources in the slice override earlier ones on the same relpath
// (last-write-wins by caller-supplied order). Character-device files with
// rdev=0 in any upper are treated as overlayfs whiteouts and produce
// OpDelete; a whiteout or an opaque directory also drops everything earlier
// uppers recorded beneath it. Symlinks are preserved as symlinks. If a stat in
// the upper returns EPERM, the entry is silently skipped (never invented as a
// spurious delete). Operations are emitted in lexicographic relpath order, so
// parents precede their children and repeated calls on the same inputs produce
// the same plan.
func BuildPlan(sources []UpperSource, hostPath string) (Plan, error) {
	merged := newLayerMerge()

	for _, src := range sources {
		walkErr := filepath.WalkDir(src.Path, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if path == src.Path {
				return nil
			}
			rel, relErr := filepath.Rel(src.Path, path)
			if relErr != nil {
				return relErr
			}
			fi, statErr := os.Lstat(path)
			if statErr != nil {
				if errors.Is(statErr, fs.ErrPermission) {
					return nil // best-effort, skip
				}
				return statErr
			}
			isWhiteout := fi.Mode()&os.ModeCharDevice != 0 && isZeroDev(fi)
			merged.add(layerEntry{
				source:     src,
				rel:        rel,
				fi:         fi,
				isWhiteout: isWhiteout,
				isOpaque:   fi.IsDir() && isOpaqueDir(path),
			})
			return nil
		})
		if walkErr != nil {
			return Plan{}, fmt.Errorf("walk upper %q: %w", src.Path, walkErr)
		}
	}

	plan := Plan{
		HostPath:  hostPath,
		BySandbox: map[string][]Operation{},
	}

	for _, rel := range merged.rels() {
		e := merged.byRel[rel]
		hostFull := filepath.Join(hostPath, rel)
		op := Operation{
			RelPath:     rel,
			HostPath:    hostFull,
			Source:      filepath.Join(e.source.Path, rel),
			SourceLabel: e.source.SourceLabel,
			ModTime:     e.fi.ModTime(),
		}

		switch {
		case e.isWhiteout:
			op.Kind = OpDelete
			op.Source = ""
			// Only record delete if the host has something to remove.
			if _, err := os.Lstat(hostFull); errors.Is(err, fs.ErrNotExist) {
				continue
			}
		case e.fi.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(filepath.Join(e.source.Path, rel))
			if err != nil {
				return Plan{}, fmt.Errorf("readlink %q: %w", rel, err)
			}
			op.IsSymlink = true
			op.LinkTarget = link
			op.Kind = kindForTarget(hostFull)
		case e.fi.IsDir():
			op.IsDir = true
			op.Mode = e.fi.Mode()
			op.Kind = kindForTarget(hostFull)
		default:
			op.Mode = e.fi.Mode()
			op.Bytes = e.fi.Size()
			op.Kind = kindForTarget(hostFull)
		}

		plan.Operations = append(plan.Operations, op)
		plan.BySandbox[e.source.SandboxID] = append(plan.BySandbox[e.source.SandboxID], op)
	}
	return plan, nil
}

// kindForTarget returns OpOverwrite if hostFull exists, else OpCreate.
func kindForTarget(hostFull string) OpKind {
	if _, err := os.Lstat(hostFull); err == nil {
		return OpOverwrite
	}
	return OpCreate
}

// isZeroDev returns true when the FileInfo is a char device whose rdev is 0,
// the overlayfs whiteout marker.
func isZeroDev(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return st.Rdev == 0
}
