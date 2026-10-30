package isolator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"devsandbox/internal/cgroups"
	"devsandbox/internal/config"
	"devsandbox/internal/sandbox"
	"devsandbox/internal/sandbox/mounts"
	"devsandbox/internal/sandbox/tools"
	"devsandbox/internal/seatbelt"
)

func nativeTestConfig(t *testing.T) *sandbox.Config {
	t.Helper()
	base := t.TempDir()
	cfg := &sandbox.Config{
		HomeDir: filepath.Join(base, "host"), ProjectDir: filepath.Join(base, "project"),
		SandboxRoot: filepath.Join(base, "state"), SandboxHome: filepath.Join(base, "state/home"),
		Shell: sandbox.ShellBash, ShellPath: "/bin/bash", DefaultMountMode: "split",
		HideEnvFiles: true, ConfigVisibility: "hidden", Isolation: sandbox.IsolationSeatbelt,
	}
	for _, dir := range []string{cfg.HomeDir, cfg.ProjectDir, cfg.SandboxHome} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return cfg
}

func TestSeatbeltValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*sandbox.Config, *config.Config, *SeatbeltIsolator)
		want string
	}{
		{"defaults", func(*sandbox.Config, *config.Config, *SeatbeltIsolator) {}, ""},
		{"limits", func(_ *sandbox.Config, _ *config.Config, s *SeatbeltIsolator) {
			s.config.Limits = cgroups.Limits{Memory: "1g"}
		}, "resources"},
		{"worktree", func(c *sandbox.Config, _ *config.Config, _ *SeatbeltIsolator) { c.GitRepoRoot = c.HomeDir }, ""},
		{"custom mounts", func(c *sandbox.Config, _ *config.Config, _ *SeatbeltIsolator) {
			c.MountsConfig = mounts.NewEngine(config.MountsConfig{Rules: []config.MountRule{{Pattern: "secret", Mode: "hidden"}}}, c.HomeDir)
		}, "mounts"},
		{"forwarding", func(_ *sandbox.Config, a *config.Config, _ *SeatbeltIsolator) {
			enabled := true
			a.PortForwarding.Enabled = &enabled
		}, "port_forwarding"},
		{"auto detect", func(_ *sandbox.Config, a *config.Config, _ *SeatbeltIsolator) {
			enabled := true
			a.PortForwarding.AutoDetect = &enabled
		}, "auto_detect"},
		{"write through", func(c *sandbox.Config, _ *config.Config, _ *SeatbeltIsolator) { c.DefaultMountMode = "readwrite" }, "readwrite"},
		{"tool write through", func(c *sandbox.Config, _ *config.Config, _ *SeatbeltIsolator) {
			c.ToolsConfig = map[string]any{"go": map[string]any{"mount_mode": "readwrite"}}
		}, "readwrite"},
		{"explicit unavailable integration", func(c *sandbox.Config, _ *config.Config, _ *SeatbeltIsolator) {
			c.ToolsConfig = map[string]any{"docker": map[string]any{"enabled": true}}
		}, "host integration"},
		{"proxy environment override", func(c *sandbox.Config, _ *config.Config, _ *SeatbeltIsolator) {
			c.ProxyExtraEnv = []string{"PATH"}
		}, "reserves"},
		{"proxy certificate override", func(c *sandbox.Config, _ *config.Config, _ *SeatbeltIsolator) {
			c.ProxyExtraCAEnv = []string{"GIT_DIR"}
		}, "reserves"},
		{"git auth", func(c *sandbox.Config, _ *config.Config, _ *SeatbeltIsolator) {
			c.ToolsConfig = map[string]any{"git": map[string]any{"mode": "readwrite"}}
		}, "credential sharing"},
		{"host home project", func(c *sandbox.Config, _ *config.Config, _ *SeatbeltIsolator) { c.ProjectDir = c.HomeDir }, "host home"},
		{"state in project", func(c *sandbox.Config, _ *config.Config, _ *SeatbeltIsolator) {
			c.SandboxRoot = filepath.Join(c.ProjectDir, "state")
		}, "state directory"},
		{"home override", func(c *sandbox.Config, _ *config.Config, _ *SeatbeltIsolator) {
			c.EnvVars = map[string]string{"HOME": "/host"}
		}, "reserves"},
		{"loader passthrough", func(c *sandbox.Config, _ *config.Config, _ *SeatbeltIsolator) {
			c.EnvPassthrough = []string{"DYLD_INSERT_LIBRARIES"}
		}, "reserves"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, app, iso := nativeTestConfig(t), &config.Config{}, NewSeatbeltIsolator(SeatbeltConfig{})
			tc.edit(cfg, app, iso)
			err := iso.ValidateConfig(cfg, app)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("ValidateConfig = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSeatbeltValidationResolvedProject(t *testing.T) {
	cfg := nativeTestConfig(t)
	alias := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(filepath.Dir(cfg.SandboxRoot), alias); err != nil {
		t.Fatal(err)
	}
	cfg.ProjectDir = alias
	if err := NewSeatbeltIsolator(SeatbeltConfig{}).ValidateConfig(cfg, nil); err == nil {
		t.Fatal("allowed project alias containing sandbox state")
	}
}

func TestNativePinnedSymlink(t *testing.T) {
	cfg := nativeTestConfig(t)
	target := filepath.Join(t.TempDir(), "program")
	if err := os.WriteFile(target, []byte("program"), 0o700); err != nil {
		t.Fatal(err)
	}
	target, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(cfg.HomeDir, "program-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	home, err := os.OpenRoot(cfg.SandboxHome)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = home.Close() }()
	p := &nativePlan{}
	if err := p.applyBindings(home, cfg, []tools.Binding{{Source: link, Dest: link, Type: tools.MountBind, ReadOnly: true}}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(p.policy.ReadOnly, target) || !slices.Contains(p.policy.DenyWrite, target) {
		t.Fatalf("pinned target is not read-only: %+v", p.policy)
	}
}

func TestNativeProtectedProjectAliases(t *testing.T) {
	for _, tc := range []struct {
		name       string
		policy     seatbelt.Policy
		readDenied bool
	}{
		{".ENV.local", seatbelt.Policy{HideEnvFiles: true}, true},
		{".DEVSANDBOX.TOML", seatbelt.Policy{ProtectConfig: true}, false},
		{".devsandbox.toml", seatbelt.Policy{ConfigHidden: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := t.TempDir()
			target := filepath.Join(project, "target")
			if err := os.WriteFile(target, []byte("private"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("target", filepath.Join(project, tc.name)); err != nil {
				t.Fatal(err)
			}
			p := &nativePlan{policy: tc.policy}
			if err := p.protectProjectFiles(project); err != nil {
				t.Fatal(err)
			}
			resolved, err := filepath.EvalSymlinks(target)
			if err != nil {
				t.Fatal(err)
			}
			denied := p.policy.DenyWrite
			if tc.readDenied {
				denied = p.policy.DenyRead
			}
			if !slices.Contains(denied, resolved) {
				t.Fatalf("unprotected target: %+v", p.policy)
			}
		})
	}
	project := t.TempDir()
	if err := os.Symlink("missing", filepath.Join(project, ".env")); err != nil {
		t.Fatal(err)
	}
	p := &nativePlan{policy: seatbelt.Policy{HideEnvFiles: true}}
	if err := p.protectProjectFiles(project); err == nil {
		t.Fatal("accepted unresolved protected symlink")
	}
	p.policy.HideEnvFiles = false
	if err := p.protectProjectFiles(project); err != nil {
		t.Fatal(err)
	}
}

func TestSeatbeltCommandKeepsSecretsOutOfArgv(t *testing.T) {
	cfg := nativeTestConfig(t)
	t.Setenv("HOST_SECRET", "not-inherited")
	env := nativeBaseEnv(cfg)
	env["HTTPS_PROXY"] = "http://user:session-secret@127.0.0.1:1234"
	cmd := nativeCommand(context.Background(), &RunConfig{SandboxCfg: cfg, Command: []string{"printf", "%s", "a'b"}}, "profile", env)
	if cmd.Path != seatbelt.Executable || cmd.Dir != cfg.ProjectDir {
		t.Fatalf("wrong launch: %s at %s", cmd.Path, cmd.Dir)
	}
	if strings.Contains(strings.Join(cmd.Args, " "), "session-secret") {
		t.Fatal("credential in argv")
	}
	got := strings.Join(cmd.Env, "\n")
	if !strings.Contains(got, "session-secret") || strings.Contains(got, "HOST_SECRET=") {
		t.Fatalf("wrong environment isolation")
	}
}

func TestPrepareNativeProxyAndEnvironment(t *testing.T) {
	cfg := nativeTestConfig(t)
	cfg.ToolsConfig = map[string]any{}
	for _, tool := range tools.All() {
		cfg.ToolsConfig[tool.Name()] = map[string]any{"mount_mode": "disabled"}
	}
	cfg.ProxyEnabled = true
	cfg.EnvVars = map[string]string{"EXPLICIT": "configured", "HTTPS_PROXY": "must-be-overridden"}
	run := &RunConfig{SandboxCfg: cfg, ProxyPort: 1234, ProxyAuthToken: "session-secret"}
	p, err := prepareNative(run)
	if err != nil {
		t.Fatal(err)
	}
	if p.policy.ProxyPort != 1234 || !p.policy.HideEnvFiles || !p.policy.ConfigHidden {
		t.Fatalf("lost security policy: %+v", p.policy)
	}
	if p.env["EXPLICIT"] != "configured" || !strings.Contains(p.env["HTTPS_PROXY"], "session-secret") {
		t.Fatal("wrong environment precedence")
	}
	if p.env["HOME"] != cfg.SandboxHome || p.env["TMPDIR"] != filepath.Join(cfg.SandboxHome, ".tmp") {
		t.Fatal("private paths were not exported")
	}
	run.ProxyAuthToken = ""
	if _, err := prepareNative(run); err == nil {
		t.Fatal("proxy enabled without authentication")
	}
}

func nativeWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func nativeRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNativeSplitRefreshPreservesNestedData(t *testing.T) {
	cfg := nativeTestConfig(t)
	source := filepath.Join(cfg.HomeDir, ".agent")
	nativeWrite(t, filepath.Join(source, "config"), "host config")
	nativeWrite(t, filepath.Join(source, "sessions", "old"), "host session")
	bindings := []tools.Binding{
		{Source: source, Type: tools.MountTmpOverlay},
		{Source: filepath.Join(source, "sessions"), Type: tools.MountOverlay},
	}
	root, err := os.OpenRoot(cfg.SandboxHome)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	p := &nativePlan{setup: filepath.Join(cfg.SandboxRoot, "setup")}
	if err := p.applyBindings(root, cfg, bindings); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(cfg.SandboxHome, ".agent")
	nativeWrite(t, filepath.Join(dest, "config"), "sandbox config")
	nativeWrite(t, filepath.Join(dest, "leftover"), "disposable")
	nativeWrite(t, filepath.Join(dest, "sessions", "new"), "sandbox session")
	if err := os.Remove(filepath.Join(dest, "sessions", "old")); err != nil {
		t.Fatal(err)
	}
	if err := p.applyBindings(root, cfg, bindings); err != nil {
		t.Fatal(err)
	}
	if nativeRead(t, filepath.Join(dest, "config")) != "host config" || nativeRead(t, filepath.Join(dest, "sessions", "new")) != "sandbox session" {
		t.Fatal("config refresh lost nested state")
	}
	for _, path := range []string{"leftover", "sessions/old"} {
		if _, err := os.Stat(filepath.Join(dest, path)); !os.IsNotExist(err) {
			t.Fatalf("unexpected retained/resurrected %s: %v", path, err)
		}
	}
}

func TestNativeCopyDoesNotFollowLinks(t *testing.T) {
	cfg := nativeTestConfig(t)
	victim := filepath.Join(cfg.HomeDir, "secret")
	nativeWrite(t, victim, "secret")
	src := filepath.Join(cfg.HomeDir, "config")
	if err := os.Mkdir(src, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(cfg.SandboxHome)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := nativeCopy(root, src, "copied", nil); err != nil {
		t.Fatal(err)
	}
	info, err := root.Lstat("copied/link")
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("source symlink was dereferenced: %v", err)
	}
	if err := root.Symlink(cfg.HomeDir, "escape"); err != nil {
		t.Fatal(err)
	}
	if err := nativeCopy(root, victim, "escape/secret", nil); err == nil {
		t.Fatal("destination symlink escaped home")
	}
	if nativeRead(t, victim) != "secret" {
		t.Fatal("host file changed")
	}
}

func TestNativeBindingsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		binding func(*sandbox.Config) tools.Binding
	}{
		{"outside remap", func(c *sandbox.Config) tools.Binding {
			return tools.Binding{Source: c.ProjectDir, Dest: "/unrelated", Type: tools.MountOverlay}
		}},
		{"host writable", func(c *sandbox.Config) tools.Binding {
			return tools.Binding{Source: filepath.Join(c.HomeDir, "config"), Type: tools.MountBind}
		}},
		{"layers", func(c *sandbox.Config) tools.Binding {
			return tools.Binding{Source: c.ProjectDir, OverlaySources: []string{c.HomeDir}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := nativeTestConfig(t)
			nativeWrite(t, filepath.Join(cfg.HomeDir, "config"), "host")
			root, err := os.OpenRoot(cfg.SandboxHome)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			if err := (&nativePlan{setup: filepath.Join(cfg.SandboxRoot, "setup")}).applyBindings(root, cfg, []tools.Binding{tc.binding(cfg)}); err == nil {
				t.Fatal("unsupported binding accepted")
			}
		})
	}
}

func TestNativeGitSnapshot(t *testing.T) {
	cfg := nativeTestConfig(t)
	nativeGitFixture(t, cfg.ProjectDir, "main")
	nativeGitHost(t, cfg.ProjectDir, "config", "remote.origin.url", "https://secret@example.test/repo")
	p := nativeGitPlan(t, cfg)
	if got := nativeGitRun(t, p, cfg.ProjectDir, "config", "remote.origin.url"); got != "https://example.test/repo" {
		t.Fatalf("snapshot URL = %q", got)
	}
	if got := nativeGitHost(t, cfg.ProjectDir, "config", "remote.origin.url"); got != "https://secret@example.test/repo" {
		t.Fatal("host config changed")
	}
	if !slices.Contains(p.policy.DenyRead, filepath.Join(cfg.ProjectDir, ".git")) {
		t.Fatal("original metadata not hidden")
	}
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE"} {
		if _, ok := p.env[key]; ok {
			t.Fatalf("global routing environment %s", key)
		}
	}
}

func TestPrepareNativeWithRealGit(t *testing.T) {
	cfg := nativeTestConfig(t)
	t.Setenv("HOME", cfg.HomeDir)
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	cfg.ToolsConfig = map[string]any{}
	for _, tool := range tools.All() {
		if tool.Name() != "git" {
			cfg.ToolsConfig[tool.Name()] = map[string]any{"mount_mode": "disabled"}
		}
	}
	cmd := exec.Command("git", "init", cfg.ProjectDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	p, err := prepareNative(&RunConfig{SandboxCfg: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if p.env["GIT_DIR"] != "" || p.env["GIT_WORK_TREE"] != "" || cfg.CommandPathPrefix == "" {
		t.Fatal("expected a dispatcher without global Git routing")
	}
	if got := nativeGitRun(t, p, cfg.ProjectDir, "rev-parse", "--absolute-git-dir"); !nativeWithin(filepath.Join(p.setup, "git"), got) {
		t.Fatalf("git bypassed snapshot: %q", got)
	}
	nativeGitRun(t, p, cfg.ProjectDir, "status", "--porcelain")
}

func TestNativeGeneratedConfigIsNotASeed(t *testing.T) {
	cfg := nativeTestConfig(t)
	setup := filepath.Join(cfg.SandboxRoot, "setup")
	source := filepath.Join(setup, ".gitconfig.safe")
	nativeWrite(t, source, "fresh")
	root, err := os.OpenRoot(cfg.SandboxHome)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	p := &nativePlan{setup: setup}
	bindings := []tools.Binding{{Source: source, Dest: filepath.Join(cfg.HomeDir, ".gitconfig"), HomeRelativeDest: true, Type: tools.MountOverlay}}
	if err := p.applyBindings(root, cfg, bindings); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(cfg.SandboxHome, ".gitconfig")
	nativeWrite(t, dest, "stale")
	if err := p.applyBindings(root, cfg, bindings); err != nil {
		t.Fatal(err)
	}
	if nativeRead(t, dest) != "fresh" || !slices.Contains(p.policy.DenyWrite, dest) {
		t.Fatal("generated config treated as persistent writable seed")
	}
}

func TestNativeReadOnlyDestinationAliases(t *testing.T) {
	cfg := nativeTestConfig(t)
	source := filepath.Join(cfg.HomeDir, ".config", "app", "rc")
	nativeWrite(t, source, "read only")
	if err := os.Mkdir(filepath.Join(cfg.SandboxHome, "alternate"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("alternate", filepath.Join(cfg.SandboxHome, ".config")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(cfg.SandboxHome)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	p := &nativePlan{setup: filepath.Join(cfg.SandboxRoot, "setup")}
	if err := p.applyBindings(root, cfg, []tools.Binding{{Source: source, Type: tools.MountBind, ReadOnly: true}}); err != nil {
		t.Fatal(err)
	}
	for _, parent := range []string{".config", "alternate"} {
		path := filepath.Join(cfg.SandboxHome, parent, "app", "rc")
		if !slices.Contains(p.policy.DenyWrite, path) {
			t.Fatalf("missing deny for %s", path)
		}
	}
}

func TestNativeGeneratedShellBindings(t *testing.T) {
	cfg := nativeTestConfig(t)
	setup := filepath.Join(cfg.SandboxRoot, "setup")
	for _, path := range []string{".config/starship.toml", ".tmux.conf", ".p10k.zsh", ".oh-my-zsh-custom/plugin.zsh"} {
		nativeWrite(t, filepath.Join(setup, path), "generated")
	}
	root, err := os.OpenRoot(cfg.SandboxHome)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	p := &nativePlan{setup: setup}
	for _, tool := range []tools.Tool{&tools.Starship{}, &tools.Tmux{}, &tools.Powerlevel10k{}, &tools.OhMyZsh{}} {
		bindings := tool.Bindings(cfg.HomeDir, setup)
		for i := range bindings {
			sandbox.ResolveBindingType(&bindings[i], "", "split")
		}
		if err := p.applyBindings(root, cfg, bindings); err != nil {
			t.Fatalf("%s: %v", tool.Name(), err)
		}
	}
	for _, path := range []string{".config/starship.toml", ".tmux.conf", ".p10k.zsh", ".oh-my-zsh/custom/plugin.zsh"} {
		if nativeRead(t, filepath.Join(cfg.SandboxHome, path)) != "generated" {
			t.Fatalf("missing %s", path)
		}
	}
}

func TestNativeIntegrationRequests(t *testing.T) {
	for _, section := range []map[string]any{nil, {"enabled": false}, {"mode": "disabled"}, {"mode": "auto"}} {
		if nativeIntegrationRequested(section) {
			t.Fatalf("optional integration required: %v", section)
		}
	}
	for _, section := range []map[string]any{{"enabled": true}, {"mode": "enforce"}, {"mount_mode": "readonly"}} {
		if !nativeIntegrationRequested(section) {
			t.Fatalf("explicit integration ignored: %v", section)
		}
	}
}
