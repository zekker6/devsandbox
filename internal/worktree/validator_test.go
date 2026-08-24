package worktree

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateBranch(t *testing.T) {
	good := []string{"feature", "feat/login", "user/foo-bar", "devsandbox/x"}
	for _, b := range good {
		if err := ValidateBranch(b); err != nil {
			t.Errorf("ValidateBranch(%q) unexpected err: %v", b, err)
		}
	}
	bad := []string{"", ".", "..", "-x", "x/", "/x", "x//y", "x..y", "x y", "x\tx", "@{x}"}
	for _, b := range bad {
		if err := ValidateBranch(b); err == nil {
			t.Errorf("ValidateBranch(%q) = nil, want error", b)
		}
	}
}

func TestValidateRef(t *testing.T) {
	good := []string{"HEAD", "HEAD~3", "v1.0^", "origin/main", "abc123", "refs/tags/v2.0"}
	for _, r := range good {
		if err := ValidateRef(r); err != nil {
			t.Errorf("ValidateRef(%q) unexpected err: %v", r, err)
		}
	}
	bad := []string{"", "-x", "x y", "x\tx"}
	for _, r := range bad {
		if err := ValidateRef(r); err == nil {
			t.Errorf("ValidateRef(%q) = nil, want error", r)
		}
	}
}

func TestValidateOptions(t *testing.T) {
	cases := []struct {
		name    string
		opts    Options
		wantErr string
	}{
		{"ok readonly", Options{Enabled: true, GitMode: "readonly"}, ""},
		{"ok readwrite", Options{Enabled: true, GitMode: "readwrite"}, ""},
		{"disabled rejected", Options{Enabled: true, GitMode: "disabled"}, "git-mode=disabled"},
		{"not enabled is ok", Options{Enabled: false, GitMode: "disabled"}, ""},
		{"bad branch", Options{Enabled: true, GitMode: "readonly", Branch: ".."}, "branch"},
		{"base with tilde ok", Options{Enabled: true, GitMode: "readonly", Base: "HEAD~3"}, ""},
		{"base with caret ok", Options{Enabled: true, GitMode: "readonly", Base: "v1.0^"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.opts)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want contains %q", err, tc.wantErr)
			}
		})
	}
}

func TestRepoRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "git", "add", "README")
	run(t, dir, "git", "-c", "user.email=a@b", "-c", "user.name=a", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "init")

	got, err := RepoRoot(dir)
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	wantReal, _ := filepath.EvalSymlinks(dir)
	gotReal, _ := filepath.EvalSymlinks(got)
	if gotReal != wantReal {
		t.Errorf("RepoRoot = %q, want %q", gotReal, wantReal)
	}

	other := t.TempDir()
	if _, err := RepoRoot(other); err == nil {
		t.Errorf("RepoRoot(non-repo) = nil, want error")
	}
}

func TestRepoRootFromWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// Create a main repo.
	repo := t.TempDir()
	run(t, repo, "git", "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "git", "add", "README")
	run(t, repo, "git", "-c", "user.email=a@b", "-c", "user.name=a", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "init")

	// Create a worktree.
	wtDir := filepath.Join(t.TempDir(), "wt")
	run(t, repo, "git", "worktree", "add", "-b", "test-wt", wtDir, "HEAD")

	// RepoRoot from inside the worktree must return the main repo root.
	got, err := RepoRoot(wtDir)
	if err != nil {
		t.Fatalf("RepoRoot(worktree): %v", err)
	}
	wantReal, _ := filepath.EvalSymlinks(repo)
	gotReal, _ := filepath.EvalSymlinks(got)
	if gotReal != wantReal {
		t.Errorf("RepoRoot(worktree) = %q, want main repo %q", gotReal, wantReal)
	}
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// initRepo creates a repository with one commit and returns its path.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "git", "add", "README")
	run(t, dir, "git", "-c", "user.email=a@b", "-c", "user.name=a", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "init")
	return dir
}

func TestLinkedRepoRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	repo := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	run(t, repo, "git", "worktree", "add", "-q", "-b", "feat", wt, "HEAD")

	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	wtSub := filepath.Join(wt, "sub")
	if err := os.MkdirAll(wtSub, 0o755); err != nil {
		t.Fatal(err)
	}

	real := func(p string) string {
		if p == "" {
			return ""
		}
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			return p
		}
		return resolved
	}

	tests := []struct {
		name string
		dir  string
		want string
	}{
		{"worktree root", wt, real(repo)},
		// git reports --git-dir absolute and --git-common-dir relative from a
		// subdirectory, so this is the case a spelling-level comparison of the
		// two would misread as a worktree.
		{"subdirectory of ordinary checkout", sub, ""},
		{"ordinary checkout root", repo, ""},
		{"subdirectory of worktree", wtSub, real(repo)},
		{"not a repository", t.TempDir(), ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LinkedRepoRoot(tc.dir)
			if err != nil {
				t.Fatalf("LinkedRepoRoot(%s): %v", tc.dir, err)
			}
			if real(got) != tc.want {
				t.Errorf("LinkedRepoRoot(%s) = %q, want %q", tc.dir, got, tc.want)
			}
		})
	}
}

// A submodule's worktree shares a git dir under .git/modules/<name>, which has
// no repository root to mount. It must be reported, not silently treated as an
// ordinary checkout and left with broken git.
func TestLinkedRepoRootUnmappable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	inner := initRepo(t)
	super := initRepo(t)
	run(t, super, "git", "-c", "protocol.file.allow=always", "-c", "user.email=a@b", "-c", "user.name=a",
		"-c", "commit.gpgsign=false", "submodule", "add", "-q", inner, "sub")

	subWt := filepath.Join(t.TempDir(), "subwt")
	run(t, filepath.Join(super, "sub"), "git", "worktree", "add", "-q", "-b", "feat", subWt, "HEAD")

	got, err := LinkedRepoRoot(subWt)
	if !errors.Is(err, ErrUnmappableWorktree) {
		t.Fatalf("err = %v, want ErrUnmappableWorktree", err)
	}
	if got != "" {
		t.Errorf("root = %q, want empty", got)
	}
}

// git reports --git-dir resolved and --git-common-dir relative, so a checkout
// reached through a symlink hands back two spellings of one directory. Compared
// as written they differ, and since the common dir is still named ".git" the
// misdetection is silent: an ordinary checkout would be treated as a worktree
// and get a bogus mount.
func TestLinkedRepoRootSymlinkedCheckoutIsNotAWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "real"), filepath.Join(base, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	repo := filepath.Join(base, "link", "repo")
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "git", "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "git", "add", "README")
	run(t, repo, "git", "-c", "user.email=a@b", "-c", "user.name=a", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "init")

	got, err := LinkedRepoRoot(filepath.Join(repo, "sub"))
	if err != nil {
		t.Fatalf("LinkedRepoRoot: %v", err)
	}
	if got != "" {
		t.Errorf("LinkedRepoRoot(symlinked subdir of ordinary checkout) = %q, want empty", got)
	}
}
