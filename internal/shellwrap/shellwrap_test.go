package shellwrap

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testDevsandbox = "/usr/local/bin/devsandbox"

func TestSnippetFishExactOutput(t *testing.T) {
	got, err := Snippet(ShellFish, testDevsandbox, []string{"claude"})
	if err != nil {
		t.Fatalf("Snippet: %v", err)
	}
	want := Header + `
if test -z "$DEVSANDBOX"
    function claude --wraps claude
        if test -x '/usr/local/bin/devsandbox'
            '/usr/local/bin/devsandbox' run-agent claude $argv
        else
            printf '%s %s %s\n' "devsandbox: no executable at" '/usr/local/bin/devsandbox' "- run 'devsandbox agent-wrappers install' to refresh the wrappers" >&2
            return 127
        end
    end
    function claude-no-ds --wraps claude
        command claude $argv
    end
end
`
	if got != want {
		t.Errorf("fish snippet mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSnippetFishSeveralAgents(t *testing.T) {
	got, err := Snippet(ShellFish, testDevsandbox, []string{"claude", "codex"})
	if err != nil {
		t.Fatalf("Snippet: %v", err)
	}
	want := Header + `
if test -z "$DEVSANDBOX"
    function claude --wraps claude
        if test -x '/usr/local/bin/devsandbox'
            '/usr/local/bin/devsandbox' run-agent claude $argv
        else
            printf '%s %s %s\n' "devsandbox: no executable at" '/usr/local/bin/devsandbox' "- run 'devsandbox agent-wrappers install' to refresh the wrappers" >&2
            return 127
        end
    end
    function claude-no-ds --wraps claude
        command claude $argv
    end
    function codex --wraps codex
        if test -x '/usr/local/bin/devsandbox'
            '/usr/local/bin/devsandbox' run-agent codex $argv
        else
            printf '%s %s %s\n' "devsandbox: no executable at" '/usr/local/bin/devsandbox' "- run 'devsandbox agent-wrappers install' to refresh the wrappers" >&2
            return 127
        end
    end
    function codex-no-ds --wraps codex
        command codex $argv
    end
end
`
	if got != want {
		t.Errorf("fish snippet mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSnippetBashExactOutput(t *testing.T) {
	got, err := Snippet(ShellBash, testDevsandbox, []string{"claude"})
	if err != nil {
		t.Fatalf("Snippet: %v", err)
	}
	want := Header + `
if [ -n "${DEVSANDBOX:-}" ]; then :; else
  claude() { if [ -x '/usr/local/bin/devsandbox' ]; then '/usr/local/bin/devsandbox' run-agent claude "$@"; else printf '%s %s %s\n' "devsandbox: no executable at" '/usr/local/bin/devsandbox' "- run 'devsandbox agent-wrappers install' to refresh the wrappers" >&2; return 127; fi; }
  claude-no-ds() { command claude "$@"; }
fi
`
	if got != want {
		t.Errorf("bash snippet mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSnippetZshSeveralAgents(t *testing.T) {
	got, err := Snippet(ShellZsh, testDevsandbox, []string{"claude", "codex"})
	if err != nil {
		t.Fatalf("Snippet: %v", err)
	}
	want := Header + `
if [ -n "${DEVSANDBOX:-}" ]; then :; else
  claude() { if [ -x '/usr/local/bin/devsandbox' ]; then '/usr/local/bin/devsandbox' run-agent claude "$@"; else printf '%s %s %s\n' "devsandbox: no executable at" '/usr/local/bin/devsandbox' "- run 'devsandbox agent-wrappers install' to refresh the wrappers" >&2; return 127; fi; }
  claude-no-ds() { command claude "$@"; }
  codex() { if [ -x '/usr/local/bin/devsandbox' ]; then '/usr/local/bin/devsandbox' run-agent codex "$@"; else printf '%s %s %s\n' "devsandbox: no executable at" '/usr/local/bin/devsandbox' "- run 'devsandbox agent-wrappers install' to refresh the wrappers" >&2; return 127; fi; }
  codex-no-ds() { command codex "$@"; }
fi
`
	if got != want {
		t.Errorf("zsh snippet mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

// The guard is not decoration: fish's conf.d and the bash/zsh rc files are
// bound into the sandbox, where an unguarded wrapper would recurse.
func TestSnippetGuardWrapsEveryDefinition(t *testing.T) {
	for _, shell := range SupportedShells() {
		t.Run(shell, func(t *testing.T) {
			got, err := Snippet(shell, testDevsandbox, []string{"claude", "codex"})
			if err != nil {
				t.Fatalf("Snippet: %v", err)
			}
			lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
			if len(lines) < 3 {
				t.Fatalf("snippet too short:\n%s", got)
			}
			if lines[0] != Header {
				t.Errorf("first line = %q, want header", lines[0])
			}
			guard := lines[1]
			if !strings.Contains(guard, "DEVSANDBOX") {
				t.Errorf("second line %q does not guard on DEVSANDBOX", guard)
			}
			last := lines[len(lines)-1]
			if last != "end" && last != "fi" {
				t.Errorf("last line = %q, want the guard's closing keyword", last)
			}
			for _, l := range lines[2 : len(lines)-1] {
				if strings.TrimSpace(l) == "" {
					continue
				}
				if !strings.HasPrefix(l, " ") {
					t.Errorf("definition line %q is not indented inside the guard", l)
				}
			}
		})
	}
}

// Empty and unset must mean the same thing in every shell, which is why the
// guard tests non-emptiness rather than using fish's `set -q`.
func TestSnippetGuardUsesNonEmptySemantics(t *testing.T) {
	fish, err := Snippet(ShellFish, testDevsandbox, []string{"claude"})
	if err != nil {
		t.Fatalf("Snippet: %v", err)
	}
	if !strings.Contains(fish, `if test -z "$DEVSANDBOX"`) {
		t.Errorf("fish guard is not a non-empty test:\n%s", fish)
	}
	if strings.Contains(fish, "set -q") {
		t.Errorf("fish guard uses set -q, which is true for an empty value:\n%s", fish)
	}
	for _, shell := range []string{ShellBash, ShellZsh} {
		got, err := Snippet(shell, testDevsandbox, []string{"claude"})
		if err != nil {
			t.Fatalf("Snippet(%s): %v", shell, err)
		}
		if !strings.Contains(got, `if [ -n "${DEVSANDBOX:-}" ]; then :; else`) {
			t.Errorf("%s guard is not a non-empty test:\n%s", shell, got)
		}
	}
}

func TestSnippetUsesAbsolutePathNotCommandLookup(t *testing.T) {
	for _, shell := range SupportedShells() {
		got, err := Snippet(shell, testDevsandbox, []string{"claude"})
		if err != nil {
			t.Fatalf("Snippet(%s): %v", shell, err)
		}
		if !strings.Contains(got, testDevsandbox) {
			t.Errorf("%s snippet does not embed the absolute devsandbox path:\n%s", shell, got)
		}
		if !strings.Contains(got, "command claude") {
			t.Errorf("%s snippet's -no-ds form does not use command:\n%s", shell, got)
		}
	}
}

// A missing baked path must fail closed. The alternatives both hand execution
// to something the sandbox can influence: falling through to the real agent
// runs it unsandboxed, and resolving `devsandbox` through PATH runs whatever
// binary PATH names first - a project-local bin directory is sandbox-writable,
// and the baked path is legitimately gone after every upgrade that moves it.
func TestSnippetFailsClosedWhenBakedPathIsGone(t *testing.T) {
	tests := []struct {
		shell string
		guard string
	}{
		{ShellFish, "if test -x '" + testDevsandbox + "'"},
		{ShellBash, "if [ -x '" + testDevsandbox + "' ]"},
		{ShellZsh, "if [ -x '" + testDevsandbox + "' ]"},
	}
	for _, tt := range tests {
		got, err := Snippet(tt.shell, testDevsandbox, []string{"claude"})
		if err != nil {
			t.Fatalf("Snippet(%s): %v", tt.shell, err)
		}
		if !strings.Contains(got, tt.guard) {
			t.Errorf("%s snippet does not guard the baked path on existence:\n%s", tt.shell, got)
		}
		if strings.Contains(got, "command devsandbox") {
			t.Errorf("%s snippet resolves devsandbox through PATH:\n%s", tt.shell, got)
		}
		if strings.Contains(got, "command claude run-agent") || strings.Contains(got, "command devsandbox claude") {
			t.Errorf("%s snippet skips run-agent:\n%s", tt.shell, got)
		}
		if !strings.Contains(got, "agent-wrappers install") {
			t.Errorf("%s snippet does not name the reinstall command:\n%s", tt.shell, got)
		}
		if !strings.Contains(got, "return 127") {
			t.Errorf("%s snippet does not exit non-zero when the binary is gone:\n%s", tt.shell, got)
		}
	}
}

// The snippet is executed by a shell, so the guard and the invocation must
// carry the same quoting as the path they protect.
func TestSnippetQuotesPathInExistenceGuard(t *testing.T) {
	const awkward = "/opt/dev sandbox/dev'sandbox"
	for _, shell := range SupportedShells() {
		got, err := Snippet(shell, awkward, []string{"claude"})
		if err != nil {
			t.Fatalf("Snippet(%s): %v", shell, err)
		}
		if strings.Contains(got, "-x "+awkward) {
			t.Errorf("%s snippet leaves the guarded path unquoted:\n%s", shell, got)
		}
	}
}

func TestSnippetErrors(t *testing.T) {
	tests := []struct {
		name    string
		shell   string
		path    string
		agents  []string
		wantSub string
	}{
		{"unsupported shell", "nu", testDevsandbox, []string{"claude"}, "unsupported shell"},
		{"empty shell", "", testDevsandbox, []string{"claude"}, "unsupported shell"},
		{"relative path", ShellBash, "devsandbox", []string{"claude"}, "must be absolute"},
		{"empty path", ShellBash, "", []string{"claude"}, "must be absolute"},
		{"no agents", ShellBash, testDevsandbox, nil, "no agents"},
		{"empty agent name", ShellBash, testDevsandbox, []string{""}, "empty agent name"},
		{"agent with space", ShellBash, testDevsandbox, []string{"cl aude"}, "invalid agent name"},
		{"agent with metachar", ShellFish, testDevsandbox, []string{"claude;rm -rf /"}, "invalid agent name"},
		{"agent leading hyphen", ShellBash, testDevsandbox, []string{"-claude"}, "invalid agent name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Snippet(tt.shell, tt.path, tt.agents)
			if err == nil {
				t.Fatalf("expected an error, got snippet:\n%s", got)
			}
			if got != "" {
				t.Errorf("expected no snippet on error, got:\n%s", got)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantSub)
			}
		})
	}
}

func TestSnippetQuotesPathWithSpaces(t *testing.T) {
	const p = "/opt/my tools/devsandbox"
	for _, shell := range SupportedShells() {
		got, err := Snippet(shell, p, []string{"claude"})
		if err != nil {
			t.Fatalf("Snippet(%s): %v", shell, err)
		}
		if !strings.Contains(got, "'"+p+"'") {
			t.Errorf("%s snippet does not quote a path with spaces:\n%s", shell, got)
		}
	}
}

func TestInstallPath(t *testing.T) {
	tests := []struct {
		name    string
		shell   string
		xdg     string
		want    string
		wantAbs bool
	}{
		{name: "fish default", shell: ShellFish, want: "/home/u/.config/fish/conf.d/devsandbox-agents.fish"},
		{name: "bash default", shell: ShellBash, want: "/home/u/.config/devsandbox/agent-wrappers.bash"},
		{name: "zsh default", shell: ShellZsh, want: "/home/u/.config/devsandbox/agent-wrappers.zsh"},
		{name: "fish xdg", shell: ShellFish, xdg: "/xdg", want: "/xdg/fish/conf.d/devsandbox-agents.fish"},
		{name: "bash xdg", shell: ShellBash, xdg: "/xdg", want: "/xdg/devsandbox/agent-wrappers.bash"},
		{name: "relative xdg ignored", shell: ShellBash, xdg: "relative", want: "/home/u/.config/devsandbox/agent-wrappers.bash"},
		{name: "unsupported shell", shell: "nu", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", tt.xdg)
			if got := InstallPath(tt.shell, "/home/u"); got != tt.want {
				t.Errorf("InstallPath(%q) = %q, want %q", tt.shell, got, tt.want)
			}
		})
	}
}

func TestSourceLineIsExistenceGuarded(t *testing.T) {
	const p = "/home/u/.config/devsandbox/agent-wrappers.bash"
	for _, shell := range []string{ShellBash, ShellZsh} {
		got := SourceLine(shell, p)
		if !strings.HasPrefix(got, "[ -r '"+p+"' ]") {
			t.Errorf("%s source line is not existence-guarded: %q", shell, got)
		}
		if !strings.Contains(got, "&& . '"+p+"'") {
			t.Errorf("%s source line does not source the file: %q", shell, got)
		}
	}
	if got := SourceLine(ShellFish, p); got != "" {
		t.Errorf("fish source line = %q, want empty (conf.d is a drop-in)", got)
	}
	if got := SourceLine("nu", p); got != "" {
		t.Errorf("unsupported shell source line = %q, want empty", got)
	}
}

func TestIsSupportedShell(t *testing.T) {
	for _, s := range SupportedShells() {
		if !IsSupportedShell(s) {
			t.Errorf("IsSupportedShell(%q) = false", s)
		}
	}
	for _, s := range []string{"", "nu", "sh", "Fish"} {
		if IsSupportedShell(s) {
			t.Errorf("IsSupportedShell(%q) = true", s)
		}
	}
}

// --- behavioral tests: run the generated snippet under the real shell ---

// fakeBin builds a directory containing a fake devsandbox that echoes its argv
// one element per line, and a fake claude standing in for the real binary.
func fakeBin(t *testing.T) (dir, devsandbox string) {
	t.Helper()
	dir = t.TempDir()
	devsandbox = filepath.Join(dir, "devsandbox")
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(devsandbox, "#!/bin/sh\nfor a in \"$@\"; do printf 'arg:%s\\n' \"$a\"; done\n")
	write(filepath.Join(dir, "claude"), "#!/bin/sh\necho real-claude\n")
	return dir, devsandbox
}

// runShell sources the generated snippet and runs body under shell, failing the
// test when the shell exits non-zero.
func runShell(t *testing.T, shell, snippet, body string, env []string) string {
	t.Helper()
	out, err := runShellAllowingFailure(t, shell, snippet, body, env)
	if err != nil {
		t.Fatalf("%s failed: %v\noutput:\n%s", shell, err, out)
	}
	return out
}

// runShellAllowingFailure is runShell for the cases where a non-zero exit is
// the behavior under test.
func runShellAllowingFailure(t *testing.T, shell, snippet, body string, env []string) (string, error) {
	t.Helper()
	bin, err := exec.LookPath(shell)
	if err != nil {
		t.Skipf("%s not installed", shell)
	}

	dir := t.TempDir()
	snippetPath := filepath.Join(dir, "snippet."+shell)
	if err := os.WriteFile(snippetPath, []byte(snippet), 0o644); err != nil {
		t.Fatalf("write snippet: %v", err)
	}
	driverPath := filepath.Join(dir, "driver."+shell)
	driver := "source '" + snippetPath + "'\n" + body
	if err := os.WriteFile(driverPath, []byte(driver), 0o644); err != nil {
		t.Fatalf("write driver: %v", err)
	}

	var args []string
	switch shell {
	case ShellFish:
		args = []string{"--no-config", driverPath}
	case ShellBash:
		args = []string{"--norc", "--noprofile", driverPath}
	case ShellZsh:
		args = []string{"-f", driverPath}
	}

	cmd := exec.Command(bin, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestSnippetBehavior(t *testing.T) {
	for _, shell := range SupportedShells() {
		t.Run(shell, func(t *testing.T) {
			binDir, devsandbox := fakeBin(t)
			snippet, err := Snippet(shell, devsandbox, []string{"claude"})
			if err != nil {
				t.Fatalf("Snippet: %v", err)
			}
			baseEnv := []string{"PATH=" + binDir + ":/usr/bin:/bin", "HOME=" + t.TempDir()}

			t.Run("wrapper active when DEVSANDBOX unset", func(t *testing.T) {
				out := runShell(t, shell, snippet, "claude --resume abc\n", baseEnv)
				want := "arg:run-agent\narg:claude\narg:--resume\narg:abc\n"
				if out != want {
					t.Errorf("output = %q, want %q", out, want)
				}
			})

			t.Run("argument with spaces stays one argument", func(t *testing.T) {
				out := runShell(t, shell, snippet, "claude 'a b' 'c;d'\n", baseEnv)
				want := "arg:run-agent\narg:claude\narg:a b\narg:c;d\n"
				if out != want {
					t.Errorf("output = %q, want %q", out, want)
				}
			})

			t.Run("no wrapper when DEVSANDBOX is set", func(t *testing.T) {
				env := append(append([]string{}, baseEnv...), "DEVSANDBOX=1")
				out := runShell(t, shell, snippet, "claude\n", env)
				if out != "real-claude\n" {
					t.Errorf("output = %q, want the real binary to run", out)
				}
			})

			// An empty DEVSANDBOX means "outside the sandbox" in every shell:
			// the whole point of the non-empty guard semantics.
			t.Run("empty DEVSANDBOX behaves like unset", func(t *testing.T) {
				env := append(append([]string{}, baseEnv...), "DEVSANDBOX=")
				out := runShell(t, shell, snippet, "claude\n", env)
				want := "arg:run-agent\narg:claude\n"
				if out != want {
					t.Errorf("output = %q, want %q", out, want)
				}
			})

			t.Run("no-ds companion reaches the real binary", func(t *testing.T) {
				out := runShell(t, shell, snippet, "claude-no-ds\n", baseEnv)
				if out != "real-claude\n" {
					t.Errorf("output = %q, want the real binary to run", out)
				}
			})

			t.Run("command escape hatch reaches the real binary", func(t *testing.T) {
				out := runShell(t, shell, snippet, "command claude\n", baseEnv)
				if out != "real-claude\n" {
					t.Errorf("output = %q, want the real binary to run", out)
				}
			})
		})
	}
}

// The upgrade case, run for real: the baked path is gone while a devsandbox
// sits in PATH. The wrapper must refuse rather than execute it - PATH is the
// one input the sandbox can influence from inside, via a project-local bin
// directory - and must say what to run to fix it.
//
// The backslash case is what forces printf over echo: zsh's echo expands
// escapes by default, so `\c` in the path would swallow the rest of the
// diagnostic, including the reinstall command it exists to name.
func TestSnippetBehaviorBakedPathMissing(t *testing.T) {
	dirs := map[string]string{
		"plain":     "moved-by-upgrade",
		"backslash": `moved\cby\tupgrade`,
	}
	for _, shell := range SupportedShells() {
		t.Run(shell, func(t *testing.T) {
			for name, leaf := range dirs {
				t.Run(name, func(t *testing.T) {
					binDir, _ := fakeBin(t)
					gone := filepath.Join(t.TempDir(), leaf, "devsandbox")
					snippet, err := Snippet(shell, gone, []string{"claude"})
					if err != nil {
						t.Fatalf("Snippet: %v", err)
					}
					env := []string{"PATH=" + binDir + ":/usr/bin:/bin", "HOME=" + t.TempDir()}

					out, runErr := runShellAllowingFailure(t, shell, snippet, "claude --resume abc\n", env)
					if runErr == nil {
						t.Errorf("wrapper exited zero with the baked path gone; output:\n%s", out)
					}
					if strings.Contains(out, "arg:run-agent") {
						t.Errorf("wrapper ran the devsandbox found in PATH; output:\n%s", out)
					}
					if strings.Contains(out, "real-claude") {
						t.Errorf("wrapper fell through to the unsandboxed agent; output:\n%s", out)
					}
					if !strings.Contains(out, gone) {
						t.Errorf("error does not name the missing path; output:\n%s", out)
					}
					if !strings.Contains(out, "agent-wrappers install") {
						t.Errorf("error does not name the reinstall command; output:\n%s", out)
					}
				})
			}
		})
	}
}

// The devsandbox path is interpolated into shell source, so every character a
// shell would act on has to survive quoting. Spaces alone are not enough of a
// test: they are the one metacharacter both quoters get right by simply
// wrapping in quotes, so a quoter gutted to plain concatenation still passes a
// spaces-only suite. The single quote (which terminates the quoting) and the
// backslash (which fish, unlike POSIX shells, still processes inside single
// quotes) are what actually exercise the escaping.
func TestSnippetBehaviorPathNeedingQuoting(t *testing.T) {
	dirs := map[string]string{
		"spaces":                  "my tools",
		"single quote":            "it's tools",
		"backslash":               `back\slash`,
		"quote and backslash":     `it's a \ mess`,
		"double quote and dollar": `say "$PATH"`,
	}

	for _, shell := range SupportedShells() {
		t.Run(shell, func(t *testing.T) {
			for name, leaf := range dirs {
				t.Run(name, func(t *testing.T) {
					binDir, _ := fakeBin(t)
					dir := filepath.Join(t.TempDir(), leaf)
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("mkdir: %v", err)
					}
					devsandbox := filepath.Join(dir, "devsandbox")
					if err := os.WriteFile(devsandbox, []byte("#!/bin/sh\nfor a in \"$@\"; do printf 'arg:%s\\n' \"$a\"; done\n"), 0o755); err != nil {
						t.Fatalf("write: %v", err)
					}
					snippet, err := Snippet(shell, devsandbox, []string{"claude"})
					if err != nil {
						t.Fatalf("Snippet: %v", err)
					}
					env := []string{"PATH=" + binDir + ":/usr/bin:/bin", "HOME=" + t.TempDir()}
					out := runShell(t, shell, snippet, "claude x\n", env)
					want := "arg:run-agent\narg:claude\narg:x\n"
					if out != want {
						t.Errorf("output = %q, want %q", out, want)
					}
				})
			}
		})
	}
}

// SourceLine is pasted into an rc file, so it needs the same quoting guarantee
// as the snippet body - and it embeds the path twice.
func TestSourceLineQuotesPath(t *testing.T) {
	const p = `/opt/it's a \ dir/agent-wrappers.bash`
	for _, shell := range []string{ShellBash, ShellZsh} {
		line := SourceLine(shell, p)
		quoted := `'/opt/it'\''s a \ dir/agent-wrappers.bash'`
		if want := "[ -r " + quoted + " ] && . " + quoted; line != want {
			t.Errorf("%s SourceLine = %q, want %q", shell, line, want)
		}
	}
}
