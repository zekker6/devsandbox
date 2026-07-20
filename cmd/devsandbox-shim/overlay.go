package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const overlayLayersBase = "/tmp/.devsandbox-overlay-layers"

// prepareOverlayDirs creates upper and work directories for an overlay mount.
// Uses a hash of the target path to produce deterministic, unique dir names.
func prepareOverlayDirs(base, target string) (upper, work string, err error) {
	h := sha256.Sum256([]byte(target))
	name := hex.EncodeToString(h[:])[:16]

	upper = filepath.Join(base, name, "upper")
	work = filepath.Join(base, name, "work")

	if err := os.MkdirAll(upper, 0o755); err != nil {
		return "", "", fmt.Errorf("create overlay upper dir: %w", err)
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", "", fmt.Errorf("create overlay work dir: %w", err)
	}
	return upper, work, nil
}

// buildOverlayMountOptions builds the data string for mount(2) with overlay type.
func buildOverlayMountOptions(lowerdir, upperdir, workdir string) string {
	return "lowerdir=" + lowerdir + ",upperdir=" + upperdir + ",workdir=" + workdir
}

// splitOverlaySpec splits a "target:upper:work" spec. Uses last two colons
// to handle paths that may contain colons (unlikely but defensive).
func splitOverlaySpec(spec string) []string {
	last := len(spec) - 1
	secondColon := -1
	firstColon := -1
	for i := last; i >= 0; i-- {
		if spec[i] == ':' {
			if secondColon == -1 {
				secondColon = i
			} else {
				firstColon = i
				break
			}
		}
	}
	if firstColon == -1 || secondColon == -1 {
		return nil
	}
	return []string{spec[:firstColon], spec[firstColon+1 : secondColon], spec[secondColon+1:]}
}

func writeFile(path, content string) {
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		fatal("write %s: %v", path, err)
	}
}

// copyDir recursively copies src directory contents into dst.
// Preserves file permissions and symlinks. dst must already exist.
//
// Target subtrees that are themselves mount points are skipped: another binding
// owns that path. The krun copy-on-start overlay copies a Config dir (e.g.
// ~/.claude) whose target can have a nested binding mounted read-only beneath it
// (e.g. ~/.claude/projects); writing through that read-only mount would fail with
// EROFS and abort the launch, so the copy must leave it to the mount.
func copyDir(base, src, dst string) error {
	// Guard the whole path from base down to dst: dst is a NESTED copyoverlay
	// target under the persistent, guest-writable sandbox home (e.g.
	// ~/.config/Claude with nothing mounted at ~/.config), and a prior untrusted
	// run could have replaced dst OR any intermediate component with a symlink.
	// Without this, filepath.Join(dst, ...) writes would follow the link and
	// deposit source files outside the intended target.
	if err := ensureRealDirPath(base, dst); err != nil {
		return err
	}
	return copyDirExcluding(src, dst, mountPointSet())
}

// cleanCopyOverlayTarget removes stale content from a copyoverlay target before
// the fresh copy runs. On the copy-on-start backends (krun on any OS, and the
// Docker backend on macOS) a tmpoverlay dir cannot use kernel overlayfs (the
// libkrun guest rejects overlayfs over virtio-fs; Docker Desktop on macOS has no
// overlayfs), so it degrades to a copy into the *persistent* sandbox home. Both
// backends reach this code via copyoverlay manifest entries. Without a pre-clear,
// files a previous run wrote under the target (e.g. a hook planted in ~/.claude)
// survive into the next session because copyDir only writes source entries and
// never deletes extraneous ones. Resetting to the host source on every run
// restores the discard-on-exit semantics tmpoverlay promises.
func cleanCopyOverlayTarget(base, dst string) error {
	return cleanCopyOverlayTargetExcluding(base, dst, mountPointSet())
}

// cleanCopyOverlayTargetExcluding is cleanCopyOverlayTarget with an explicit set
// of target paths to preserve (paths that are separate mount points). Split out
// so the skip logic is testable without creating real mounts.
//
// It is mount-aware: a naive RemoveAll(dst) would hit a nested read-only mount
// (e.g. ~/.claude/projects) with EROFS/EBUSY and abort the launch. For each
// top-level child it removes the child outright when it holds no mount point,
// preserves the child when it IS a mount point, and recurses when a mount point
// lives somewhere beneath it - so both the mount point and every ancestor
// directory on the way to it survive while everything else is cleared.
func cleanCopyOverlayTargetExcluding(base, dst string, skip map[string]bool) error {
	// Full-path guard: dst is a NESTED copyoverlay target under the persistent,
	// guest-writable sandbox home. A prior untrusted run could have deleted dst OR
	// any intermediate component (e.g. ~/.config, where nothing is mounted) and
	// planted a symlink (or any non-directory) pointing at the project dir or
	// another read-write mount. The shim runs this clean as ROOT before dropping
	// privileges, so the os.ReadDir + os.RemoveAll walk below would FOLLOW such a
	// symlink and delete files OUTSIDE the target. ensureRealDirPath replaces any
	// symlinked component from base down to dst with a fresh empty directory before
	// the walk, so only the real target is ever read or removed. Child symlinks
	// under dst are handled inside cleanOverlayTree (e.IsDir() is false for a
	// symlink, so it is removed as a link, not traversed).
	if err := ensureRealDirPath(base, dst); err != nil {
		return err
	}
	return cleanOverlayTree(dst, skip)
}

// ensureRealDirPath guarantees that every path component strictly below base,
// down to and including dst, is a real directory - never a symlink or other
// non-directory. It is the full-path generalization of the leaf-only ensureRealDir.
//
// A leaf-only guard is insufficient because copyoverlay targets are NESTED under
// the sandbox home (e.g. ~/.config/Claude, with nothing mounted at ~/.config
// itself). os.Lstat FOLLOWS intermediate components, so a prior untrusted run that
// replaced an intermediate directory with a symlink (rm -rf ~/.config;
// ln -s <victim> ~/.config) slips past a leaf-only check: the leaf resolves
// through the link to a real directory. The shim runs the clean/copy as ROOT
// before dropping privileges, so a redirected intermediate would let
// RemoveAll/copy/chown operate outside the intended target - deleting or
// rewriting files under a host-backed read-write mount (e.g. the project dir).
//
// Each component is Lstat'd and repaired top-down via ensureRealDir, so an
// intermediate symlink is unlinked (its target untouched) and recreated as a real
// directory before the walk descends past it. base and everything above it are
// never inspected or modified: base is the trusted bind-mount root of the
// persistent sandbox home. Fails closed if dst is not strictly under base.
func ensureRealDirPath(base, dst string) error {
	base = filepath.Clean(base)
	dst = filepath.Clean(dst)

	rel, err := filepath.Rel(base, dst)
	if err != nil {
		return fmt.Errorf("overlay target %s not resolvable under sandbox home %s: %w", dst, base, err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("overlay target %s is not under sandbox home %s (fail-closed)", dst, base)
	}

	cur := base
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		if err := ensureRealDir(cur); err != nil {
			return err
		}
	}
	return nil
}

// ensureRealDir guarantees a single path component is a real directory before it
// is read, walked, or written THROUGH. Lstat (never Stat) inspects the entry
// itself, not its symlink target; any non-directory (symlink, regular file,
// socket, ...) is removed with os.Remove - which unlinks a symlink rather than
// following it - and recreated as a fresh empty directory. A missing entry is a
// no-op (first run): the caller creates it afterwards. Callers must invoke this
// top-down so every ancestor is already a real directory (see ensureRealDirPath);
// on its own it does not protect intermediate components.
func ensureRealDir(dst string) error {
	fi, err := os.Lstat(dst)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if fi.IsDir() {
		return nil
	}
	if err := os.Remove(dst); err != nil {
		return fmt.Errorf("remove non-directory overlay target %s: %w", dst, err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("recreate overlay target %s: %w", dst, err)
	}
	return nil
}

// cleanOverlayTree walks a target ROOT that ensureRealDir has already confirmed
// is a real directory, removing stale content while preserving mount points and
// the ancestor directories on the way to them. It recurses only into real
// directories (os.ReadDir's DirEntry.IsDir() does not follow symlinks), so no
// child symlink is ever traversed.
func cleanOverlayTree(dst string, skip map[string]bool) error {
	entries, err := os.ReadDir(dst)
	if err != nil {
		// Nothing to clean if the target does not exist yet (first run); copyDir
		// creates it. Any other error is real and must surface.
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}

	for _, e := range entries {
		child := filepath.Join(dst, e.Name())
		switch {
		case skip[child]:
			// child is itself a mount point: leave it and its subtree to the mount.
		case e.IsDir() && hasNestedSkip(child, skip):
			// A mount point lives beneath child: keep child, clean only its
			// non-mount content. e.IsDir() is false for symlinks, so a symlink is
			// never traversed - it falls through to RemoveAll below.
			if err := cleanOverlayTree(child, skip); err != nil {
				return err
			}
		default:
			if err := os.RemoveAll(child); err != nil {
				return fmt.Errorf("remove stale overlay entry %s: %w", child, err)
			}
		}
	}
	return nil
}

// hasNestedSkip reports whether any skip path is nested strictly below dir, i.e.
// dir is an ancestor directory of a mount point that must be preserved.
func hasNestedSkip(dir string, skip map[string]bool) bool {
	prefix := dir + string(filepath.Separator)
	for p := range skip {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// copyDirExcluding is copyDir with an explicit set of target paths to skip
// (paths that are separate mount points). Split out so the skip logic is
// testable without creating real mounts.
func copyDirExcluding(src, dst string, skip map[string]bool) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(dst, relPath)

		// Skip any target that is owned by another mount (the copy root itself is
		// never a mount point here). For a directory this prunes the whole subtree.
		if path != src && skip[targetPath] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Handle symlinks before IsDir check (symlinks to dirs would match)
		if d.Type()&fs.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("readlink %s: %w", path, err)
			}
			return os.Symlink(link, targetPath)
		}

		if d.IsDir() {
			return os.MkdirAll(targetPath, 0o755)
		}

		// Skip non-regular files (unix sockets, FIFOs, device nodes). These are
		// runtime artifacts - e.g. ~/.claude/channels/matrix/mux.sock - that
		// cannot be read as regular files (os.ReadFile returns ENXIO) and carry
		// no meaning inside the sandbox. Without this the krun copy-on-start
		// overlay aborts the whole launch when such a node exists in the source.
		if !d.Type().IsRegular() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		return os.WriteFile(targetPath, data, info.Mode().Perm())
	})
}
