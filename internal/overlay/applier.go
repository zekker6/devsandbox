package overlay

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

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
	switch op.Kind {
	case OpDelete:
		return os.RemoveAll(op.HostPath)
	case OpCreate, OpOverwrite:
		if err := os.MkdirAll(filepath.Dir(op.HostPath), 0o755); err != nil {
			return err
		}
		if op.IsSymlink {
			_ = os.Remove(op.HostPath) // ignore if missing
			return os.Symlink(op.LinkTarget, op.HostPath)
		}
		if op.IsDir {
			return os.MkdirAll(op.HostPath, op.Mode)
		}
		return copyFile(op.Source, op.HostPath, op.Mode, op.ModTime)
	}
	return fmt.Errorf("unknown op kind: %v", op.Kind)
}

func copyFile(src, dst string, mode os.FileMode, mtime time.Time) (retErr error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if err := in.Close(); err != nil && retErr == nil {
			retErr = err
		}
	}()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Chmod(dst, mode); err != nil {
		return err
	}
	return os.Chtimes(dst, mtime, mtime)
}
