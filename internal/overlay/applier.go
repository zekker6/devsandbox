package overlay

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ErrUnsafeDestination is returned when an operation's destination cannot be
// proven to stay inside the host path the plan named — a symlink among the
// components below the migration root, or a host path that is not that root
// joined with the operation's relative path.
var ErrUnsafeDestination = errors.New("unsafe migration destination")

// Apply executes the operations in the plan against the host filesystem.
// Not transactional: on first error, returns with already-applied ops left in place.
// Apply is idempotent — re-running after a partial failure resumes correctly.
func Apply(plan Plan) error {
	for i, op := range plan.Operations {
		if err := applyOne(op); err != nil {
			return fmt.Errorf("operation %d (%s %s): %w", i, op.Kind, op.RelPath, err)
		}
	}
	return nil
}

func applyOne(op Operation) error {
	if err := checkDestination(op); err != nil {
		return err
	}
	switch op.Kind {
	case OpDelete:
		return os.RemoveAll(op.HostPath)
	case OpCreate, OpOverwrite:
		if err := os.MkdirAll(filepath.Dir(op.HostPath), 0o755); err != nil {
			return err
		}
		if err := reconcileDestination(op); err != nil {
			return err
		}
		if op.IsSymlink {
			return os.Symlink(op.LinkTarget, op.HostPath)
		}
		if op.IsDir {
			return os.MkdirAll(op.HostPath, op.Mode)
		}
		return copyFile(op.Source, op.HostPath, op.Mode, op.ModTime)
	}
	return fmt.Errorf("unknown op kind: %v", op.Kind)
}

// reconcileDestination removes an existing destination whose entry type the
// operation's own action cannot replace. An overlay records a type change as an
// rm plus a recreate at the same name, and BuildPlan classifies any existing
// target as OpOverwrite without comparing types — so without this a directory
// over a file fails ENOTDIR, a file over a directory fails renaming onto an
// existing directory, and a symlink over a non-empty directory fails EEXIST.
// Each aborts Apply with earlier operations already committed, and every retry
// dies at the same operation, against the resume its doc comment promises.
//
// A same-type destination is left alone: an existing directory keeps the host
// contents no operation in the plan recreates, and copyFile's rename replaces a
// regular file atomically. The exception is a symlink under a directory
// operation — MkdirAll follows it silently, so the migrated tree would land
// wherever it points, outside the host path the preview named.
func reconcileDestination(op Operation) error {
	fi, err := os.Lstat(op.HostPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	switch {
	case op.IsSymlink:
		// os.Symlink refuses to create over any existing entry.
	case op.IsDir:
		if fi.IsDir() {
			return nil
		}
	default:
		if !fi.IsDir() {
			// copyFile renames over a regular file or a symlink without
			// following it; only a directory is in the way.
			return nil
		}
	}
	if fi.IsDir() {
		return os.RemoveAll(op.HostPath)
	}
	return os.Remove(op.HostPath)
}

// checkDestination refuses an operation that would traverse a symlink below
// the migration root. The root itself is left alone: it is the host path the
// user named, and a symlink there is their own arrangement. Every component
// underneath it must be a real directory, otherwise MkdirAll follows the link
// and the write lands wherever it points — outside the path the preview showed.
//
// The destination's own final component is not checked here: copyFile replaces
// it atomically without opening it, and reconcileDestination unlinks it when
// the operation's action would follow it, so a symlink there is overwritten
// rather than written through.
func checkDestination(op Operation) error {
	root, rel, err := splitRoot(op)
	if err != nil {
		return err
	}
	parts := strings.Split(rel, string(filepath.Separator))
	cur := root
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		fi, statErr := os.Lstat(cur)
		if statErr != nil {
			if errors.Is(statErr, fs.ErrNotExist) {
				// Nothing below this exists yet, so there is nothing to
				// traverse: MkdirAll creates real directories from here down.
				return nil
			}
			return statErr
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: parent component %q is a symlink", ErrUnsafeDestination, cur)
		}
	}
	return nil
}

// splitRoot recovers an operation's migration root and cleaned relative path.
// BuildPlan sets HostPath to filepath.Join(root, RelPath), so trimming the
// relative path yields the root. It is derived per operation rather than read
// from Plan.HostPath because `overlay migrate` aggregates several BuildPlan
// results into one plan whose HostPath field holds only the last path.
func splitRoot(op Operation) (root, rel string, err error) {
	rel = filepath.Clean(op.RelPath)
	host := filepath.Clean(op.HostPath)
	suffix := string(filepath.Separator) + rel
	if rel == "." || rel == string(filepath.Separator) || !strings.HasSuffix(host, suffix) {
		return "", "", fmt.Errorf("%w: host path %q is not %q under a root", ErrUnsafeDestination, op.HostPath, op.RelPath)
	}
	root = strings.TrimSuffix(host, suffix)
	if root == "" {
		root = string(filepath.Separator)
	}
	return root, rel, nil
}

func copyFile(src, dst string, mode os.FileMode, mtime time.Time) (retErr error) {
	// O_NOFOLLOW: the planner classified src with Lstat, so a symlink here
	// means the upper changed between planning and application.
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() {
		if err := in.Close(); err != nil && retErr == nil {
			retErr = err
		}
	}()

	// Write a fresh entry beside the destination and rename over it. dst is
	// never opened, so a symlink sitting there is replaced rather than
	// truncated and written through, and no reader sees a half-copied file.
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".devsandbox-migrate-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if retErr != nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	if err := os.Chtimes(tmpName, mtime, mtime); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}
