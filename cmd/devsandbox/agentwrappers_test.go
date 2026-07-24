package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"devsandbox/internal/agentid"
	"devsandbox/internal/shellwrap"
)

func newWrapperEnv(shell string, agents ...string) (wrapperEnv, *strings.Builder) {
	out := &strings.Builder{}
	return wrapperEnv{
		shell:          shell,
		devsandboxPath: "/opt/bin/devsandbox",
		agents:         agents,
		out:            out,
	}, out
}

func TestDetectShell(t *testing.T) {
	tests := []struct {
		name      string
		arg       string
		shellEnv  string
		want      string
		wantError bool
	}{
		{name: "argument wins", arg: "fish", shellEnv: "/bin/bash", want: "fish"},
		{name: "shell env path", shellEnv: "/usr/bin/fish", want: "fish"},
		{name: "bash", shellEnv: "/bin/bash", want: "bash"},
		{name: "zsh", shellEnv: "/bin/zsh", want: "zsh"},
		{name: "login shell argv0", shellEnv: "-bash", want: "bash"},
		{name: "argument as path", arg: "/usr/local/bin/zsh", want: "zsh"},
		{name: "unsupported shell", shellEnv: "/usr/bin/nu", wantError: true},
		{name: "unsupported argument", arg: "nu", shellEnv: "/bin/bash", wantError: true},
		{name: "nothing to detect", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := detectShell(tt.arg, tt.shellEnv)
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

// The output is piped straight into `source`/`eval`, so it must be the snippet
// and nothing else - no progress line, no trailing advice.
func TestActivateWritesOnlyTheSnippet(t *testing.T) {
	for _, shell := range shellwrap.SupportedShells() {
		t.Run(shell, func(t *testing.T) {
			env, out := newWrapperEnv(shell, "claude", "codex")
			if err := activateWrappers(env); err != nil {
				t.Fatalf("activate: %v", err)
			}
			want, err := shellwrap.Snippet(shell, env.devsandboxPath, []string{"claude", "codex"})
			if err != nil {
				t.Fatalf("snippet: %v", err)
			}
			if out.String() != want {
				t.Errorf("activate output =\n%s\nwant\n%s", out, want)
			}
		})
	}
}

// A host with no agent installed is not an error: activate runs on every shell
// start, and failing there would break the startup file. The output still says
// what happened instead of being empty, and stays evaluable - every line is a
// comment.
func TestActivateWithNoAgentsEmitsComment(t *testing.T) {
	env, out := newWrapperEnv(shellwrap.ShellBash)
	if err := activateWrappers(env); err != nil {
		t.Fatalf("activate: %v", err)
	}
	got := out.String()
	for line := range strings.SplitSeq(strings.TrimRight(got, "\n"), "\n") {
		if !strings.HasPrefix(line, "#") {
			t.Errorf("line %q is not a comment; output must stay evaluable:\n%s", line, got)
		}
	}
	for _, agent := range agentid.KnownAgents() {
		if !strings.Contains(got, agent) {
			t.Errorf("output does not name %q as a supported agent:\n%s", agent, got)
		}
	}
}

// The snippet is the whole deliverable, so a short write must surface rather
// than leaving a shell with wrappers for some agents and not others.
func TestActivateReportsWriteFailure(t *testing.T) {
	env, _ := newWrapperEnv(shellwrap.ShellFish, "claude")
	env.out = failingWriter{}
	if err := activateWrappers(env); err == nil {
		t.Fatal("expected a write error to be reported")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("disk on fire")
}

func TestAgentWrappersCommandRegistersActivate(t *testing.T) {
	cmd := newAgentWrappersCmd()
	var names []string
	for _, sub := range cmd.Commands() {
		names = append(names, sub.Name())
	}
	if !reflect.DeepEqual(names, []string{"activate"}) {
		t.Errorf("agent-wrappers subcommands = %v, want [activate]", names)
	}
}

// The help has to carry the line to paste, since activate's stdout is reserved
// for shell code and can say nothing to a human.
func TestAgentWrappersHelpNamesTheActivationLine(t *testing.T) {
	long := newAgentWrappersCmd().Long
	for _, shell := range shellwrap.SupportedShells() {
		line := shellwrap.ActivateLine(shell)
		if line == "" {
			t.Fatalf("no activation line for supported shell %q", shell)
		}
		if !strings.Contains(long, line) {
			t.Errorf("help does not contain the %s activation line %q", shell, line)
		}
		if !strings.Contains(long, shellwrap.StartupFile(shell)) {
			t.Errorf("help does not name the %s startup file", shell)
		}
	}
}
