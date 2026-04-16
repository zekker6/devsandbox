package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Manager runs `git worktree` on the host. Zero-value is usable; NewManager
// exists for symmetry and future DI.
type Manager struct{}

// NewManager returns a new Manager.
func NewManager() *Manager { return &Manager{} }

// Handle describes a worktree known to git.
type Handle struct {
	Path    string // absolute host path of the working tree
	Branch  string // branch checked out in the worktree
	Created bool   // true if this Ensure call created the worktree
}

// EnsureRequest is the input to Manager.Ensure.
type EnsureRequest struct {
	RepoRoot    string // main repo root (git rev-parse --show-toplevel)
	SandboxRoot string // per-project devsandbox state dir
	Branch      string // branch name to create or reuse; must be non-empty
	Base        string // base ref when creating; empty means HEAD
}

// Ensure guarantees that a worktree for req.Branch exists at the canonical
// path under req.SandboxRoot. If the path already exists and git knows about
// it (pointing at req.Branch), we reuse. If the path exists but git does not
// know about it, we fail — caller must intervene, we do not clobber.
func (m *Manager) Ensure(ctx context.Context, req EnsureRequest) (*Handle, error) {
	if req.RepoRoot == "" || req.SandboxRoot == "" || req.Branch == "" {
		return nil, fmt.Errorf("worktree: Ensure requires RepoRoot, SandboxRoot, Branch")
	}
	path := WorktreePath(req.SandboxRoot, req.Branch)

	// Normalize early so the Handle always carries a canonical path matching
	// what git records internally (important on macOS where /tmp → /private/tmp).
	// normalizePath falls back to filepath.Clean when the target does not yet exist.
	path, err := normalizePath(path)
	if err != nil {
		return nil, fmt.Errorf("worktree: normalize path: %w", err)
	}

	existing, err := m.findWorktree(ctx, req.RepoRoot, path)
	if err != nil {
		return nil, err
	}
	pathExists := fileExists(path)

	switch {
	case existing != nil:
		if existing.Branch != req.Branch {
			return nil, fmt.Errorf("worktree: path %s is already registered to branch %q (wanted %q); resolve with 'git worktree remove %s' or pick a different branch", path, existing.Branch, req.Branch, path)
		}
		return &Handle{Path: path, Branch: req.Branch, Created: false}, nil

	case pathExists:
		return nil, fmt.Errorf("worktree: stale directory at %s (git has no record). Remove it manually or pick another branch; refusing to clobber", path)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("worktree: create parent dir: %w", err)
	}

	branchExists, err := m.branchExists(ctx, req.RepoRoot, req.Branch)
	if err != nil {
		return nil, err
	}

	var argv []string
	if branchExists {
		argv = []string{"-C", req.RepoRoot, "worktree", "add", path, req.Branch}
	} else {
		base := req.Base
		if base == "" {
			base = "HEAD"
		}
		argv = []string{"-C", req.RepoRoot, "worktree", "add", "-b", req.Branch, path, base}
	}
	cmd := exec.CommandContext(ctx, "git", argv...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("worktree: git %s failed: %w\n%s", strings.Join(argv, " "), err, strings.TrimSpace(string(out)))
	}

	// Re-normalize now that the directory exists — on macOS the pre-creation
	// normalization falls back to filepath.Clean (e.g. /var/folders/...) but
	// post-creation EvalSymlinks resolves to /private/var/folders/... .
	if resolved, err := normalizePath(path); err == nil {
		path = resolved
	}

	return &Handle{Path: path, Branch: req.Branch, Created: true}, nil
}

// Remove removes the worktree at path. Best-effort: tries
// `git worktree remove --force` then `git worktree prune`. Callers should
// log on error rather than failing outright.
func (m *Manager) Remove(ctx context.Context, repoRoot, path string) error {
	if repoRoot == "" || path == "" {
		return fmt.Errorf("worktree: Remove requires repoRoot and path")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "worktree", "remove", "--force", path)
	removeOut, removeErr := cmd.CombinedOutput()

	pruneCmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "worktree", "prune")
	pruneOut, pruneErr := pruneCmd.CombinedOutput()

	if removeErr != nil && fileExists(path) {
		return fmt.Errorf("worktree: git worktree remove failed: %w\n%s\nprune: %v %s", removeErr, strings.TrimSpace(string(removeOut)), pruneErr, strings.TrimSpace(string(pruneOut)))
	}
	return nil
}

// existingWorktree describes an entry returned by `git worktree list --porcelain`.
type existingWorktree struct {
	Path   string
	Branch string
}

func (m *Manager) findWorktree(ctx context.Context, repoRoot, path string) (*existingWorktree, error) {
	// Normalise the target path so symlink-heavy temp dirs (e.g. /tmp →
	// /private/tmp on macOS) compare correctly against git's canonical output.
	normPath, err := normalizePath(path)
	if err != nil {
		return nil, fmt.Errorf("worktree: normalize path %s: %w", path, err)
	}

	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("worktree: git worktree list: %w", err)
	}
	for _, block := range strings.Split(string(out), "\n\n") {
		var cur existingWorktree
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				raw := strings.TrimPrefix(line, "worktree ")
				norm, err := normalizePath(raw)
				if err == nil {
					cur.Path = norm
				} else {
					cur.Path = raw
				}
			case strings.HasPrefix(line, "branch "):
				ref := strings.TrimPrefix(line, "branch ")
				cur.Branch = strings.TrimPrefix(ref, "refs/heads/")
			}
		}
		if cur.Path == normPath {
			return &cur, nil
		}
	}
	return nil, nil
}

func (m *Manager) branchExists(ctx context.Context, repoRoot, branch string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("worktree: show-ref %s: %w", branch, err)
}

// normalizePath resolves symlinks for an existing path, or returns the
// cleaned path if the entry does not yet exist (so callers can compare
// before creation).
func normalizePath(p string) (string, error) {
	norm, err := filepath.EvalSymlinks(p)
	if err != nil {
		if os.IsNotExist(err) {
			return filepath.Clean(p), nil
		}
		return "", err
	}
	return norm, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
