package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A HomeRelativeDest binding tells the Docker/krun backend to rewrite the host
// home prefix of its Dest. A Dest that does not carry that prefix would be left
// alone, so the flag would read as applied while changing nothing - the silent
// no-op the flag exists to remove. Asked of every registered tool rather than
// of a list, so a tool that grows such a binding cannot drift out of it.
func TestBindings_HomeRelativeDestIsUnderHomeDir(t *testing.T) {
	home, sandboxHome := t.TempDir(), t.TempDir()

	for _, tool := range All() {
		for _, b := range tool.Bindings(home, sandboxHome) {
			if !b.HomeRelativeDest {
				continue
			}
			if b.Dest == "" {
				t.Errorf("%s: HomeRelativeDest with an empty Dest, which is remapped from Source instead", tool.Name())
				continue
			}
			if !strings.HasPrefix(b.Dest, home+"/") {
				t.Errorf("%s: HomeRelativeDest binding Dest = %q, want a path under %q", tool.Name(), b.Dest, home)
			}
		}
	}
}

// The safe gitconfig and the ignore/attributes copies all live in the sandbox
// home and are named with the host home prefix, which only bwrap binds there.
func TestGit_Bindings_SandboxHomeDestsAreHomeRelative(t *testing.T) {
	const homeDir, sandboxHome = "/home/u", "/sandbox/home"

	g := &Git{mode: GitModeReadOnly}
	got := make(map[string]bool)
	for _, b := range g.Bindings(homeDir, sandboxHome) {
		if b.HomeRelativeDest {
			got[b.Dest] = true
		}
	}

	for _, name := range []string{".gitconfig", ".gitignore.safe", ".gitattributes.safe"} {
		if !got[filepath.Join(homeDir, name)] {
			t.Errorf("%s binding is not marked HomeRelativeDest, so Docker and krun mount it where nothing reads it", name)
		}
	}
}

// The read-only .git and the sanitized .git/config are pinned to their host
// paths on purpose: the worktree's .git file carries an absolute gitdir:
// pointer that has to resolve inside the sandbox.
func TestGit_Bindings_RepoDestsAreNotHomeRelative(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte("[core]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &Git{mode: GitModeReadOnly, projectDir: repo, gitRepoRoot: repo}
	for _, b := range g.Bindings("/home/u", "/sandbox/home") {
		if strings.HasPrefix(b.Dest, repo) && b.HomeRelativeDest {
			t.Errorf("binding for %q is marked HomeRelativeDest; a repository path must be mounted verbatim", b.Dest)
		}
	}
}
