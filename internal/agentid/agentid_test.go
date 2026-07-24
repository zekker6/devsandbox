package agentid

import (
	"slices"
	"testing"
)

func TestKnownAgents(t *testing.T) {
	got := KnownAgents()
	want := []string{"claude", "pi", "codex", "opencode", "copilot"}
	if !slices.Equal(got, want) {
		t.Fatalf("KnownAgents() = %v, want %v", got, want)
	}

	// The returned slice must not alias package state.
	got[0] = "mutated"
	if KnownAgents()[0] != "claude" {
		t.Fatal("KnownAgents() returned a slice aliasing package state")
	}
}

func TestCanonicalAgent(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string
	}{
		{"bare name", []string{"claude"}, "claude"},
		{"absolute path", []string{"/usr/local/bin/claude"}, "claude"},
		{"relative path", []string{"./bin/claude"}, "claude"},
		{"with args", []string{"claude", "--resume", "abc"}, "claude"},
		{"empty argv", nil, ""},
		{"empty argv0", []string{""}, ""},
		{"unknown binary", []string{"bash"}, ""},
		{"agent in later arg", []string{"sh", "-c", "claude --resume x"}, ""},
		{"substring prefix", []string{"claude-wrapper"}, ""},
		{"substring suffix", []string{"my-claude"}, ""},
		{"no-ds companion", []string{"claude-no-ds"}, ""},
		{"pi bare name", []string{"pi"}, "pi"},
		{"pi absolute path", []string{"/home/user/.local/bin/pi", "--session", "abc"}, "pi"},
		{"pi substring prefix", []string{"pipenv"}, ""},
		{"pi no-ds companion", []string{"pi-no-ds"}, ""},
		{"codex bare name", []string{"codex"}, "codex"},
		{"codex absolute path", []string{"/usr/local/bin/codex", "resume", "abc"}, "codex"},
		{"codex substring prefix", []string{"codexctl"}, ""},
		{"codex no-ds companion", []string{"codex-no-ds"}, ""},
		{"opencode bare name", []string{"opencode"}, "opencode"},
		{"opencode absolute path", []string{"/usr/local/bin/opencode", "--continue"}, "opencode"},
		{"opencode substring prefix", []string{"opencoder"}, ""},
		{"opencode no-ds companion", []string{"opencode-no-ds"}, ""},
		{"copilot bare name", []string{"copilot"}, "copilot"},
		{"copilot absolute path", []string{"/usr/local/bin/copilot", "--resume"}, "copilot"},
		{"copilot no-ds companion", []string{"copilot-no-ds"}, ""},
		{"directory-like argv0", []string{"/usr/local/bin/"}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanonicalAgent(tt.argv); got != tt.want {
				t.Fatalf("CanonicalAgent(%q) = %q, want %q", tt.argv, got, tt.want)
			}
		})
	}
}

func TestIsResumeInvocation(t *testing.T) {
	tests := []struct {
		name  string
		agent string
		args  []string
		want  bool
	}{
		{"separate value", "claude", []string{"--resume", "abc-123"}, true},
		{"equals form", "claude", []string{"--resume=abc-123"}, true},
		{"flag alone", "claude", []string{"--resume"}, true},
		{"after another flag", "claude", []string{"--print", "--resume", "abc"}, true},
		{"no args", "claude", nil, false},
		{"unrelated flag", "claude", []string{"--print"}, false},
		{"after double dash", "claude", []string{"--", "--resume", "abc"}, false},
		{"prefix without equals", "claude", []string{"--resumed"}, false},
		{"positional value only", "claude", []string{"abc-123"}, false},
		{"other agent flag", "claude", []string{"--session", "abc"}, false},
		{"pi separate value", "pi", []string{"--session", "abc-123"}, true},
		{"pi equals form", "pi", []string{"--session=abc-123"}, true},
		{"pi flag alone", "pi", []string{"--session"}, true},
		{"pi after double dash", "pi", []string{"--", "--session", "abc"}, false},
		{"pi with claude's flag", "pi", []string{"--resume", "abc"}, false},
		{"pi no args", "pi", nil, false},
		{"codex subcommand with id", "codex", []string{"resume", "abc-123"}, true},
		{"codex subcommand alone", "codex", []string{"resume"}, true},
		{"codex subcommand with --last", "codex", []string{"resume", "--last"}, true},
		{"codex subcommand not first", "codex", []string{"-c", "model=o3", "resume", "abc"}, false},
		{"codex subcommand as a value", "codex", []string{"--output-last-message", "resume"}, false},
		{"codex other subcommand", "codex", []string{"exec", "hi"}, false},
		{"codex with claude's flag", "codex", []string{"--resume", "abc"}, false},
		{"codex no args", "codex", nil, false},
		{"codex bare prompt", "codex", []string{"fix the build"}, false},
		{"opencode continue", "opencode", []string{"--continue"}, true},
		{"opencode short continue", "opencode", []string{"-c"}, true},
		{"opencode session value", "opencode", []string{"--session", "abc"}, true},
		{"opencode short session", "opencode", []string{"-s", "abc"}, true},
		{"opencode after double dash", "opencode", []string{"--", "--continue"}, false},
		{"opencode bare prompt", "opencode", []string{"fix the build"}, false},
		{"copilot resume", "copilot", []string{"--resume"}, true},
		{"copilot short resume", "copilot", []string{"-r", "abc"}, true},
		{"copilot resume equals", "copilot", []string{"--resume=abc"}, true},
		{"copilot continue", "copilot", []string{"--continue"}, true},
		{"copilot session-id not a resume verb", "copilot", []string{"--session-id", "abc"}, false},
		{"copilot no args", "copilot", nil, false},
		{"unknown agent", "aider", []string{"--session", "abc"}, false},
		{"empty agent", "", []string{"--resume", "abc"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsResumeInvocation(tt.agent, tt.args); got != tt.want {
				t.Fatalf("IsResumeInvocation(%q, %q) = %v, want %v", tt.agent, tt.args, got, tt.want)
			}
		})
	}
}

// herdr only ever types --resume, but the wrapper intercepts everything the
// user types and -c/--continue reaches the same per-project session store. A
// guard blind to it lets a --worktree pane silently open a fresh session.
func TestIsResumeInvocationRecognizesClaudeContinue(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"long continue", []string{"--continue"}, true},
		{"short continue", []string{"-c"}, true},
		{"continue with later flags", []string{"--continue", "--fork-session"}, true},
		{"continue after a passthrough separator", []string{"--", "--continue"}, false},
		{"prompt mentioning continue", []string{"-p", "continue"}, false},
		{"unrelated short flag", []string{"-p"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsResumeInvocation("claude", tt.args); got != tt.want {
				t.Errorf("IsResumeInvocation(claude, %q) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

// The aliases are claude's own flags: pi and codex must not inherit them, or a
// plain `pi -c` would be refused in a worktree pane for no reason.
func TestIsResumeInvocationDoesNotShareAliasesAcrossAgents(t *testing.T) {
	for _, agent := range []string{"pi", "codex"} {
		for _, arg := range []string{"--continue", "-c"} {
			if IsResumeInvocation(agent, []string{arg}) {
				t.Errorf("IsResumeInvocation(%s, [%q]) = true, want false", agent, arg)
			}
		}
	}
}
