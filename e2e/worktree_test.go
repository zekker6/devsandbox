package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"devsandbox/internal/sandbox"
	"devsandbox/internal/worktree"
)

// setupWorktreeTest initialises a fresh git repo in a temp dir and returns a
// helper that runs devsandbox with --worktree flags from that repo's root.
// It also creates an isolated sandbox base_path so the real user state is
// never touched.
//
// The caller must check bwrapAvailable() and git availability before calling
// this function — it does not skip on its own.
func setupWorktreeTest(t *testing.T) (repoDir string, run func(args ...string) *exec.Cmd) {
	t.Helper()

	repoDir = t.TempDir()

	// Initialise a minimal git repo with an initial commit so HEAD exists.
	for _, argv := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@test.local"},
		{"config", "user.name", "Test"},
	} {
		c := exec.Command("git", append([]string{"-C", repoDir}, argv...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", argv, err, out)
		}
	}
	readmePath := filepath.Join(repoDir, "README")
	if err := os.WriteFile(readmePath, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"add", "README"},
		{"commit", "-m", "init"},
	} {
		c := exec.Command("git", append([]string{"-C", repoDir}, argv...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", argv, err, out)
		}
	}

	// Isolated sandbox base: one temp dir for XDG config, one for sandbox state.
	configDir := t.TempDir()
	sandboxBase := t.TempDir()

	configPath := filepath.Join(configDir, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	configContent := fmt.Sprintf("[sandbox]\nbase_path = %q\n", sandboxBase)
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatal(err)
	}

	run = func(args ...string) *exec.Cmd {
		c := exec.Command(binaryPath, args...)
		c.Dir = repoDir
		c.Env = append(os.Environ(), "XDG_CONFIG_HOME="+configDir)
		return c
	}
	return repoDir, run
}

// gitIsAvailable returns true when git is on PATH.
func gitIsAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// insideSandbox returns true when the test process itself is running inside a
// devsandbox session. In that case all tests that invoke the devsandbox binary
// will be rejected by the recursive-sandboxing guard.
func insideSandbox() bool {
	return os.Getenv("DEVSANDBOX") == "1"
}

// headCommit returns the HEAD commit hash for the given repo dir.
func headCommit(t *testing.T, repoDir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD in %s: %v", repoDir, err)
	}
	return strings.TrimSpace(string(out))
}

// branchHead returns the HEAD commit of a named branch, or empty string if it
// does not exist.
func branchHead(t *testing.T, repoDir, branch string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repoDir, "rev-parse", "refs/heads/"+branch).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// TestWorktree_CreatesAndEntersBwrap verifies that --worktree=<branch> sets the
// working directory inside the sandbox to the worktree path (which ends with
// /worktrees/<leaf>).
func TestWorktree_CreatesAndEntersBwrap(t *testing.T) {
	if insideSandbox() {
		t.Skip("cannot launch nested devsandbox from inside a sandbox session")
	}
	if !bwrapAvailable() {
		t.Skip("bwrap not available or user namespaces disabled")
	}
	if !gitIsAvailable() {
		t.Skip("git not available")
	}

	_, run := setupWorktreeTest(t)

	cmd := run("--worktree=feat", "--", "pwd")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("devsandbox --worktree=feat -- pwd failed: %v\nOutput: %s", err, out)
	}
	if !strings.Contains(string(out), "/worktrees/feat-") {
		t.Errorf("expected pwd to contain /worktrees/feat-<hash>, got: %s", out)
	}
}

// TestWorktree_CommitBlockedUnderReadonly verifies that with the default
// readonly git mode a commit inside the sandbox fails, and that no commit
// lands on the worktree branch.
func TestWorktree_CommitBlockedUnderReadonly(t *testing.T) {
	if insideSandbox() {
		t.Skip("cannot launch nested devsandbox from inside a sandbox session")
	}
	if !bwrapAvailable() {
		t.Skip("bwrap not available or user namespaces disabled")
	}
	if !gitIsAvailable() {
		t.Skip("git not available")
	}

	repoDir, run := setupWorktreeTest(t)

	beforeMain := headCommit(t, repoDir)

	// Run a commit attempt inside the sandbox; the whole invocation should
	// fail because the child command fails.
	cmd := run("--worktree=blocked", "--",
		"sh", "-c",
		"echo y >> README && git add README && git -c user.email=t@t -c user.name=t commit -m x",
	)
	out, err := cmd.CombinedOutput()

	// The child command must have failed (readonly overlay blocks the write or
	// git itself reports a failure).
	if err == nil {
		t.Errorf("expected commit to fail under readonly mode, but it succeeded\nOutput: %s", out)
	}

	// The main branch HEAD must be unchanged.
	afterMain := headCommit(t, repoDir)
	if afterMain != beforeMain {
		t.Errorf("main HEAD changed: before=%s after=%s", beforeMain, afterMain)
	}

	// The worktree branch (if it was created) must not have a new commit
	// beyond the base.
	wtBranchHead := branchHead(t, repoDir, "blocked")
	if wtBranchHead != "" && wtBranchHead != beforeMain {
		t.Errorf("worktree branch 'blocked' advanced unexpectedly to %s", wtBranchHead)
	}
}

// TestWorktree_CommitLandsOnBranchUnderReadwrite verifies that with
// --git-mode=readwrite a commit inside the sandbox:
//   - lands on the worktree branch
//   - does NOT change the main branch HEAD
//   - does NOT leave new files in the main checkout
func TestWorktree_CommitLandsOnBranchUnderReadwrite(t *testing.T) {
	if insideSandbox() {
		t.Skip("cannot launch nested devsandbox from inside a sandbox session")
	}
	if !bwrapAvailable() {
		t.Skip("bwrap not available or user namespaces disabled")
	}
	if !gitIsAvailable() {
		t.Skip("git not available")
	}

	repoDir, run := setupWorktreeTest(t)

	mainBefore := headCommit(t, repoDir)

	cmd := run(
		"--worktree=rw-branch",
		"--git-mode=readwrite",
		"--",
		"sh", "-c",
		"echo added > new_file.txt && git add new_file.txt && git -c user.email=t@t -c user.name=t commit -m 'rw commit'",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("commit under readwrite failed: %v\nOutput: %s", err, out)
	}

	// (a) Main branch HEAD unchanged.
	mainAfter := headCommit(t, repoDir)
	if mainAfter != mainBefore {
		t.Errorf("main HEAD changed: before=%s after=%s", mainBefore, mainAfter)
	}

	// (b) Worktree branch has a new commit.
	wtHead := branchHead(t, repoDir, "rw-branch")
	if wtHead == "" {
		t.Fatal("worktree branch 'rw-branch' not found after commit")
	}
	if wtHead == mainBefore {
		t.Error("worktree branch HEAD did not advance — commit did not land")
	}

	// Verify new_file.txt exists in the worktree branch.
	showOut, err := exec.Command("git", "-C", repoDir, "show", "rw-branch:new_file.txt").Output()
	if err != nil || strings.TrimSpace(string(showOut)) != "added" {
		t.Errorf("new_file.txt not found in rw-branch: err=%v out=%q", err, showOut)
	}

	// (c) Main checkout working tree has no new_file.txt.
	if _, err := os.Stat(filepath.Join(repoDir, "new_file.txt")); !os.IsNotExist(err) {
		t.Error("new_file.txt unexpectedly present in main checkout")
	}
}

// TestWorktree_RmRemovesWorktree verifies that --rm tears down the git worktree
// on exit so it no longer appears in `git worktree list --porcelain`.
func TestWorktree_RmRemovesWorktree(t *testing.T) {
	if insideSandbox() {
		t.Skip("cannot launch nested devsandbox from inside a sandbox session")
	}
	if !bwrapAvailable() {
		t.Skip("bwrap not available or user namespaces disabled")
	}
	if !gitIsAvailable() {
		t.Skip("git not available")
	}

	repoDir, run := setupWorktreeTest(t)

	cmd := run("--worktree=eph", "--rm", "--", "true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--rm run failed: %v\nOutput: %s", err, out)
	}

	listOut, err := exec.Command("git", "-C", repoDir, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	if strings.Contains(string(listOut), "eph") {
		t.Errorf("worktree 'eph' still present after --rm:\n%s", listOut)
	}
}

// TestWorktree_ReuseExistingBranch verifies that two back-to-back invocations
// with the same --worktree=<branch> both succeed (second one reuses the
// existing worktree).
func TestWorktree_ReuseExistingBranch(t *testing.T) {
	if insideSandbox() {
		t.Skip("cannot launch nested devsandbox from inside a sandbox session")
	}
	if !bwrapAvailable() {
		t.Skip("bwrap not available or user namespaces disabled")
	}
	if !gitIsAvailable() {
		t.Skip("git not available")
	}

	_, run := setupWorktreeTest(t)

	for i := 1; i <= 2; i++ {
		cmd := run("--worktree=shared", "--", "true")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("invocation %d with --worktree=shared failed: %v\nOutput: %s", i, err, out)
		}
	}
}

// TestWorktree_RejectsStaleDir verifies that when the canonical worktree path
// already exists on disk but is unknown to git, devsandbox refuses to proceed
// and does NOT clobber the directory.
func TestWorktree_RejectsStaleDir(t *testing.T) {
	if insideSandbox() {
		t.Skip("cannot launch nested devsandbox from inside a sandbox session")
	}
	if !gitIsAvailable() {
		t.Skip("git not available")
	}
	// This test does not need bwrap — validation happens before the sandbox
	// is entered. We still run the binary, which will fail at worktree setup.

	repoDir := t.TempDir()

	// Minimal repo.
	for _, argv := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@test.local"},
		{"config", "user.name", "Test"},
	} {
		c := exec.Command("git", append([]string{"-C", repoDir}, argv...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", argv, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repoDir, "README"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"add", "README"},
		{"commit", "-m", "init"},
	} {
		c := exec.Command("git", append([]string{"-C", repoDir}, argv...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", argv, err, out)
		}
	}

	// Isolated sandbox base.
	configDir := t.TempDir()
	sandboxBase := t.TempDir()

	configPath := filepath.Join(configDir, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	configContent := fmt.Sprintf("[sandbox]\nbase_path = %q\n", sandboxBase)
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pre-create the canonical worktree path with a ghost file so git doesn't
	// know about it.
	sandboxName := sandbox.GenerateSandboxName(repoDir)
	sandboxRoot := filepath.Join(sandboxBase, sandboxName)
	wtPath := worktree.WorktreePath(sandboxRoot, "ghost-branch")
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatal(err)
	}
	ghostFile := filepath.Join(wtPath, "ghost")
	if err := os.WriteFile(ghostFile, []byte("do not clobber"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binaryPath, "--worktree=ghost-branch", "--", "true")
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+configDir)
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("expected failure with stale directory, but command succeeded\nOutput: %s", out)
	}
	if !strings.Contains(strings.ToLower(string(out)), "stale") {
		t.Errorf("expected 'stale' in error output, got: %s", out)
	}

	// Ghost file must still be present (no clobber).
	if _, err := os.Stat(ghostFile); os.IsNotExist(err) {
		t.Error("ghost file was removed — stale directory was clobbered")
	}
}

// TestWorktree_RejectedWithGitModeDisabled verifies that combining
// --worktree with --git-mode=disabled is rejected before any sandbox is
// entered.
func TestWorktree_RejectedWithGitModeDisabled(t *testing.T) {
	if insideSandbox() {
		t.Skip("cannot launch nested devsandbox from inside a sandbox session")
	}
	if !gitIsAvailable() {
		t.Skip("git not available")
	}
	// No bwrap check needed — validation is pre-sandbox.

	repoDir := t.TempDir()
	for _, argv := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@test.local"},
		{"config", "user.name", "Test"},
	} {
		c := exec.Command("git", append([]string{"-C", repoDir}, argv...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", argv, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repoDir, "README"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"add", "README"},
		{"commit", "-m", "init"},
	} {
		c := exec.Command("git", append([]string{"-C", repoDir}, argv...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", argv, err, out)
		}
	}

	configDir := t.TempDir()
	sandboxBase := t.TempDir()
	configPath := filepath.Join(configDir, "devsandbox", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath,
		[]byte(fmt.Sprintf("[sandbox]\nbase_path = %q\n", sandboxBase)), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binaryPath, "--worktree=x", "--git-mode=disabled", "--", "true")
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+configDir)
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("expected failure with --git-mode=disabled, but command succeeded\nOutput: %s", out)
	}
	if !strings.Contains(string(out), "--git-mode=disabled") {
		t.Errorf("expected '--git-mode=disabled' in error output, got: %s", out)
	}
}
