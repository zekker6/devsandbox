package overlay

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// BuildPlan walks the upper sources in order and produces the set of
// operations needed to promote their merged state to hostPath.
//
// Later sources in the slice override earlier ones on the same relpath
// (last-write-wins by caller-supplied order). Character-device files with
// rdev=0 in any upper are treated as overlayfs whiteouts and produce
// OpDelete. Symlinks are preserved as symlinks. If a stat in the upper
// returns EPERM, the entry is silently skipped (never invented as a
// spurious delete).
func BuildPlan(sources []UpperSource, hostPath string) (Plan, error) {
	type entry struct {
		source     UpperSource
		rel        string
		fi         fs.FileInfo
		isWhiteout bool
	}
	merged := map[string]entry{} // relpath -> winning entry

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
			merged[rel] = entry{source: src, rel: rel, fi: fi, isWhiteout: isWhiteout}
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

	for rel, e := range merged {
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
