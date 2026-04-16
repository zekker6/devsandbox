package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// makeRepo creates a fresh git repo with an initial commit and returns its path.
func makeRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, argv := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "a@b"},
		{"config", "user.name", "a"},
	} {
		run(t, dir, "git", argv...)
	}
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "git", "add", "README")
	run(t, dir, "git", "commit", "-q", "-m", "init")
	return dir
}

func TestManagerEnsureCreatesNewBranch(t *testing.T) {
	requireGit(t)
	repo := makeRepo(t)
	sbox := t.TempDir()

	m := NewManager()
	h, err := m.Ensure(context.Background(), EnsureRequest{
		RepoRoot:    repo,
		SandboxRoot: sbox,
		Branch:      "feat/foo",
		Base:        "HEAD",
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if h.Path == "" || h.Branch != "feat/foo" {
		t.Fatalf("bad handle: %+v", h)
	}
	// Resolve symlinks on sbox so the expected path matches what Ensure
	// returns (on macOS /var → /private/var).
	resolvedSbox, err := filepath.EvalSymlinks(sbox)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", sbox, err)
	}
	wantPath := WorktreePath(resolvedSbox, "feat/foo")
	if h.Path != wantPath {
		t.Errorf("h.Path = %q, want %q", h.Path, wantPath)
	}
	if h.Created != true {
		t.Error("expected Created=true on fresh branch")
	}

	data, err := os.ReadFile(filepath.Join(h.Path, ".git"))
	if err != nil {
		t.Fatalf("read .git: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(data)), "gitdir:") {
		t.Errorf(".git file does not start with 'gitdir:': %q", data)
	}
}

func TestManagerEnsureReusesExistingWorktree(t *testing.T) {
	requireGit(t)
	repo := makeRepo(t)
	sbox := t.TempDir()
	m := NewManager()

	req := EnsureRequest{RepoRoot: repo, SandboxRoot: sbox, Branch: "x", Base: "HEAD"}
	first, err := m.Ensure(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Ensure(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if second.Path != first.Path {
		t.Errorf("reuse path mismatch: %q vs %q", first.Path, second.Path)
	}
	if second.Created {
		t.Error("expected Created=false on reuse")
	}
}

func TestManagerEnsureRejectsStaleDir(t *testing.T) {
	requireGit(t)
	repo := makeRepo(t)
	sbox := t.TempDir()

	// Pre-create the canonical path that Ensure would use for branch "stale",
	// including the hash suffix from SanitizeLeaf.
	stale := WorktreePath(sbox, "stale")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "ghost"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewManager()
	_, err := m.Ensure(context.Background(), EnsureRequest{
		RepoRoot: repo, SandboxRoot: sbox, Branch: "stale", Base: "HEAD",
	})
	if err == nil {
		t.Fatalf("expected stale-dir error, got nil")
	}
	if !strings.Contains(err.Error(), "stale") {
		t.Errorf("err = %v, want contains 'stale'", err)
	}
	if _, statErr := os.Stat(filepath.Join(stale, "ghost")); statErr != nil {
		t.Errorf("manager clobbered stale dir: %v", statErr)
	}
}

func TestManagerRemove(t *testing.T) {
	requireGit(t)
	repo := makeRepo(t)
	sbox := t.TempDir()
	m := NewManager()
	h, err := m.Ensure(context.Background(), EnsureRequest{
		RepoRoot: repo, SandboxRoot: sbox, Branch: "rm-me", Base: "HEAD",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(context.Background(), repo, h.Path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, statErr := os.Stat(h.Path); !os.IsNotExist(statErr) {
		t.Errorf("worktree dir still exists after Remove: %v", statErr)
	}
}
