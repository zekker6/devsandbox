package worktree

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Options are the inputs the validator cares about. `cmd/devsandbox` constructs
// this from raw flag state before calling Validate.
type Options struct {
	// Enabled is true when --worktree was passed (with or without a value).
	Enabled bool
	// Branch is the explicit branch from --worktree=<branch>, or empty for auto.
	Branch string
	// Base is --worktree-base. Empty defaults to HEAD.
	Base string
	// GitMode is the resolved git-mode string ("readonly", "readwrite", "disabled").
	GitMode string
}

// Validate returns a non-nil error when the flag combination is incoherent.
func Validate(o Options) error {
	if !o.Enabled {
		return nil
	}
	if o.GitMode == "disabled" {
		return errors.New("--worktree cannot be combined with --git-mode=disabled (no git metadata would be accessible)")
	}
	if o.Branch != "" {
		if err := ValidateBranch(o.Branch); err != nil {
			return fmt.Errorf("invalid --worktree branch %q: %w", o.Branch, err)
		}
	}
	if o.Base != "" {
		if err := ValidateRef(o.Base); err != nil {
			return fmt.Errorf("invalid --worktree-base %q: %w", o.Base, err)
		}
	}
	return nil
}

// ValidateBranch checks branch-name sanity. Catches common footguns before
// shelling out to git; NOT a full reimpl of git-check-ref-format.
func ValidateBranch(name string) error {
	if name == "" {
		return errors.New("empty branch name")
	}
	if name == "." || name == ".." {
		return errors.New("dot/double-dot is not a valid branch name")
	}
	if strings.HasPrefix(name, "-") {
		return errors.New("branch name cannot start with '-'")
	}
	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
		return errors.New("branch name cannot start or end with '/'")
	}
	if strings.Contains(name, "//") || strings.Contains(name, "..") {
		return errors.New("branch name cannot contain '//' or '..'")
	}
	for _, r := range name {
		if r == ' ' || r == '\t' || r == '\n' || r < 0x20 || r == 0x7f {
			return errors.New("branch name contains whitespace or control character")
		}
		switch r {
		case '~', '^', ':', '?', '*', '[', '\\':
			return fmt.Errorf("branch name contains forbidden character %q", r)
		}
	}
	if strings.Contains(name, "@{") {
		return errors.New("branch name cannot contain '@{'")
	}
	return nil
}

// ValidateRef checks ref sanity for --worktree-base. More permissive than
// ValidateBranch: allows ~, ^, and : which are valid in git refspecs (e.g.
// HEAD~3, v1.0^, origin/main). Only rejects whitespace, control characters,
// and obviously dangerous patterns.
func ValidateRef(ref string) error {
	if ref == "" {
		return errors.New("empty ref")
	}
	for _, r := range ref {
		if r == ' ' || r == '\t' || r == '\n' || r < 0x20 || r == 0x7f {
			return errors.New("ref contains whitespace or control character")
		}
	}
	if strings.HasPrefix(ref, "-") {
		return errors.New("ref cannot start with '-'")
	}
	return nil
}

// RepoRoot returns the main repository root for the git repo containing dir.
// When dir is inside a worktree, this still returns the main repo root (not
// the worktree root) by using --git-common-dir to find the real .git metadata.
// The returned path is symlink-resolved for reliable string comparison.
func RepoRoot(dir string) (string, error) {
	// --git-common-dir returns the path to the shared .git directory. For a
	// regular checkout this is just ".git"; for a worktree it is something like
	// "/path/to/main-repo/.git". We use this to derive the true repo root.
	commonCmd := exec.Command("git", "-C", dir, "rev-parse", "--git-common-dir")
	commonOut, err := commonCmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git repository (git rev-parse failed)", dir)
	}
	commonDir := strings.TrimRight(string(commonOut), "\n")

	// If the path is relative, resolve it relative to dir.
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(dir, commonDir)
	}
	commonDir = filepath.Clean(commonDir)

	// The common dir is the .git directory; the repo root is its parent.
	repoRoot := filepath.Dir(commonDir)

	// Normalize symlinks for reliable comparison.
	resolved, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		return repoRoot, nil
	}
	return resolved, nil
}
