package overlay

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

// layerEntry is one path as one upper records it, classified the way overlayfs
// reads that upper: a whiteout marker, an opaque directory, or an ordinary
// entry.
type layerEntry struct {
	source     UpperSource
	rel        string
	fi         fs.FileInfo
	isDir      bool
	isWhiteout bool
	isOpaque   bool
}

// layerMerge accumulates entries across uppers in walk order, last-write-wins,
// and reproduces the two overlayfs constructs that hide *lower* layers rather
// than replacing a single path: a whiteout, and an opaque directory.
type layerMerge struct {
	byRel map[string]layerEntry
	// children indexes each recorded path by its parent, so pruning a subtree
	// costs the size of that subtree instead of a scan of every entry recorded
	// so far. Scanning was O(n) per non-directory - which is most of an upper -
	// and a cache upper reaches six figures of entries, where the quadratic
	// version spent minutes before the dry run printed anything.
	children map[string]map[string]struct{}
}

func newLayerMerge() *layerMerge {
	return &layerMerge{
		byRel:    map[string]layerEntry{},
		children: map[string]map[string]struct{}{},
	}
}

// add records e as the winner for its path. When e hides everything the lower
// layers hold beneath that path, every descendant recorded so far is dropped
// with it. Without that, an earlier upper's `d/file` survives a later upper's
// removal of `d` and is migrated back onto the host: state the merged overlay
// view does not contain.
//
// Three constructs hide a lower layer's subtree, not two. A whiteout (the delete
// half of a delete, or of a delete-then-recreate in a *later* upper) and an
// opaque directory (the recreate half, in the same upper) are the marked ones.
// The third carries no marker at all and needs none: overlayfs merges a path
// across layers only while both sides are directories, so anything else in a
// higher upper masks the lower subtree outright. `rm -rf d && echo x > d` in a
// concurrent session leaves `d` as a plain regular file, its whiteout replaced
// by the new file, and every `d/*` the primary upper still holds hidden. Missing
// that case did not merely migrate stale state: the plan emitted `d` as a file
// and `d/file` after it, and Apply died at `mkdir d: not a directory` with the
// earlier operations committed, at the same operation on every retry.
//
// Only entries already recorded are pruned, which is exactly the set from lower
// layers: uppers are walked in order, and WalkDir visits a directory before its
// own children, so an opaque directory's contents are added after the prune -
// and a non-directory has no children to lose.
func (m *layerMerge) add(e layerEntry) {
	if e.isWhiteout || e.isOpaque || !e.isDir {
		m.prune(e.rel)
	}
	m.link(e.rel)
	m.byRel[e.rel] = e
}

// link records rel under each of its ancestors so prune can walk down to it.
//
// Every ancestor is linked, not just the immediate parent, because an entry can
// be recorded without its parent ever being: a directory whose Lstat returns
// EPERM is skipped while WalkDir still descends into it. Linking only the parent
// would leave such a subtree unreachable, and the whiteout above it would fail
// to hide it. The walk stops at the first link already present, which was
// created together with every link above it, so this is O(1) after the first
// entry in a directory.
func (m *layerMerge) link(rel string) {
	for child := rel; ; {
		parent := filepath.Dir(child)
		kids := m.children[parent]
		if kids == nil {
			kids = map[string]struct{}{}
			m.children[parent] = kids
		}
		if _, linked := kids[child]; linked {
			return
		}
		kids[child] = struct{}{}
		if parent == child || parent == "." || parent == string(filepath.Separator) {
			return
		}
		child = parent
	}
}

// prune drops everything recorded beneath rel, depth first. rel itself stays:
// the caller is about to overwrite it. An ancestor that was only ever linked
// carries no byRel entry, and deleting a key that is not there is a no-op.
func (m *layerMerge) prune(rel string) {
	for child := range m.children[rel] {
		m.prune(child)
		delete(m.byRel, child)
	}
	delete(m.children, rel)
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
// OpDelete; a whiteout, an opaque directory, or any non-directory also drops
// everything earlier uppers recorded beneath it, since none of the three merges
// with a lower subtree. Symlinks are preserved as symlinks. If a stat in
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
			if !isWhiteout && !isMigratable(fi) {
				// Skipping the entry is not the same as ignoring it. A FIFO or
				// socket is still a non-directory in a higher upper, so it
				// masks whatever the lower layers hold at that path — the
				// `rm -rf d && mkfifo d` case, which otherwise migrated the
				// earlier upper's `d` and every `d/*` back onto the host, i.e.
				// exactly the files the sandbox deleted. Nothing is recorded in
				// its place, because the applier has no form to promote it to.
				merged.prune(rel)
				delete(merged.byRel, rel)
				return nil
			}
			merged.add(layerEntry{
				source: src,
				rel:    rel,
				fi:     fi,
				// A whiteout is a char device, so it is never a directory; the
				// explicit field keeps that reading off fi, which the merge
				// tests construct without.
				isDir:      fi.IsDir(),
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
			op.Kind, op.ReplacesHostDir = kindForTarget(hostFull)
		case e.fi.IsDir():
			op.IsDir = true
			op.Mode = e.fi.Mode()
			// A directory over a directory is merged, not replaced.
			op.Kind, _ = kindForTarget(hostFull)
		default:
			op.Mode = e.fi.Mode()
			op.Bytes = e.fi.Size()
			op.Kind, op.ReplacesHostDir = kindForTarget(hostFull)
		}

		plan.Operations = append(plan.Operations, op)
		plan.BySandbox[e.source.SandboxID] = append(plan.BySandbox[e.source.SandboxID], op)
	}
	return plan, nil
}

// kindForTarget returns OpOverwrite if hostFull exists, else OpCreate, plus
// whether the existing entry is a directory. A caller that is not planting a
// directory there gets isDir=true when applying will take that host subtree
// with it (reconcileDestination's os.RemoveAll), which the preview names.
func kindForTarget(hostFull string) (kind OpKind, isDir bool) {
	fi, err := os.Lstat(hostFull)
	if err != nil {
		return OpCreate, false
	}
	return OpOverwrite, fi.IsDir()
}

// isMigratable reports whether an upper entry has a form the applier can
// promote: a directory, a symlink, or a regular file.
//
// Everything else is a runtime artifact with no meaning on the host, and each
// one breaks Apply in its own way rather than migrating badly. A FIFO makes
// copyFile's O_RDONLY open block forever with no timeout and no message; a unix
// socket - `~/.claude/channels/matrix/mux.sock` is the one that turns up -
// fails ENXIO, which aborts Apply mid-plan and dies at the same operation on
// every re-run, against the resume Apply's doc comment promises. Uppers are
// fully sandbox-writable, so the sandbox chooses whether either exists. The
// krun copy-on-start overlay skips the same set for the same reason
// (cmd/devsandbox-shim/overlay.go).
func isMigratable(fi fs.FileInfo) bool {
	m := fi.Mode()
	return m.IsDir() || m.IsRegular() || m&os.ModeSymlink != 0
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
