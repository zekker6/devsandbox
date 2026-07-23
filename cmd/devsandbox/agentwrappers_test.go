package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"devsandbox/internal/shellwrap"
)

// newWrapperEnv builds an env rooted at a temp home, with XDG_CONFIG_HOME
// cleared so InstallPath resolves under that home.
func newWrapperEnv(t *testing.T, shell string, agents ...string) (wrapperEnv, *strings.Builder) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", "")
	out := &strings.Builder{}
	return wrapperEnv{
		shell:          shell,
		homeDir:        t.TempDir(),
		devsandboxPath: "/opt/bin/devsandbox",
		agents:         agents,
		getenv:         func(string) string { return "" },
		out:            out,
	}, out
}

func TestDetectShell(t *testing.T) {
	tests := []struct {
		name      string
		flag      string
		shellEnv  string
		want      string
		wantError bool
	}{
		{name: "flag wins", flag: "fish", shellEnv: "/bin/bash", want: "fish"},
		{name: "shell env path", shellEnv: "/usr/bin/fish", want: "fish"},
		{name: "bash", shellEnv: "/bin/bash", want: "bash"},
		{name: "zsh", shellEnv: "/bin/zsh", want: "zsh"},
		{name: "login shell argv0", shellEnv: "-bash", want: "bash"},
		{name: "flag as path", flag: "/usr/local/bin/zsh", want: "zsh"},
		{name: "unsupported shell", shellEnv: "/usr/bin/nu", wantError: true},
		{name: "unsupported flag", flag: "nu", shellEnv: "/bin/bash", wantError: true},
		{name: "nothing to detect", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := detectShell(tt.flag, tt.shellEnv)
			if tt.wantError {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				if !strings.Contains(err.Error(), "fish") {
					t.Errorf("error should name the supported shells, got %q", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("detectShell = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInstalledAgentsFiltersToWhatIsPresent(t *testing.T) {
	lookPath := fakeLookPath(map[string]string{"claude": "/usr/bin/claude"})
	got := installedAgents([]string{"claude", "pi", "codex"}, lookPath)
	if !reflect.DeepEqual(got, []string{"claude"}) {
		t.Errorf("installedAgents = %#v, want [claude]", got)
	}
	if got := installedAgents([]string{"pi"}, lookPath); len(got) != 0 {
		t.Errorf("installedAgents = %#v, want empty", got)
	}
}

func TestInstallWritesExpectedPathAndContent(t *testing.T) {
	for _, shell := range shellwrap.SupportedShells() {
		t.Run(shell, func(t *testing.T) {
			env, out := newWrapperEnv(t, shell, "claude")
			if err := installWrappers(env); err != nil {
				t.Fatalf("install: %v", err)
			}

			path := shellwrap.InstallPath(shell, env.homeDir)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read installed snippet: %v", err)
			}
			want, err := shellwrap.Snippet(shell, env.devsandboxPath, []string{"claude"})
			if err != nil {
				t.Fatalf("snippet: %v", err)
			}
			if string(data) != want {
				t.Errorf("installed content =\n%s\nwant\n%s", data, want)
			}
			if !strings.Contains(out.String(), path) {
				t.Errorf("output should name the install path, got %q", out)
			}
			if !strings.Contains(out.String(), "claude") {
				t.Errorf("output should name the wrapped agents, got %q", out)
			}

			line := shellwrap.SourceLine(shell, path)
			if shell == shellwrap.ShellFish {
				if line != "" {
					t.Fatalf("fish should have no source line, got %q", line)
				}
				if strings.Contains(out.String(), "source") {
					t.Errorf("fish install should not print a source line, got %q", out)
				}
				return
			}
			if !strings.Contains(out.String(), line) {
				t.Errorf("output should print the guarded source line %q, got %q", line, out)
			}
		})
	}
}

func TestInstallNeverEditsStartupFiles(t *testing.T) {
	env, _ := newWrapperEnv(t, shellwrap.ShellBash, "claude")
	rc := filepath.Join(env.homeDir, ".bashrc")
	if err := os.WriteFile(rc, []byte("export FOO=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installWrappers(env); err != nil {
		t.Fatalf("install: %v", err)
	}
	data, err := os.ReadFile(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "export FOO=1\n" {
		t.Errorf(".bashrc was modified: %q", data)
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	env, _ := newWrapperEnv(t, shellwrap.ShellFish, "claude")
	if err := installWrappers(env); err != nil {
		t.Fatalf("first install: %v", err)
	}
	path := shellwrap.InstallPath(env.shell, env.homeDir)
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	out := &strings.Builder{}
	env.out = out
	if err := installWrappers(env); err != nil {
		t.Fatalf("second install: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("second install changed the file")
	}
	if !strings.Contains(out.String(), "Already up to date") {
		t.Errorf("second install should report it was already current, got %q", out)
	}
}

func TestInstallRefusesToClobberForeignFile(t *testing.T) {
	env, _ := newWrapperEnv(t, shellwrap.ShellFish, "claude")
	path := shellwrap.InstallPath(env.shell, env.homeDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const handWritten = "# mine, not devsandbox's\nfunction claude; end\n"
	if err := os.WriteFile(path, []byte(handWritten), 0o644); err != nil {
		t.Fatal(err)
	}

	err := installWrappers(env)
	if err == nil {
		t.Fatal("expected install to refuse a foreign file")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error should name the file, got %q", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != handWritten {
		t.Errorf("foreign file was modified: %q", data)
	}
}

func TestInstallWithNoInstalledAgentsErrors(t *testing.T) {
	env, _ := newWrapperEnv(t, shellwrap.ShellBash)
	err := installWrappers(env)
	if err == nil {
		t.Fatal("expected an error when no supported agent is installed")
	}
	if !strings.Contains(err.Error(), "claude") {
		t.Errorf("error should name the supported agents, got %q", err)
	}
	if _, statErr := os.Stat(shellwrap.InstallPath(env.shell, env.homeDir)); statErr == nil {
		t.Error("no snippet should have been written")
	}
}

func TestUninstallRemovesGeneratedFileAndPrintsSourceLine(t *testing.T) {
	env, _ := newWrapperEnv(t, shellwrap.ShellBash, "claude")
	if err := installWrappers(env); err != nil {
		t.Fatalf("install: %v", err)
	}
	path := shellwrap.InstallPath(env.shell, env.homeDir)

	out := &strings.Builder{}
	env.out = out
	if err := uninstallWrappers(env); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("snippet still present after uninstall: %v", err)
	}
	if line := shellwrap.SourceLine(env.shell, path); !strings.Contains(out.String(), line) {
		t.Errorf("uninstall should print the rc line to remove %q, got %q", line, out)
	}
}

func TestUninstallRefusesForeignFile(t *testing.T) {
	env, _ := newWrapperEnv(t, shellwrap.ShellZsh, "claude")
	path := shellwrap.InstallPath(env.shell, env.homeDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("claude() { :; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := uninstallWrappers(env); err == nil {
		t.Fatal("expected uninstall to refuse a foreign file")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("foreign file was removed: %v", err)
	}
}

func TestUninstallWithoutInstallIsNoop(t *testing.T) {
	env, out := newWrapperEnv(t, shellwrap.ShellFish, "claude")
	if err := uninstallWrappers(env); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !strings.Contains(out.String(), "Nothing to remove") {
		t.Errorf("output = %q, want a nothing-to-remove notice", out)
	}
}

func TestStatusReportsEachState(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		env, out := newWrapperEnv(t, shellwrap.ShellFish, "claude")
		if err := statusWrappers(env); err != nil {
			t.Fatalf("status: %v", err)
		}
		if !strings.Contains(out.String(), "not installed") {
			t.Errorf("output = %q, want not installed", out)
		}
		if !strings.Contains(out.String(), "would wrap: claude") {
			t.Errorf("output = %q, want the agents that would be wrapped", out)
		}
	})

	t.Run("current", func(t *testing.T) {
		env, _ := newWrapperEnv(t, shellwrap.ShellFish, "claude")
		if err := installWrappers(env); err != nil {
			t.Fatalf("install: %v", err)
		}
		out := &strings.Builder{}
		env.out = out
		if err := statusWrappers(env); err != nil {
			t.Fatalf("status: %v", err)
		}
		if !strings.Contains(out.String(), "installed (current)") {
			t.Errorf("output = %q, want installed (current)", out)
		}
		if !strings.Contains(out.String(), "Agents:  claude") {
			t.Errorf("output = %q, want the wrapped agents read back from the file", out)
		}
	})

	t.Run("stale", func(t *testing.T) {
		env, _ := newWrapperEnv(t, shellwrap.ShellFish, "claude")
		if err := installWrappers(env); err != nil {
			t.Fatalf("install: %v", err)
		}
		// A moved devsandbox binary is the common way a snippet goes stale.
		env.devsandboxPath = "/somewhere/else/devsandbox"
		out := &strings.Builder{}
		env.out = out
		if err := statusWrappers(env); err != nil {
			t.Fatalf("status: %v", err)
		}
		if !strings.Contains(out.String(), "installed (out of date)") {
			t.Errorf("output = %q, want installed (out of date)", out)
		}
		if !strings.Contains(out.String(), "agent-wrappers install") {
			t.Errorf("output = %q, want the refresh hint", out)
		}
	})

	t.Run("foreign", func(t *testing.T) {
		env, out := newWrapperEnv(t, shellwrap.ShellFish, "claude")
		path := shellwrap.InstallPath(env.shell, env.homeDir)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("function claude; end\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := statusWrappers(env); err != nil {
			t.Fatalf("status: %v", err)
		}
		if !strings.Contains(out.String(), "not written by devsandbox") {
			t.Errorf("output = %q, want the foreign-file state", out)
		}
	})
}

func TestStatusReportsWhetherAnRCFileSourcesTheSnippet(t *testing.T) {
	env, _ := newWrapperEnv(t, shellwrap.ShellBash, "claude")
	if err := installWrappers(env); err != nil {
		t.Fatalf("install: %v", err)
	}
	path := shellwrap.InstallPath(env.shell, env.homeDir)

	out := &strings.Builder{}
	env.out = out
	if err := statusWrappers(env); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out.String(), "no startup file references it") {
		t.Errorf("output = %q, want the not-sourced notice", out)
	}

	rc := filepath.Join(env.homeDir, ".bash_profile")
	if err := os.WriteFile(rc, []byte(shellwrap.SourceLine(env.shell, path)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := statusWrappers(env); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out.String(), rc) {
		t.Errorf("output = %q, want the startup file that sources the snippet", out)
	}
}

func TestStatusWarnsAboutHerdrPaneShell(t *testing.T) {
	writeHerdrConfig := func(t *testing.T, dir, shell string) string {
		t.Helper()
		p := filepath.Join(dir, "herdr-config.toml")
		body := "[terminal]\ndefault_shell = \"" + shell + "\"\nshell_mode = \"auto\"\n"
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("unsupported pane shell", func(t *testing.T) {
		env, out := newWrapperEnv(t, shellwrap.ShellFish, "claude")
		cfg := writeHerdrConfig(t, t.TempDir(), "/usr/bin/nu")
		env.getenv = func(k string) string {
			if k == "HERDR_CONFIG_PATH" {
				return cfg
			}
			return ""
		}
		if err := installWrappers(env); err != nil {
			t.Fatalf("install: %v", err)
		}
		if err := statusWrappers(env); err != nil {
			t.Fatalf("status: %v", err)
		}
		if !strings.Contains(out.String(), "herdr pane shell: nu") {
			t.Errorf("output = %q, want herdr's configured pane shell", out)
		}
		if !strings.Contains(out.String(), "warning") {
			t.Errorf("output = %q, want a warning for an unsupported pane shell", out)
		}
	})

	t.Run("supported pane shell without a snippet", func(t *testing.T) {
		env, out := newWrapperEnv(t, shellwrap.ShellFish, "claude")
		cfg := writeHerdrConfig(t, t.TempDir(), "/bin/zsh")
		env.getenv = func(k string) string {
			if k == "HERDR_CONFIG_PATH" {
				return cfg
			}
			return ""
		}
		if err := installWrappers(env); err != nil {
			t.Fatalf("install: %v", err)
		}
		if err := statusWrappers(env); err != nil {
			t.Fatalf("status: %v", err)
		}
		if !strings.Contains(out.String(), "--shell zsh") {
			t.Errorf("output = %q, want the install hint for herdr's pane shell", out)
		}
	})

	t.Run("pane shell already wrapped", func(t *testing.T) {
		env, out := newWrapperEnv(t, shellwrap.ShellFish, "claude")
		cfg := writeHerdrConfig(t, t.TempDir(), "/usr/bin/fish")
		env.getenv = func(k string) string {
			if k == "HERDR_CONFIG_PATH" {
				return cfg
			}
			return ""
		}
		if err := installWrappers(env); err != nil {
			t.Fatalf("install: %v", err)
		}
		if err := statusWrappers(env); err != nil {
			t.Fatalf("status: %v", err)
		}
		if strings.Contains(out.String(), "warning") {
			t.Errorf("output = %q, want no warning when the pane shell is wrapped", out)
		}
	})
}

func TestHerdrPaneShell(t *testing.T) {
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".config", "herdr")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "config.toml")

	env := func(vals map[string]string) func(string) string {
		return func(k string) string { return vals[k] }
	}

	t.Run("falls back to SHELL without a config", func(t *testing.T) {
		shell, source := herdrPaneShell(home, env(map[string]string{"SHELL": "/bin/bash"}))
		if shell != "bash" || source != "$SHELL" {
			t.Errorf("got (%q, %q), want (bash, $SHELL)", shell, source)
		}
	})

	t.Run("reads terminal.default_shell", func(t *testing.T) {
		if err := os.WriteFile(cfgPath, []byte("[terminal]\ndefault_shell = \"/usr/bin/fish\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		shell, source := herdrPaneShell(home, env(map[string]string{"SHELL": "/bin/bash"}))
		if shell != "fish" || source != cfgPath {
			t.Errorf("got (%q, %q), want (fish, %s)", shell, source, cfgPath)
		}
	})

	t.Run("empty default_shell falls back to SHELL", func(t *testing.T) {
		if err := os.WriteFile(cfgPath, []byte("[terminal]\ndefault_shell = \"\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		shell, source := herdrPaneShell(home, env(map[string]string{"SHELL": "/bin/zsh"}))
		if shell != "zsh" || source != "$SHELL" {
			t.Errorf("got (%q, %q), want (zsh, $SHELL)", shell, source)
		}
	})

	t.Run("malformed config falls back to SHELL", func(t *testing.T) {
		if err := os.WriteFile(cfgPath, []byte("[terminal\nthis is not toml"), 0o644); err != nil {
			t.Fatal(err)
		}
		shell, source := herdrPaneShell(home, env(map[string]string{"SHELL": "/bin/bash"}))
		if shell != "bash" || source != "$SHELL" {
			t.Errorf("got (%q, %q), want (bash, $SHELL)", shell, source)
		}
	})

	t.Run("nothing resolvable", func(t *testing.T) {
		if err := os.Remove(cfgPath); err != nil {
			t.Fatal(err)
		}
		shell, source := herdrPaneShell(home, env(nil))
		if shell != "" || source != "" {
			t.Errorf("got (%q, %q), want empty", shell, source)
		}
	})

	t.Run("honors XDG_CONFIG_HOME", func(t *testing.T) {
		xdg := t.TempDir()
		if err := os.MkdirAll(filepath.Join(xdg, "herdr"), 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(xdg, "herdr", "config.toml")
		if err := os.WriteFile(p, []byte("[terminal]\ndefault_shell = \"/bin/zsh\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		shell, source := herdrPaneShell(home, env(map[string]string{"XDG_CONFIG_HOME": xdg}))
		if shell != "zsh" || source != p {
			t.Errorf("got (%q, %q), want (zsh, %s)", shell, source, p)
		}
	})
}

func TestWrappedAgentsReadsBackEverySnippetForm(t *testing.T) {
	for _, shell := range shellwrap.SupportedShells() {
		t.Run(shell, func(t *testing.T) {
			snippet, err := shellwrap.Snippet(shell, "/opt/bin/devsandbox", []string{"claude", "pi"})
			if err != nil {
				t.Fatal(err)
			}
			got := wrappedAgents(snippet)
			if !reflect.DeepEqual(got, []string{"claude", "pi"}) {
				t.Errorf("wrappedAgents = %#v, want [claude pi]", got)
			}
		})
	}
	if got := wrappedAgents("# nothing here\n"); len(got) != 0 {
		t.Errorf("wrappedAgents = %#v, want empty", got)
	}
}

func TestAgentWrappersCommandRegistersSubcommands(t *testing.T) {
	cmd := newAgentWrappersCmd()
	want := map[string]bool{"install": false, "status": false, "uninstall": false}
	for _, sub := range cmd.Commands() {
		if _, ok := want[sub.Name()]; ok {
			want[sub.Name()] = true
			if sub.Flags().Lookup("shell") == nil {
				t.Errorf("%s is missing the --shell flag", sub.Name())
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("agent-wrappers is missing the %q subcommand", name)
		}
	}
}
