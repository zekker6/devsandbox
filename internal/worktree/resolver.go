// Package worktree owns host-side git worktree orchestration used by the
// devsandbox --worktree flag. It is deliberately small and has no dependency
// on the sandbox package.
package worktree

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"time"
)

// AutoBranchPrefix is the branch-name prefix used for auto-generated branches.
const AutoBranchPrefix = "devsandbox/"

// SanitizeLeaf turns a git branch name into a filesystem-safe directory leaf.
// Slashes become dashes, the result is lowercased (for case-insensitive FS
// safety on macOS), and a short hash of the original name is appended to avoid
// collisions (e.g. "feat/login" vs "feat-login" map to distinct leaves).
func SanitizeLeaf(branch string) string {
	leaf := strings.ReplaceAll(branch, "/", "-")
	leaf = strings.Trim(leaf, "-")
	for strings.Contains(leaf, "--") {
		leaf = strings.ReplaceAll(leaf, "--", "-")
	}
	leaf = strings.ToLower(leaf)
	if leaf == "" {
		leaf = "worktree"
	}

	// Append a short hash of the original (case-preserved) branch name so
	// that branches differing only in case or slash-vs-dash produce distinct
	// filesystem paths.
	h := sha256.Sum256([]byte(branch))
	return leaf + "-" + hex.EncodeToString(h[:4])
}

// AutoBranchName returns the auto-generated branch name for a given session
// identifier. Uses the current UTC time when session is empty.
func AutoBranchName(session string) string {
	return AutoBranchNameAt(session, time.Now().UTC())
}

// AutoBranchNameAt is AutoBranchName with an injectable clock.
func AutoBranchNameAt(session string, now time.Time) string {
	if session != "" {
		return AutoBranchPrefix + session
	}
	return AutoBranchPrefix + now.Format("20060102-150405")
}

// WorktreePath returns the directory where a worktree for branch lives
// under the given sandbox-project root.
func WorktreePath(sandboxRoot, branch string) string {
	return filepath.Join(sandboxRoot, "worktrees", SanitizeLeaf(branch))
}
