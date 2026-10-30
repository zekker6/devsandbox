package isolator

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"devsandbox/internal/sandbox"
)

func nativeGitHost(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(nativeGitTestEnv(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func nativeGitTestEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") && !strings.HasPrefix(e, "DEVSANDBOX_GIT_") {
			env = append(env, e)
		}
	}
	return env
}

func nativeGitFixture(t *testing.T, dir, branch string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	nativeGitHost(t, dir, "init", "-b", branch)
	nativeGitHost(t, dir, "config", "user.name", "Fixture")
	nativeGitHost(t, dir, "config", "user.email", "fixture@example.test")
	nativeGitHost(t, dir, "config", "fixture.repo", branch)
	nativeWrite(t, filepath.Join(dir, "tracked"), branch+"\n")
	nativeGitHost(t, dir, "add", "tracked")
	nativeGitHost(t, dir, "commit", "-m", "fixture")
}

func nativeGitPlan(t *testing.T, cfg *sandbox.Config) *nativePlan {
	t.Helper()
	// macOS temp paths can include /var, which is a symlink to /private/var.
	for _, path := range []*string{&cfg.ProjectDir, &cfg.SandboxRoot, &cfg.SandboxHome} {
		canonical, err := filepath.EvalSymlinks(*path)
		if err != nil {
			t.Fatal(err)
		}
		*path = canonical
	}
	p := &nativePlan{setup: filepath.Join(cfg.SandboxRoot, "git-setup"), env: nativeBaseEnv(cfg)}
	if err := os.Mkdir(p.setup, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := p.prepareGit(cfg); err != nil {
		t.Fatal(err)
	}
	return p
}

func nativeGitCommand(p *nativePlan, dir string, args ...string) *exec.Cmd {
	cmd := exec.Command(filepath.Join(p.setup, "bin/git"), args...)
	cmd.Dir = dir
	cmd.Env = []string{"GIT_CONFIG_GLOBAL=/dev/null"}
	for name, value := range p.env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	return cmd
}

func nativeGitRun(t *testing.T, p *nativePlan, dir string, args ...string) string {
	t.Helper()
	out, err := nativeGitCommand(p, dir, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("dispatcher git %v in %s: %v: %s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestNativeGitNestedRouting(t *testing.T) {
	cfg := nativeTestConfig(t)
	nativeGitFixture(t, cfg.ProjectDir, "root")
	for _, name := range []string{"first", "second"} {
		nativeGitFixture(t, filepath.Join(cfg.ProjectDir, name), name)
	}
	nativeGitHost(t, cfg.ProjectDir, "config", "alias.child", "!cd first && git branch --show-current")
	nativeGitHost(t, filepath.Join(cfg.ProjectDir, "first"), "config", "alias.sibling", "!cd ../second && git config fixture.repo")
	p := nativeGitPlan(t, cfg)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"root branch", []string{"branch", "--show-current"}, "root"},
		{"root config", []string{"config", "fixture.repo"}, "root"},
		{"first branch", []string{"-C", "first", "branch", "--show-current"}, "first"},
		{"second config", []string{"-C", "second", "config", "fixture.repo"}, "second"},
		{"repeated C", []string{"-C", "first", "-C", "../second", "branch", "--show-current"}, "second"},
		{"empty C", []string{"-C", "first", "-C", "", "config", "fixture.repo"}, "first"},
		{"option operand", []string{"-c", "fixture.repo=argument", "-C", "first", "config", "fixture.repo"}, "argument"},
		{"option repeated", []string{"-c", "fixture.repo=first", "-c", "fixture.repo=argument", "-C", "second", "config", "fixture.repo"}, "argument"},
		{"shell alias", []string{"child"}, "first"},
		{"sibling shell alias", []string{"-C", "first", "sibling"}, "second"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nativeGitRun(t, p, cfg.ProjectDir, tc.args...); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	for _, name := range []string{"first", "second"} {
		if got := nativeGitRun(t, p, filepath.Join(cfg.ProjectDir, name), "config", "fixture.repo"); got != name {
			t.Fatalf("cwd routing = %q", got)
		}
	}
}

func TestNativeGitFreshAndExplicitRouting(t *testing.T) {
	cfg := nativeTestConfig(t)
	nativeGitFixture(t, cfg.ProjectDir, "parent")
	p := nativeGitPlan(t, cfg)
	snapshot := nativeGitRun(t, p, cfg.ProjectDir, "rev-parse", "--absolute-git-dir")
	before := nativeRead(t, filepath.Join(snapshot, "HEAD"))
	nativeGitRun(t, p, cfg.ProjectDir, "init", "-b", "fresh", "new")
	newDir := filepath.Join(cfg.ProjectDir, "new")
	if got := nativeGitRun(t, p, newDir, "branch", "--show-current"); got != "fresh" {
		t.Fatalf("new repo used parent: %q", got)
	}
	if err := os.Mkdir(filepath.Join(newDir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := nativeGitRun(t, p, filepath.Join(newDir, "sub"), "rev-parse", "--absolute-git-dir"); got != filepath.Join(newDir, ".git") {
		t.Fatalf("new nested ancestor routing: %q", got)
	}
	if got := nativeRead(t, filepath.Join(snapshot, "HEAD")); got != before {
		t.Fatal("init changed protected snapshot")
	}
	// Explicit selectors bypass convenience routing, never policy enforcement.
	for _, args := range [][]string{
		{"--git-dir", filepath.Join(newDir, ".git"), "branch", "--show-current"},
		{"--git-dir=" + filepath.Join(newDir, ".git"), "branch", "--show-current"},
		{"-C", newDir, "--work-tree", newDir, "branch", "--show-current"},
	} {
		if got := nativeGitRun(t, p, cfg.ProjectDir, args...); got != "fresh" {
			t.Fatalf("explicit routing: %q", got)
		}
	}
	cmd := nativeGitCommand(p, cfg.ProjectDir, "branch", "--show-current")
	cmd.Env = append(cmd.Env, "GIT_DIR="+filepath.Join(newDir, ".git"))
	if out, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "fresh" {
		t.Fatalf("explicit GIT_DIR: %s: %v", out, err)
	}
	// clone must operate on its destination, not reuse the selected snapshot.
	nativeGitRun(t, p, cfg.ProjectDir, "clone", "--no-hardlinks", cfg.ProjectDir, "cloned")
	if got := nativeGitRun(t, p, filepath.Join(cfg.ProjectDir, "cloned"), "rev-parse", "--absolute-git-dir"); got != filepath.Join(cfg.ProjectDir, "cloned/.git") {
		t.Fatalf("clone routing: %q", got)
	}
}

func nativeGitLinkedFixture(t *testing.T) (*sandbox.Config, string, string) {
	t.Helper()
	cfg := nativeTestConfig(t)
	main := filepath.Join(filepath.Dir(cfg.ProjectDir), "main")
	nativeGitFixture(t, main, "main")
	nativeGitHost(t, main, "worktree", "add", "-b", "linked", cfg.ProjectDir)
	cfg.GitRepoRoot = main
	private := nativeGitHost(t, cfg.ProjectDir, "rev-parse", "--absolute-git-dir")
	return cfg, main, private
}

func TestNativeGitLinkedSnapshot(t *testing.T) {
	cfg, main, private := nativeGitLinkedFixture(t)
	nativeGitHost(t, main, "worktree", "add", "-b", "sibling", filepath.Join(filepath.Dir(main), "sibling"))
	nativeWrite(t, filepath.Join(main, "main-only"), "main staged")
	nativeGitHost(t, main, "add", "main-only")
	nativeWrite(t, filepath.Join(cfg.ProjectDir, "linked-only"), "linked staged")
	nativeGitHost(t, cfg.ProjectDir, "add", "linked-only")
	nativeGitHost(t, main, "config", "extensions.worktreeConfig", "true")
	nativeGitHost(t, main, "config", "--worktree", "fixture.checkout", "main")
	nativeGitHost(t, cfg.ProjectDir, "config", "--worktree", "fixture.checkout", "linked")
	nativeGitHost(t, cfg.ProjectDir, "config", "--worktree", "remote.private.url", "https://worktree-secret@example.test/private")
	nativeGitHost(t, main, "config", "remote.origin.url", "https://shared-secret@example.test/repo")
	included := filepath.Join(cfg.ProjectDir, "included-config")
	nativeWrite(t, included, "[credential]\n helper = secret-helper\n[remote \"origin\"]\n url = https://included-secret@example.test/repo\n")
	nativeGitHost(t, main, "config", "include.path", included)
	nativeGitHost(t, cfg.ProjectDir, "config", "--worktree", "includeIf.gitdir:*.path", included)
	p := nativeGitPlan(t, cfg)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"branch", "--show-current"}, "linked"},
		{[]string{"config", "fixture.checkout"}, "linked"},
		{[]string{"config", "remote.origin.url"}, "https://example.test/repo"},
		{[]string{"config", "remote.private.url"}, "https://example.test/private"},
		{[]string{"diff", "--cached", "--name-only"}, "linked-only"},
	} {
		if got := nativeGitRun(t, p, cfg.ProjectDir, tc.args...); got != tc.want {
			t.Fatalf("git %v = %q, want %q", tc.args, got, tc.want)
		}
	}
	snapshot := nativeGitRun(t, p, cfg.ProjectDir, "rev-parse", "--absolute-git-dir")
	for _, name := range []string{"worktrees", "commondir", "gitdir"} {
		if _, err := os.Lstat(filepath.Join(snapshot, name)); !os.IsNotExist(err) {
			t.Fatalf("snapshot contains %s: %v", name, err)
		}
	}
	if got := nativeRead(t, filepath.Join(snapshot, "logs/HEAD")); got != nativeRead(t, filepath.Join(private, "logs/HEAD")) {
		t.Fatal("snapshot copied wrong HEAD log")
	}
	for _, name := range []string{"config", "config.worktree"} {
		data := nativeRead(t, filepath.Join(snapshot, name))
		if strings.Contains(data, "secret") || strings.Contains(strings.ToLower(data), "include") {
			t.Fatalf("unsafe %s: %s", name, data)
		}
	}
	for _, path := range []string{filepath.Join(cfg.ProjectDir, ".git"), filepath.Join(main, ".git"), private} {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || !slices.Contains(p.policy.DenyRead, resolved) {
			t.Fatalf("unprotected raw metadata %s: %v", path, err)
		}
	}
	// A selected tool may grant the main checkout or even .git read access.
	// The policy must retain an explicit denial which overrides that grant.
	p.policy.ReadOnly = append(p.policy.ReadOnly, main, filepath.Join(main, ".git"))
	profile, err := p.policy.Profile()
	if err != nil {
		t.Fatal(err)
	}
	common, err := filepath.EvalSymlinks(filepath.Join(main, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(profile, `(deny file-read* file-write* (subpath "`+common+`"))`) {
		t.Fatal("raw common metadata lacks explicit policy denial")
	}
}

func TestNativeGitRejectsInvalidMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*testing.T, *sandbox.Config, string, string)
	}{
		{"bad marker", func(t *testing.T, c *sandbox.Config, _, _ string) {
			nativeWrite(t, filepath.Join(c.ProjectDir, ".git"), "gitdir: \n")
		}},
		{"missing target", func(t *testing.T, c *sandbox.Config, _, _ string) {
			nativeWrite(t, filepath.Join(c.ProjectDir, ".git"), "gitdir: /nonexistent/metadata\n")
		}},
		{"arbitrary indirection", func(t *testing.T, c *sandbox.Config, main, _ string) {
			nativeWrite(t, filepath.Join(c.ProjectDir, ".git"), "gitdir: "+filepath.Join(main, ".git")+"\n")
		}},
		{"wrong backlink", func(t *testing.T, _ *sandbox.Config, main, private string) {
			nativeWrite(t, filepath.Join(private, "gitdir"), filepath.Join(main, ".git")+"\n")
		}},
		{"wrong host anchor", func(_ *testing.T, c *sandbox.Config, _, _ string) { c.GitRepoRoot = c.HomeDir }},
		{"disabled linked", func(_ *testing.T, c *sandbox.Config, _, _ string) {
			c.ToolsConfig = map[string]any{"git": map[string]any{"mode": "disabled"}}
		}},
		{"special config", func(t *testing.T, _ *sandbox.Config, main, _ string) {
			path := filepath.Join(main, ".git/config")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"directory config", func(t *testing.T, _ *sandbox.Config, main, _ string) {
			path := filepath.Join(main, ".git/config")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink config", func(t *testing.T, _ *sandbox.Config, main, _ string) {
			nativeGitReplaceLink(t, filepath.Join(main, ".git/config"))
		}},
		{"symlink worktree config", func(t *testing.T, _ *sandbox.Config, _, private string) {
			nativeWrite(t, filepath.Join(private, "config.worktree"), "[user]\nname=private\n")
			nativeGitReplaceLink(t, filepath.Join(private, "config.worktree"))
		}},
		{"symlink marker", func(t *testing.T, c *sandbox.Config, _, _ string) {
			nativeGitReplaceLink(t, filepath.Join(c.ProjectDir, ".git"))
		}},
		{"symlink private", func(t *testing.T, _ *sandbox.Config, _, private string) { nativeGitReplaceLink(t, private) }},
		{"symlink backlink", func(t *testing.T, _ *sandbox.Config, _, private string) {
			nativeGitReplaceLink(t, filepath.Join(private, "gitdir"))
		}},
		{"symlink commondir", func(t *testing.T, _ *sandbox.Config, _, private string) {
			nativeGitReplaceLink(t, filepath.Join(private, "commondir"))
		}},
		{"symlink common", func(t *testing.T, _ *sandbox.Config, main, _ string) {
			nativeGitReplaceLink(t, filepath.Join(main, ".git"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, main, private := nativeGitLinkedFixture(t)
			tc.edit(t, cfg, main, private)
			setup := filepath.Join(cfg.SandboxRoot, "setup")
			if err := os.Mkdir(setup, 0o700); err != nil {
				t.Fatal(err)
			}
			p := &nativePlan{setup: setup, env: nativeBaseEnv(cfg)}
			if err := p.prepareGit(cfg); err == nil {
				t.Fatal("accepted invalid Git metadata")
			}
		})
	}
}

func nativeGitReplaceLink(t *testing.T, path string) {
	t.Helper()
	if err := os.Rename(path, path+"-target"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+"-target", path); err != nil {
		t.Fatal(err)
	}
}

func TestNativeGitRejectsChangedSnapshotSource(t *testing.T) {
	for _, kind := range []string{"regular", "common", "private"} {
		for _, mutation := range []string{"replacement", "root symlink", "ancestor symlink"} {
			t.Run(kind+"/"+mutation, func(t *testing.T) {
				var cfg *sandbox.Config
				if kind == "regular" {
					cfg = nativeTestConfig(t)
					nativeGitFixture(t, cfg.ProjectDir, "main")
				} else {
					cfg, _, _ = nativeGitLinkedFixture(t)
				}
				canonical, err := filepath.EvalSymlinks(cfg.ProjectDir)
				if err != nil {
					t.Fatal(err)
				}
				cfg.ProjectDir = canonical
				repos, err := discoverNativeGit(cfg)
				if err != nil || len(repos) != 1 {
					t.Fatalf("discover: %v, %v", repos, err)
				}
				source := repos[0].common
				if kind == "private" {
					source = repos[0].private
				}
				if mutation == "ancestor symlink" {
					source = filepath.Dir(source)
				}
				if err := os.Rename(source, source+"-original"); err != nil {
					t.Fatal(err)
				}
				if mutation == "replacement" {
					err = os.Mkdir(source, 0o700)
				} else {
					// Even a symlink to the same inode must not silently change
					// the path on which the policy's metadata denial depends.
					err = os.Symlink(source+"-original", source)
				}
				if err != nil {
					t.Fatal(err)
				}
				stage, err := os.OpenRoot(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := stage.Close(); err != nil {
						t.Error(err)
					}
				}()
				if err := snapshotNativeGit(stage, repos[0], "snapshot"); err == nil {
					t.Fatal("copied metadata from a changed source")
				}
			})
		}
	}
}

func TestNativeGitMetadataReadLimit(t *testing.T) {
	for _, size := range []int{nativeGitMaxTextSize, nativeGitMaxTextSize + 1} {
		path := filepath.Join(t.TempDir(), "config")
		nativeWrite(t, path, strings.Repeat("x", size))
		data, err := nativeGitRead(path)
		if size == nativeGitMaxTextSize {
			if err != nil || len(data) != size {
				t.Fatalf("bounded read: %d bytes, %v", len(data), err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "exceeds") || data != nil {
			t.Fatalf("oversized read: %d bytes, %v", len(data), err)
		}
	}
}

func TestNativeGitQuotedPaths(t *testing.T) {
	cfg := nativeTestConfig(t)
	cfg.ProjectDir = filepath.Join(cfg.ProjectDir, "repo' with spaces")
	nativeGitFixture(t, cfg.ProjectDir, "quoted")
	p := nativeGitPlan(t, cfg)
	if got := nativeGitRun(t, p, cfg.ProjectDir, "branch", "--show-current"); got != "quoted" {
		t.Fatalf("quoted checkout route: %q", got)
	}
}

func TestNativeGitDiscoverySkipsDirectoryLinks(t *testing.T) {
	cfg := nativeTestConfig(t)
	nativeGitFixture(t, cfg.ProjectDir, "root")
	outside := filepath.Join(t.TempDir(), "outside")
	nativeGitFixture(t, outside, "outside")
	if err := os.Symlink(outside, filepath.Join(cfg.ProjectDir, "linked-dir")); err != nil {
		t.Fatal(err)
	}
	repos, err := discoverNativeGit(cfg)
	if err != nil || len(repos) != 1 {
		t.Fatalf("discovery followed directory link: %+v, %v", repos, err)
	}
}
