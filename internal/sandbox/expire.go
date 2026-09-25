package sandbox

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"devsandbox/internal/worktree"
)

// ExpireResult reports what RemoveExpired did, by sandbox name.
type ExpireResult struct {
	// Removed are the expired sandboxes that were idle and are gone.
	Removed []string
	// Kept are expired sandboxes left in place because removing them would
	// take something the user may still want: a --worktree checkout, which can
	// hold uncommitted work, or Docker state, whose volumes an automatic sweep
	// must not decide about. `sandboxes prune` removes them on request.
	Kept []string
}

// expiryVerdict is what expiry decides about one sandbox.
type expiryVerdict int

const (
	expiryLive expiryVerdict = iota
	expiryKeep
	expiryRemove
)

// RemoveExpired removes every idle sandbox under baseDir created more than
// maxAge before now. A sandbox in use is skipped silently: it is still expired
// and goes on the first launch that finds it idle.
//
// The verdict is taken twice, once to pick candidates and again under the
// sandbox's exclusive lock. The first read is stale by the time the lock is
// held: a launch of that project may have removed and recreated the sandbox
// in between, and the fresh one must not go on the old one's creation time.
// A sandbox without readable metadata is never expired, because its age is
// unknown and an unknown age must not authorize a deletion.
func RemoveExpired(baseDir string, maxAge time.Duration, now time.Time) (ExpireResult, error) {
	var res ExpireResult
	sandboxes, err := ListSandboxes(baseDir)
	if err != nil {
		return res, err
	}

	var errs []error
	for _, m := range sandboxes {
		root := m.SandboxRoot
		switch expiryOf(root, maxAge, now) {
		case expiryLive:
			continue
		case expiryKeep:
			res.Kept = append(res.Kept, m.Name)
			continue
		}
		removed, err := RemoveSandboxIfIdleWhen(root, func() bool {
			return expiryOf(root, maxAge, now) == expiryRemove
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("remove expired sandbox %s: %w", m.Name, err))
		}
		if removed {
			res.Removed = append(res.Removed, m.Name)
		}
	}
	return res, errors.Join(errs...)
}

func expiryOf(sandboxRoot string, maxAge time.Duration, now time.Time) expiryVerdict {
	m, err := LoadMetadata(sandboxRoot)
	if err != nil || m.CreatedAt.IsZero() || now.Sub(m.CreatedAt) <= maxAge {
		return expiryLive
	}
	if m.Isolation == IsolationDocker || holdsWorktrees(sandboxRoot) {
		return expiryKeep
	}
	return expiryRemove
}

// holdsWorktrees reports whether the sandbox has any --worktree checkout. A
// directory that cannot be read counts as holding one.
func holdsWorktrees(sandboxRoot string) bool {
	f, err := os.Open(worktree.WorktreesDir(sandboxRoot))
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	defer func() { _ = f.Close() }()
	_, err = f.Readdirnames(1)
	return !errors.Is(err, io.EOF)
}
