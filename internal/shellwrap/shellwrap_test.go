package shellwrap

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testDevsandbox = "/usr/local/bin/devsandbox"

const fishCleanup = `    for __devsandbox_name in $__devsandbox_wrappers
        functions -e $__devsandbox_name $__devsandbox_name-no-ds
    end
    set -e __devsandbox_name
`

const posixCleanup = `  __devsandbox_rest=${__devsandbox_wrappers-}
  unset __devsandbox_wrappers
  while [ -n "$__devsandbox_rest" ]; do __devsandbox_name=${__devsandbox_rest%% *}; __devsandbox_rest=${__devsandbox_rest#"$__devsandbox_name"}; __devsandbox_rest=${__devsandbox_rest# }; unset -f "$__devsandbox_name" "${__devsandbox_name}-no-ds" 2>/dev/null; done
  unset __devsandbox_rest __devsandbox_name
`

func TestSnippetFishExactOutput(t *testing.T) {
	got, err := Snippet(ShellFish, testDevsandbox, []string{"claude"}, nil)
	if err != nil {
		t.Fatalf("Snippet: %v", err)
	}
	want := `if test -z "$DEVSANDBOX"
` + fishCleanup + `    function claude --wraps claude
        if test -x '/usr/local/bin/devsandbox'
            '/usr/local/bin/devsandbox' run-agent claude $argv
        else
            printf '%s %s %s\n' "devsandbox: no executable at" '/usr/local/bin/devsandbox' "- reinstall devsandbox, then start a new shell to refresh the wrappers" >&2
            return 127
        end
    end
    function claude-no-ds --wraps claude
        command claude $argv
    end
    set -gu __devsandbox_wrappers claude
end
`
	if got != want {
		t.Errorf("fish snippet mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSnippetFishMixedAgentsAndCommands(t *testing.T) {
	got, err := Snippet(ShellFish, testDevsandbox, []string{"claude"}, []string{"npm", "bun"})
	if err != nil {
		t.Fatalf("Snippet: %v", err)
	}
	want := `if test -z "$DEVSANDBOX"
` + fishCleanup + `    function claude --wraps claude
        if test -x '/usr/local/bin/devsandbox'
            '/usr/local/bin/devsandbox' run-agent claude $argv
        else
            printf '%s %s %s\n' "devsandbox: no executable at" '/usr/local/bin/devsandbox' "- reinstall devsandbox, then start a new shell to refresh the wrappers" >&2
            return 127
        end
    end
    function claude-no-ds --wraps claude
        command claude $argv
    end
    function npm --wraps npm
        if test -x '/usr/local/bin/devsandbox'
            '/usr/local/bin/devsandbox' run-command npm $argv
        else
            printf '%s %s %s\n' "devsandbox: no executable at" '/usr/local/bin/devsandbox' "- reinstall devsandbox, then start a new shell to refresh the wrappers" >&2
            return 127
        end
    end
    function npm-no-ds --wraps npm
        command npm $argv
    end
    function bun --wraps bun
        if test -x '/usr/local/bin/devsandbox'
            '/usr/local/bin/devsandbox' run-command bun $argv
        else
            printf '%s %s %s\n' "devsandbox: no executable at" '/usr/local/bin/devsandbox' "- reinstall devsandbox, then start a new shell to refresh the wrappers" >&2
            return 127
        end
    end
    function bun-no-ds --wraps bun
        command bun $argv
    end
    set -gu __devsandbox_wrappers claude npm bun
end
`
	if got != want {
		t.Errorf("fish snippet mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSnippetBashMixedAgentsAndCommands(t *testing.T) {
	got, err := Snippet(ShellBash, testDevsandbox, []string{"claude"}, []string{"npm"})
	if err != nil {
		t.Fatalf("Snippet: %v", err)
	}
	want := `if [ -n "${DEVSANDBOX:-}" ]; then :; else
` + posixCleanup + `  function claude { if [ -x '/usr/local/bin/devsandbox' ]; then '/usr/local/bin/devsandbox' run-agent claude "$@"; else printf '%s %s %s\n' "devsandbox: no executable at" '/usr/local/bin/devsandbox' "- reinstall devsandbox, then start a new shell to refresh the wrappers" >&2; return 127; fi; }
  function claude-no-ds { command claude "$@"; }
  function npm { if [ -x '/usr/local/bin/devsandbox' ]; then '/usr/local/bin/devsandbox' run-command npm "$@"; else printf '%s %s %s\n' "devsandbox: no executable at" '/usr/local/bin/devsandbox' "- reinstall devsandbox, then start a new shell to refresh the wrappers" >&2; return 127; fi; }
  function npm-no-ds { command npm "$@"; }
  case $- in *a*) set +a; __devsandbox_wrappers='claude npm'; set -a ;; *) __devsandbox_wrappers='claude npm' ;; esac
fi
`
	if got != want {
		t.Errorf("bash snippet mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSnippetZshMixedAgentsAndCommands(t *testing.T) {
	got, err := Snippet(ShellZsh, testDevsandbox, []string{"claude", "codex"}, []string{"node"})
	if err != nil {
		t.Fatalf("Snippet: %v", err)
	}
	want := `if [ -n "${DEVSANDBOX:-}" ]; then :; else
` + posixCleanup + `  function claude { if [ -x '/usr/local/bin/devsandbox' ]; then '/usr/local/bin/devsandbox' run-agent claude "$@"; else printf '%s %s %s\n' "devsandbox: no executable at" '/usr/local/bin/devsandbox' "- reinstall devsandbox, then start a new shell to refresh the wrappers" >&2; return 127; fi; }
  function claude-no-ds { command claude "$@"; }
  function codex { if [ -x '/usr/local/bin/devsandbox' ]; then '/usr/local/bin/devsandbox' run-agent codex "$@"; else printf '%s %s %s\n' "devsandbox: no executable at" '/usr/local/bin/devsandbox' "- reinstall devsandbox, then start a new shell to refresh the wrappers" >&2; return 127; fi; }
  function codex-no-ds { command codex "$@"; }
  function node { if [ -x '/usr/local/bin/devsandbox' ]; then '/usr/local/bin/devsandbox' run-command node "$@"; else printf '%s %s %s\n' "devsandbox: no executable at" '/usr/local/bin/devsandbox' "- reinstall devsandbox, then start a new shell to refresh the wrappers" >&2; return 127; fi; }
  function node-no-ds { command node "$@"; }
  case $- in *a*) set +a; __devsandbox_wrappers='claude codex node'; set -a ;; *) __devsandbox_wrappers='claude codex node' ;; esac
fi
`
	if got != want {
		t.Errorf("zsh snippet mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

// An empty snapshot is not an error: it is what re-sourcing after leaving a
// project with wrapped commands produces, and its whole job is the cleanup.
func TestSnippetEmptySnapshotOnlyCleansUp(t *testing.T) {
	posixEmpty := "if [ -n \"${DEVSANDBOX:-}\" ]; then :; else\n" + posixCleanup +
		"  case $- in *a*) set +a; __devsandbox_wrappers=''; set -a ;; *) __devsandbox_wrappers='' ;; esac\nfi\n"
	tests := map[string]string{
		ShellFish: "if test -z \"$DEVSANDBOX\"\n" + fishCleanup + "    set -gu __devsandbox_wrappers\nend\n",
		ShellBash: posixEmpty,
		ShellZsh:  posixEmpty,
	}
	for shell, want := range tests {
		got, err := Snippet(shell, testDevsandbox, nil, nil)
		if err != nil {
			t.Fatalf("Snippet(%s): %v", shell, err)
		}
		if got != want {
			t.Errorf("%s snippet mismatch:\n got:\n%s\nwant:\n%s", shell, got, want)
		}
	}
}

// Agents keep going through run-agent, which applies the resume and worktree
// guards; only configured commands take run-command.
func TestSnippetRoutesAgentsAndCommandsSeparately(t *testing.T) {
	for _, shell := range SupportedShells() {
		got, err := Snippet(shell, testDevsandbox, []string{"claude"}, []string{"npm"})
		if err != nil {
			t.Fatalf("Snippet(%s): %v", shell, err)
		}
		for _, want := range []string{"run-agent claude", "run-command npm"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s snippet does not contain %q:\n%s", shell, want, got)
			}
		}
		for _, bad := range []string{"run-command claude", "run-agent npm"} {
			if strings.Contains(got, bad) {
				t.Errorf("%s snippet contains %q:\n%s", shell, bad, got)
			}
		}
	}
}

func TestSnippetDeduplicatesInFirstSeenOrder(t *testing.T) {
	got, err := Snippet(ShellBash, testDevsandbox, []string{"codex", "claude", "codex"}, []string{"npm", "bun", "npm"})
	if err != nil {
		t.Fatalf("Snippet: %v", err)
	}
	if !strings.Contains(got, "*) __devsandbox_wrappers='codex claude npm bun' ;;") {
		t.Errorf("snapshot does not record each name once in first-seen order:\n%s", got)
	}
	if n := strings.Count(got, "function npm {"); n != 1 {
		t.Errorf("npm defined %d times, want 1:\n%s", n, got)
	}
}

func TestSnippetZshSeveralAgents(t *testing.T) {
	got, err := Snippet(ShellZsh, testDevsandbox, []string{"claude", "codex"}, nil)
	if err != nil {
		t.Fatalf("Snippet: %v", err)
	}
	want := `if [ -n "${DEVSANDBOX:-}" ]; then :; else
` + posixCleanup + `  function claude { if [ -x '/usr/local/bin/devsandbox' ]; then '/usr/local/bin/devsandbox' run-agent claude "$@"; else printf '%s %s %s\n' "devsandbox: no executable at" '/usr/local/bin/devsandbox' "- reinstall devsandbox, then start a new shell to refresh the wrappers" >&2; return 127; fi; }
  function claude-no-ds { command claude "$@"; }
  function codex { if [ -x '/usr/local/bin/devsandbox' ]; then '/usr/local/bin/devsandbox' run-agent codex "$@"; else printf '%s %s %s\n' "devsandbox: no executable at" '/usr/local/bin/devsandbox' "- reinstall devsandbox, then start a new shell to refresh the wrappers" >&2; return 127; fi; }
  function codex-no-ds { command codex "$@"; }
  case $- in *a*) set +a; __devsandbox_wrappers='claude codex'; set -a ;; *) __devsandbox_wrappers='claude codex' ;; esac
fi
`
	if got != want {
		t.Errorf("zsh snippet mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSnippetFishSeveralAgents(t *testing.T) {
	got, err := Snippet(ShellFish, testDevsandbox, []string{"claude", "codex"}, nil)
	if err != nil {
		t.Fatalf("Snippet: %v", err)
	}
	want := `if test -z "$DEVSANDBOX"
` + fishCleanup + `    function claude --wraps claude
        if test -x '/usr/local/bin/devsandbox'
            '/usr/local/bin/devsandbox' run-agent claude $argv
        else
            printf '%s %s %s\n' "devsandbox: no executable at" '/usr/local/bin/devsandbox' "- reinstall devsandbox, then start a new shell to refresh the wrappers" >&2
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
            printf '%s %s %s\n' "devsandbox: no executable at" '/usr/local/bin/devsandbox' "- reinstall devsandbox, then start a new shell to refresh the wrappers" >&2
            return 127
        end
    end
    function codex-no-ds --wraps codex
        command codex $argv
    end
    set -gu __devsandbox_wrappers claude codex
end
`
	if got != want {
		t.Errorf("fish snippet mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSnippetBashExactOutput(t *testing.T) {
	got, err := Snippet(ShellBash, testDevsandbox, []string{"claude"}, nil)
	if err != nil {
		t.Fatalf("Snippet: %v", err)
	}
	want := `if [ -n "${DEVSANDBOX:-}" ]; then :; else
` + posixCleanup + `  function claude { if [ -x '/usr/local/bin/devsandbox' ]; then '/usr/local/bin/devsandbox' run-agent claude "$@"; else printf '%s %s %s\n' "devsandbox: no executable at" '/usr/local/bin/devsandbox' "- reinstall devsandbox, then start a new shell to refresh the wrappers" >&2; return 127; fi; }
  function claude-no-ds { command claude "$@"; }
  case $- in *a*) set +a; __devsandbox_wrappers='claude'; set -a ;; *) __devsandbox_wrappers='claude' ;; esac
fi
`
	if got != want {
		t.Errorf("bash snippet mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

// The guard is not decoration: the shell startup files that evaluate the
// snippet are bound into the sandbox, where an unguarded wrapper would recurse.
func TestSnippetGuardWrapsEveryDefinition(t *testing.T) {
	for _, shell := range SupportedShells() {
		t.Run(shell, func(t *testing.T) {
			got, err := Snippet(shell, testDevsandbox, []string{"claude", "codex"}, nil)
			if err != nil {
				t.Fatalf("Snippet: %v", err)
			}
			lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
			if len(lines) < 3 {
				t.Fatalf("snippet too short:\n%s", got)
			}
			guard := lines[0]
			if !strings.Contains(guard, "DEVSANDBOX") {
				t.Errorf("first line %q does not guard on DEVSANDBOX", guard)
			}
			last := lines[len(lines)-1]
			if last != "end" && last != "fi" {
				t.Errorf("last line = %q, want the guard's closing keyword", last)
			}
			for _, l := range lines[1 : len(lines)-1] {
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
	fish, err := Snippet(ShellFish, testDevsandbox, []string{"claude"}, nil)
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
		got, err := Snippet(shell, testDevsandbox, []string{"claude"}, nil)
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
		got, err := Snippet(shell, testDevsandbox, []string{"claude"}, nil)
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
		got, err := Snippet(tt.shell, testDevsandbox, []string{"claude"}, nil)
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
		if !strings.Contains(got, "start a new shell") {
			t.Errorf("%s snippet does not say how to recover:\n%s", tt.shell, got)
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
		got, err := Snippet(shell, awkward, []string{"claude"}, nil)
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
		name     string
		shell    string
		path     string
		agents   []string
		commands []string
		wantSub  string
	}{
		{"unsupported shell", "nu", testDevsandbox, []string{"claude"}, nil, "unsupported shell"},
		{"empty shell", "", testDevsandbox, []string{"claude"}, nil, "unsupported shell"},
		{"relative path", ShellBash, "devsandbox", []string{"claude"}, nil, "must be absolute"},
		{"empty path", ShellBash, "", []string{"claude"}, nil, "must be absolute"},
		{"empty agent name", ShellBash, testDevsandbox, []string{""}, nil, "empty agent name"},
		{"agent with space", ShellBash, testDevsandbox, []string{"cl aude"}, nil, "invalid agent name"},
		{"agent with metachar", ShellFish, testDevsandbox, []string{"claude;rm -rf /"}, nil, "invalid agent name"},
		{"agent leading hyphen", ShellBash, testDevsandbox, []string{"-claude"}, nil, "invalid agent name"},
		{"empty command name", ShellBash, testDevsandbox, nil, []string{""}, "empty command name"},
		{"command path", ShellFish, testDevsandbox, nil, []string{"/usr/bin/npm"}, "not a path"},
		{"command metachar", ShellBash, testDevsandbox, nil, []string{"npm;id"}, "invalid command name"},
		{"command reserved word", ShellZsh, testDevsandbox, nil, []string{"command"}, "reserved shell word"},
		{"command is an agent", ShellBash, testDevsandbox, nil, []string{"claude"}, "supported agent"},
		{"command beside its own bypass", ShellBash, testDevsandbox, nil, []string{"npm", "npm-no-ds"}, `"npm-no-ds"`},
		{"agent beside its own bypass", ShellFish, testDevsandbox, []string{"claude", "claude-no-ds"}, nil, `wrapper function "claude-no-ds" is generated for both run-agent "claude" and run-agent "claude-no-ds"`},
		{"agent named after a command bypass", ShellBash, testDevsandbox, []string{"npm-no-ds"}, []string{"npm"}, `wrapper function "npm-no-ds" is generated for both run-agent "npm-no-ds" and run-command "npm"`},
		{"agent and command with one name", ShellBash, testDevsandbox, []string{"tool"}, []string{"tool"}, `wrapper function "tool" is generated for both run-agent "tool" and run-command "tool"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Snippet(tt.shell, tt.path, tt.agents, tt.commands)
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
		got, err := Snippet(shell, p, []string{"claude"}, nil)
		if err != nil {
			t.Fatalf("Snippet(%s): %v", shell, err)
		}
		if !strings.Contains(got, "'"+p+"'") {
			t.Errorf("%s snippet does not quote a path with spaces:\n%s", shell, got)
		}
	}
}

func TestActivateLine(t *testing.T) {
	tests := map[string]string{
		ShellFish: `if test -z "$DEVSANDBOX"; devsandbox shell-wrappers activate fish | source; end`,
		ShellBash: `if [ -z "${DEVSANDBOX:-}" ]; then eval "$(devsandbox shell-wrappers activate bash)"; fi`,
		ShellZsh:  `if [ -z "${DEVSANDBOX:-}" ]; then eval "$(devsandbox shell-wrappers activate zsh)"; fi`,
	}
	for shell, want := range tests {
		if got := ActivateLine(shell); got != want {
			t.Errorf("ActivateLine(%q) = %q, want %q", shell, got, want)
		}
	}
	if got := ActivateLine("nu"); got != "" {
		t.Errorf("unsupported shell activation line = %q, want empty", got)
	}
	for _, shell := range SupportedShells() {
		if ActivateLine(shell) == "" {
			t.Errorf("supported shell %q has no activation line", shell)
		}
		if StartupFile(shell) == "" {
			t.Errorf("supported shell %q has no startup file", shell)
		}
	}
}

// The startup files are bound into the sandbox while devsandbox itself need not
// exist in there, so the line has to be guarded before it invokes anything -
// the snippet's own guard is too late to prevent a command-not-found per shell
// start. Empty and unset must mean the same thing, as everywhere else.
func TestActivateLineIsGuardedOnDevsandbox(t *testing.T) {
	for _, shell := range SupportedShells() {
		line := ActivateLine(shell)
		guard, rest, ok := strings.Cut(line, ActivateCommand)
		if !ok {
			t.Fatalf("%s activation line does not run %q: %q", shell, ActivateCommand, line)
		}
		if !strings.Contains(guard, "DEVSANDBOX") {
			t.Errorf("%s activation line invokes devsandbox unguarded: %q", shell, line)
		}
		if strings.Contains(guard, "set -q") {
			t.Errorf("%s activation guard uses set -q, which is true for an empty value: %q", shell, line)
		}
		if !strings.Contains(rest, shell) {
			t.Errorf("%s activation line does not pass the shell name: %q", shell, line)
		}
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
	write(filepath.Join(dir, "npm"), "#!/bin/sh\necho real-npm\n")
	return dir, devsandbox
}

// writeSnippet stores snippet in a file a driver script can source.
func writeSnippet(t *testing.T, shell, snippet string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snippet."+shell)
	if err := os.WriteFile(path, []byte(snippet), 0o644); err != nil {
		t.Fatalf("write snippet: %v", err)
	}
	return path
}

// functionProbe is a statement printing "fn:<name>" when name is defined as a
// shell function.
func functionProbe(shell, name string) string {
	if shell == ShellFish {
		return "functions -q " + name + "; and echo fn:" + name + "\n"
	}
	return "typeset -f " + name + " >/dev/null 2>&1 && echo fn:" + name + "\n"
}

// userFunction defines a function devsandbox did not generate.
func userFunction(shell, name string) string {
	if shell == ShellFish {
		return "function " + name + "; echo user; end\n"
	}
	return "function " + name + " { echo user; }\n"
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
	return runShellBody(t, shell, "source '"+writeSnippet(t, shell, snippet)+"'\n"+body, env)
}

// runShellBody runs script under shell, returning its combined output and exit
// error.
func runShellBody(t *testing.T, shell, script string, env []string) (string, error) {
	t.Helper()
	bin, err := exec.LookPath(shell)
	if err != nil {
		t.Skipf("%s not installed", shell)
	}

	driverPath := filepath.Join(t.TempDir(), "driver."+shell)
	if err := os.WriteFile(driverPath, []byte(script), 0o644); err != nil {
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
			snippet, err := Snippet(shell, devsandbox, []string{"claude"}, nil)
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

// A configured command is wrapped whether or not the host has it: it is
// resolved inside the sandbox, which is the point of wrapping package tooling
// the host never needs to install.
func TestSnippetBehaviorCustomCommand(t *testing.T) {
	for _, shell := range SupportedShells() {
		t.Run(shell, func(t *testing.T) {
			binDir, devsandbox := fakeBin(t)
			snippet, err := Snippet(shell, devsandbox, nil, []string{"npm", "node"})
			if err != nil {
				t.Fatalf("Snippet: %v", err)
			}
			baseEnv := []string{"PATH=" + binDir + ":/usr/bin:/bin", "HOME=" + t.TempDir()}

			tests := []struct {
				name string
				body string
				env  []string
				want string
			}{
				{"routes through run-command with arguments intact", "npm install 'a b' --proxy 'c;d'\n", baseEnv,
					"arg:run-command\narg:npm\narg:install\narg:a b\narg:--proxy\narg:c;d\n"},
				{"host-missing command still routes", "node x\n", baseEnv, "arg:run-command\narg:node\narg:x\n"},
				{"no-ds companion reaches the host binary", "npm-no-ds\n", baseEnv, "real-npm\n"},
				{"command escape hatch reaches the host binary", "command npm\n", baseEnv, "real-npm\n"},
				{"inert inside the sandbox", "npm\n", append(append([]string{}, baseEnv...), "DEVSANDBOX=1"), "real-npm\n"},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					if out := runShell(t, shell, snippet, tt.body, tt.env); out != tt.want {
						t.Errorf("output = %q, want %q", out, tt.want)
					}
				})
			}
		})
	}
}

// Re-sourcing activation is how a shell picks up a different project's
// wrappers, so a snapshot has to take down the one before it: a wrapper left
// behind would keep sandboxing a command the current config no longer names,
// and a bypass left behind would keep pointing at it. Only recorded names may
// go, and the record must not reach a child process - a child shell has none of
// the functions, so an inherited list would only name its own.
func TestSnippetBehaviorSnapshotsReconcile(t *testing.T) {
	names := []string{"claude", "claude-no-ds", "npm", "npm-no-ds", "bun", "bun-no-ds", "node", "node-no-ds", "userfn"}

	for _, shell := range SupportedShells() {
		t.Run(shell, func(t *testing.T) {
			binDir, devsandbox := fakeBin(t)
			snapshot := func(agents, commands []string) string {
				s, err := Snippet(shell, devsandbox, agents, commands)
				if err != nil {
					t.Fatalf("Snippet: %v", err)
				}
				return writeSnippet(t, shell, s)
			}
			first := snapshot([]string{"claude"}, []string{"npm", "bun"})
			// An inherited, exported record is what a leak would look like;
			// activation has to leave it unexported either way.
			env := []string{"PATH=" + binDir + ":/usr/bin:/bin", "HOME=" + t.TempDir(), "__devsandbox_wrappers=stale"}

			run := func(t *testing.T, second, calls string) string {
				t.Helper()
				script := userFunction(shell, "userfn") +
					"source '" + first + "'\n" +
					"source '" + second + "'\n"
				for _, n := range names {
					script += functionProbe(shell, n)
				}
				script += calls + "env\ntrue\n"
				out, err := runShellBody(t, shell, script, env)
				if err != nil {
					t.Fatalf("%s failed: %v\noutput:\n%s", shell, err, out)
				}
				if strings.Contains(out, "__devsandbox_wrappers=") {
					t.Errorf("snapshot record is exported to a child process:\n%s", out)
				}
				return out
			}

			t.Run("second snapshot replaces the first", func(t *testing.T) {
				out := run(t, snapshot(nil, []string{"node"}), "node x 'a b'\nnpm\n")
				want := "fn:node\nfn:node-no-ds\nfn:userfn\narg:run-command\narg:node\narg:x\narg:a b\nreal-npm\n"
				if !strings.HasPrefix(out, want) {
					t.Errorf("output = %q, want prefix %q", out, want)
				}
			})

			t.Run("same snapshot twice keeps every wrapper", func(t *testing.T) {
				out := run(t, first, "npm i\n")
				want := "fn:claude\nfn:claude-no-ds\nfn:npm\nfn:npm-no-ds\nfn:bun\nfn:bun-no-ds\nfn:userfn\narg:run-command\narg:npm\narg:i\n"
				if !strings.HasPrefix(out, want) {
					t.Errorf("output = %q, want prefix %q", out, want)
				}
			})

			t.Run("empty snapshot removes every wrapper", func(t *testing.T) {
				out := run(t, snapshot(nil, nil), "claude\n")
				want := "fn:userfn\nreal-claude\n"
				if !strings.HasPrefix(out, want) {
					t.Errorf("output = %q, want prefix %q", out, want)
				}
			})

			// allexport exports every assignment, the record's included.
			if shell != ShellFish {
				t.Run("record stays unexported under allexport", func(t *testing.T) {
					out := run(t, first, "set -a\nsource '"+first+"'\ncase $- in *a*) echo allexport-on ;; esac\n")
					if !strings.Contains(out, "allexport-on\n") {
						t.Errorf("allexport was not restored after activation:\n%s", out)
					}
				})
			}
		})
	}
}

// Users alias package tooling (alias npm=pnpm). bash and zsh alias-expand the
// name in `name() {`, which would turn the whole snippet into a syntax error and
// leave the shell with no wrappers at all.
func TestSnippetBehaviorSurvivesAliasOnWrappedName(t *testing.T) {
	aliases := map[string]string{
		ShellFish: "alias npm 'echo aliased'\n",
		ShellBash: "shopt -s expand_aliases\nalias npm='echo aliased'\n",
		ShellZsh:  "alias npm='echo aliased'\n",
	}
	for _, shell := range SupportedShells() {
		t.Run(shell, func(t *testing.T) {
			binDir, devsandbox := fakeBin(t)
			snippet, err := Snippet(shell, devsandbox, []string{"claude"}, []string{"npm"})
			if err != nil {
				t.Fatalf("Snippet: %v", err)
			}
			env := []string{"PATH=" + binDir + ":/usr/bin:/bin", "HOME=" + t.TempDir()}
			script := aliases[shell] + "source '" + writeSnippet(t, shell, snippet) + "'\nclaude x\nnpm-no-ds\n"
			out, err := runShellBody(t, shell, script, env)
			if err != nil {
				t.Fatalf("%s failed: %v\noutput:\n%s", shell, err, out)
			}
			if want := "arg:run-agent\narg:claude\narg:x\nreal-npm\n"; out != want {
				t.Errorf("output = %q, want %q", out, want)
			}
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
					snippet, err := Snippet(shell, gone, []string{"claude"}, nil)
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
					if !strings.Contains(out, "start a new shell") {
						t.Errorf("error does not say how to recover; output:\n%s", out)
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
					snippet, err := Snippet(shell, devsandbox, []string{"claude"}, nil)
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

// The activation line is what the user pastes into a startup file, so its
// evaluation form has to be valid in the shell it names - fish takes a pipe into
// `source`, the POSIX shells an `eval` of a command substitution. The guard is
// checked the same way: inside the sandbox devsandbox must not be invoked at
// all, because it need not exist in there.
func TestActivateLineBehavior(t *testing.T) {
	for _, shell := range SupportedShells() {
		t.Run(shell, func(t *testing.T) {
			binDir := t.TempDir()
			marker := filepath.Join(t.TempDir(), "invoked")
			fake := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + marker + "'\necho 'echo activated'\n"
			if err := os.WriteFile(filepath.Join(binDir, "devsandbox"), []byte(fake), 0o755); err != nil {
				t.Fatalf("write fake devsandbox: %v", err)
			}
			baseEnv := []string{"PATH=" + binDir + ":/usr/bin:/bin", "HOME=" + t.TempDir()}
			line := ActivateLine(shell) + "\n"

			out, err := runShellBody(t, shell, line, baseEnv)
			if err != nil {
				t.Fatalf("%s failed: %v\noutput:\n%s", shell, err, out)
			}
			if out != "activated\n" {
				t.Errorf("output = %q, want the emitted snippet to be evaluated", out)
			}
			args, err := os.ReadFile(marker)
			if err != nil {
				t.Fatalf("read marker: %v", err)
			}
			if want := "shell-wrappers activate " + shell + "\n"; string(args) != want {
				t.Errorf("devsandbox invoked as %q, want %q", args, want)
			}

			if err := os.Remove(marker); err != nil {
				t.Fatalf("reset marker: %v", err)
			}
			sandboxed := append(append([]string{}, baseEnv...), "DEVSANDBOX=1")
			out, err = runShellBody(t, shell, line, sandboxed)
			if err != nil {
				t.Fatalf("%s failed inside the sandbox: %v\noutput:\n%s", shell, err, out)
			}
			if out != "" {
				t.Errorf("output = %q, want nothing inside the sandbox", out)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Errorf("devsandbox was invoked inside the sandbox")
			}
		})
	}
}
