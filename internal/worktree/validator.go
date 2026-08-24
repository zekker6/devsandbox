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

// ErrUnmappableWorktree is returned by LinkedRepoRoot for a linked worktree
// whose shared git directory is not a repository's ".git" - a submodule's
// worktree, or a checkout driven by an explicit GIT_DIR. There is no
// repository root to mount for those, so the caller reports the gap rather
// than binding a directory that is not one.
var ErrUnmappableWorktree = errors.New("linked worktree has no mountable repository root")

// LinkedRepoRoot returns the main repository root when dir sits inside a
// linked git worktree, and "" when it does not (an ordinary checkout, or no
// repository at all - neither is an error, since every launch calls this).
//
// The test is `--git-dir` != `--git-common-dir`. A linked worktree keeps its
// own HEAD and index under <common>/worktrees/<name> while sharing the object
// store through the common dir; no other repository shape separates the two.
// The two are compared symlink-resolved, because git spells them relative
// whenever it can: from a subdirectory of an ordinary checkout they come back
// as "/repo/.git" and "../.git", which is one directory written two ways and
// would otherwise read as a worktree.
//
// The root is returned in git's own spelling. It is what gets bind-mounted,
// and what has to land on it is the `gitdir:` line git wrote into the
// worktree's .git file - which git derives from this same common dir, so
// taking it verbatim is what keeps mount and pointer in step. Resolution
// stays on the comparison, where it is load-bearing.
func LinkedRepoRoot(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--git-dir", "--git-common-dir")
	out, err := cmd.Output()
	if err != nil {
		// Not a repository, or no git binary. Both mean "nothing to mount";
		// a launch outside a repo is ordinary and must not be blocked.
		return "", nil
	}

	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 2 {
		return "", nil
	}

	gitDir := absGitPath(dir, lines[0])
	commonDir := absGitPath(dir, lines[1])
	if evalSymlinks(gitDir) == evalSymlinks(commonDir) {
		return "", nil
	}

	// The common dir is what gets mounted, and git.go mounts it as
	// <root>/.git - so a common dir under any other name has no root to
	// derive and is reported instead of guessed at.
	if filepath.Base(commonDir) != ".git" {
		return "", fmt.Errorf("%w: shared git directory is %s", ErrUnmappableWorktree, commonDir)
	}
	return filepath.Dir(commonDir), nil
}

// absGitPath turns a path git reported relative to dir into an absolute one,
// leaving any symlinks in it as git spelled them.
func absGitPath(dir, p string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return filepath.Clean(p)
}

// evalSymlinks resolves p for comparison. Best-effort: a path that cannot be
// resolved is still comparable in its cleaned form.
func evalSymlinks(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}
