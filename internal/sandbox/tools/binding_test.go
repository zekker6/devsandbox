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

// TestGit_Bindings_ReadWriteRefsAreHomeRelativeUnderHome asks the same question
// TestBindings_HomeRelativeDestIsUnderHomeDir does, of the bindings that test
// cannot reach: it iterates All() and calls Bindings without calling Setup, so
// git's refBindings are nil there and only the four static readwrite entries
// are seen. Everything this change resolves is invisible to it.
//
// The flag is asserted as a struct field rather than through a rendered path
// because bwrap binds the sandbox home at the host home path, so the
// home-relative and verbatim spellings of a Dest are the same string there. A
// wrong flag is a silent no-op on bwrap and mounts the file where nothing looks
// for it on Docker and krun.
func TestGit_Bindings_ReadWriteRefsAreHomeRelativeUnderHome(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	sandboxHome := filepath.Join(tmpDir, "sandbox")
	projectDir := filepath.Join(tmpDir, "project")
	shared := filepath.Join(tmpDir, "shared", "team.gitconfig")
	for _, d := range []string{sandboxHome, projectDir, filepath.Join(homeDir, ".config", "git"), filepath.Dir(shared)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	isolateGitEnv(t, homeDir)
	captureNotices(t)

	// One config exercising every spelling the resolver can report: a
	// ~/-spelled key, an unset key falling back to the XDG default, a
	// ~/-spelled include, a relative include nested inside it, and an
	// absolutely-spelled include that has to stay verbatim.
	writeFile(t, filepath.Join(homeDir, "my-ignore"), "build/\n")
	writeFile(t, filepath.Join(homeDir, ".config", "git", "attributes"), "*.bin binary\n")
	writeFile(t, filepath.Join(homeDir, "inc.gitconfig"), "[include]\n\tpath = nested.gitconfig\n")
	writeFile(t, filepath.Join(homeDir, "nested.gitconfig"), "[user]\n\temail = ada@corp\n")
	writeFile(t, shared, "[user]\n\tname = Ada\n")
	writeFile(t, filepath.Join(homeDir, ".gitconfig"),
		"[core]\n\texcludesfile = ~/my-ignore\n[include]\n\tpath = ~/inc.gitconfig\n\tpath = "+shared+"\n")

	g := &Git{}
	g.Configure(GlobalConfig{ProjectDir: projectDir}, map[string]any{"mode": "readwrite"})
	if err := g.Setup(homeDir, sandboxHome); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if len(g.refBindings) == 0 {
		t.Fatal("Setup resolved no references, so the invariant below asserts nothing")
	}

	for _, b := range g.Bindings(homeDir, sandboxHome) {
		if !b.HomeRelativeDest {
			continue
		}
		if b.Dest == "" {
			t.Errorf("%s: HomeRelativeDest with an empty Dest, which is remapped from Source instead", b.Source)
			continue
		}
		if !strings.HasPrefix(b.Dest, homeDir+"/") {
			t.Errorf("HomeRelativeDest binding for %s: Dest = %q, want a path under %q", b.Source, b.Dest, homeDir)
		}
	}

	// And the converse, which the loop above cannot see: an absolutely-spelled
	// include names the same path on every backend, so flagging it would move
	// it under the container home where git never looks.
	var found bool
	for _, b := range g.refBindings {
		if b.Source != shared {
			continue
		}
		found = true
		if b.Dest != shared || b.HomeRelativeDest {
			t.Errorf("absolute include: Dest = %q HomeRelativeDest = %v, want %q and false",
				b.Dest, b.HomeRelativeDest, shared)
		}
	}
	if !found {
		t.Errorf("the absolutely-spelled include %s was not carried", shared)
	}
}
