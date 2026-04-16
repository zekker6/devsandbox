//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupWorktreeDockerTest is like setupWorktreeTest but configures Docker
// isolation. The caller must check dockerAvailable(), dockerImageAvailable(),
// and gitIsAvailable() before calling.
func setupWorktreeDockerTest(t *testing.T) (repoDir string, run func(args ...string) *exec.Cmd) {
	t.Helper()

	repoDir = t.TempDir()
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
	configContent := fmt.Sprintf("[sandbox]\nisolation = \"docker\"\nbase_path = %q\n\n[sandbox.docker]\n", sandboxBase)
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

// TestWorktree_DockerCreatesAndEnters verifies that --worktree works with the
// Docker backend: the CWD inside the container is the worktree path, and the
// gitdir: pointer resolves correctly (git status succeeds).
func TestWorktree_DockerCreatesAndEnters(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping e2e test in short mode")
	}
	if insideSandbox() {
		t.Skip("cannot launch nested devsandbox from inside a sandbox session")
	}
	if !dockerAvailable() {
		t.Skip("Docker not available")
	}
	if !dockerImageAvailable() {
		t.Skip("Docker image 'devsandbox:local' not available - run 'task docker:build' first")
	}
	if !gitIsAvailable() {
		t.Skip("git not available")
	}

	_, run := setupWorktreeDockerTest(t)

	cmd := run("--worktree=docker-feat", "--", "sh", "-c", "pwd && git status")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("devsandbox --worktree=docker-feat failed: %v\nOutput: %s", err, out)
	}
	outStr := string(out)
	if !strings.Contains(outStr, "/worktrees/") {
		t.Errorf("expected pwd to contain /worktrees/, got: %s", outStr)
	}
	// git status must succeed (not error about missing gitdir).
	if strings.Contains(outStr, "fatal:") {
		t.Errorf("git status failed inside Docker worktree:\n%s", outStr)
	}
}

// TestWorktree_DockerReadwriteCommit verifies that with Docker backend +
// --git-mode=readwrite, commits land on the worktree branch.
func TestWorktree_DockerReadwriteCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping e2e test in short mode")
	}
	if insideSandbox() {
		t.Skip("cannot launch nested devsandbox from inside a sandbox session")
	}
	if !dockerAvailable() {
		t.Skip("Docker not available")
	}
	if !dockerImageAvailable() {
		t.Skip("Docker image 'devsandbox:local' not available - run 'task docker:build' first")
	}
	if !gitIsAvailable() {
		t.Skip("git not available")
	}

	repoDir, run := setupWorktreeDockerTest(t)
	mainBefore := headCommit(t, repoDir)

	cmd := run(
		"--worktree=docker-rw",
		"--git-mode=readwrite",
		"--",
		"sh", "-c",
		"echo docker > docker_file.txt && git add docker_file.txt && git -c user.email=t@t -c user.name=t commit -m 'docker commit'",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Docker readwrite commit failed: %v\nOutput: %s", err, out)
	}

	// Main branch HEAD unchanged.
	mainAfter := headCommit(t, repoDir)
	if mainAfter != mainBefore {
		t.Errorf("main HEAD changed: before=%s after=%s", mainBefore, mainAfter)
	}

	// Worktree branch has the commit.
	wtHead := branchHead(t, repoDir, "docker-rw")
	if wtHead == "" {
		t.Fatal("worktree branch 'docker-rw' not found after commit")
	}
	if wtHead == mainBefore {
		t.Error("worktree branch HEAD did not advance")
	}
}
