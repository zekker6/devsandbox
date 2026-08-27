package main

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"devsandbox/internal/agentid"
	"devsandbox/internal/shellwrap"
)

func newWrapperEnv(shell string, agents ...string) (wrapperEnv, *strings.Builder) {
	return newSelectedWrapperEnv(shell, agentid.KnownAgents(), agents...)
}

// newSelectedWrapperEnv is the same, for a --agents selection narrower than
// every known agent.
func newSelectedWrapperEnv(shell string, selected []string, agents ...string) (wrapperEnv, *strings.Builder) {
	out := &strings.Builder{}
	return wrapperEnv{
		shell:          shell,
		devsandboxPath: "/opt/bin/devsandbox",
		selected:       selected,
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
	if named := agentsNamedInComment(t, got); !reflect.DeepEqual(named, agentid.KnownAgents()) {
		t.Errorf("comment names %#v, want every supported agent %#v", named, agentid.KnownAgents())
	}
}

// agentsNamedInComment returns the agents the no-wrapper comment lists. Names
// are compared as a parsed list rather than with strings.Contains, because
// "copilot" contains "pi" - a substring check passes for an agent the comment
// never named.
func agentsNamedInComment(t *testing.T, comment string) []string {
	t.Helper()
	_, rest, ok := strings.Cut(comment, "(")
	if !ok {
		t.Fatalf("comment names no agent list: %q", comment)
	}
	list, _, ok := strings.Cut(rest, ")")
	if !ok {
		t.Fatalf("comment agent list is unterminated: %q", comment)
	}
	return strings.Split(list, ", ")
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

// An omitted flag has to keep selecting everything, and an explicit list is
// canonically ordered and deduplicated so the snippet a startup file evaluates
// does not depend on how the values were typed.
func TestResolveAgentSelection(t *testing.T) {
	known := agentid.KnownAgents()
	tests := []struct {
		name      string
		requested []string
		explicit  bool
		want      []string
	}{
		{name: "omitted selects every known agent", want: known},
		{name: "omitted ignores stray values", requested: []string{"claude"}, want: known},
		{name: "single agent", requested: []string{"codex"}, explicit: true, want: []string{"codex"}},
		{
			name:      "subset in canonical order",
			requested: []string{"claude", "codex"},
			explicit:  true,
			want:      []string{"claude", "codex"},
		},
		{
			name:      "reordered values keep canonical order",
			requested: []string{"codex", "claude"},
			explicit:  true,
			want:      []string{"claude", "codex"},
		},
		{
			name:      "duplicates collapse to one",
			requested: []string{"claude", "claude", "codex", "claude"},
			explicit:  true,
			want:      []string{"claude", "codex"},
		},
		{
			name:      "surrounding whitespace is tolerated",
			requested: []string{" claude ", "codex\t"},
			explicit:  true,
			want:      []string{"claude", "codex"},
		},
		{name: "every known agent", requested: known, explicit: true, want: known},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveAgentSelection(tt.requested, tt.explicit)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolveAgentSelection = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// An explicit selection that resolves to nothing, or names an agent this build
// cannot wrap, is a mistake worth stopping on - and the error has to carry the
// list of names that would have worked, since the user is reading it from a
// failed shell start.
func TestResolveAgentSelectionRejectsUnusableSelections(t *testing.T) {
	tests := []struct {
		name      string
		requested []string
		// wantPrefix is asserted whole rather than name by name: it is what
		// pins a repeated bad value being reported once, and the noun agreeing
		// with how many there are.
		wantPrefix string
	}{
		{name: "explicitly empty", requested: nil, wantPrefix: "no agents selected"},
		{name: "empty string value", requested: []string{""}, wantPrefix: "no agents selected"},
		{name: "whitespace only value", requested: []string{"  "}, wantPrefix: "no agents selected"},
		{name: "one unsupported", requested: []string{"gemini"}, wantPrefix: `unsupported agent "gemini":`},
		{
			name:       "unsupported alongside supported",
			requested:  []string{"claude", "gemini"},
			wantPrefix: `unsupported agent "gemini":`,
		},
		{
			name:       "every unsupported value is reported once, in the order typed",
			requested:  []string{"gemini", "aider", "gemini"},
			wantPrefix: `unsupported agents "gemini", "aider":`,
		},
		{
			name:       "case is not folded",
			requested:  []string{"Claude"},
			wantPrefix: `unsupported agent "Claude":`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveAgentSelection(tt.requested, true)
			if err == nil {
				t.Fatalf("expected an error, got %#v", got)
			}
			if got != nil {
				t.Errorf("expected no selection alongside the error, got %#v", got)
			}
			if !strings.HasPrefix(err.Error(), tt.wantPrefix) {
				t.Errorf("error %q does not start with %q", err, tt.wantPrefix)
			}
			for _, agent := range agentid.KnownAgents() {
				if !strings.Contains(err.Error(), agent) {
					t.Errorf("error %q does not list the supported agent %q", err, agent)
				}
			}
		})
	}
}

// runActivate drives the real command the way a startup file does, and returns
// what a shell would have evaluated.
func runActivate(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newAgentWrappersActivateCmd()
	out := &strings.Builder{}
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// parseAgentSelection resolves --agents exactly as the command does, so the
// flag's own parsing is under test and not only the helper behind it.
func parseAgentSelection(t *testing.T, args ...string) ([]string, error) {
	t.Helper()
	cmd := newAgentWrappersActivateCmd()
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	requested, err := cmd.Flags().GetStringSlice("agents")
	if err != nil {
		t.Fatalf("read --agents: %v", err)
	}
	return resolveAgentSelection(requested, cmd.Flags().Changed("agents"))
}

// The two list syntaxes are the same request. They are worth pinning because
// cobra's StringSlice, not devsandbox, is what splits on the comma.
func TestActivateAgentsFlagFormsAreEquivalent(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "omitted", args: []string{}, want: agentid.KnownAgents()},
		{name: "comma separated", args: []string{"--agents", "claude,codex"}, want: []string{"claude", "codex"}},
		{
			// pflag splits but does not trim, so this arrives as [claude, " codex"].
			name: "comma separated with spaces",
			args: []string{"--agents", "claude, codex"},
			want: []string{"claude", "codex"},
		},
		{
			name: "repeated",
			args: []string{"--agents", "claude", "--agents", "codex"},
			want: []string{"claude", "codex"},
		},
		{
			name: "repeated and comma separated",
			args: []string{"--agents", "claude,pi", "--agents", "codex"},
			want: []string{"claude", "pi", "codex"},
		},
		{
			name: "reordered",
			args: []string{"--agents", "codex", "--agents", "claude"},
			want: []string{"claude", "codex"},
		},
		{
			name: "duplicated across forms",
			args: []string{"--agents", "codex,claude", "--agents", "codex"},
			want: []string{"claude", "codex"},
		},
		{name: "equals form", args: []string{"--agents=claude,codex"}, want: []string{"claude", "codex"}},
		{name: "single agent", args: []string{"--agents", "codex"}, want: []string{"codex"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAgentSelection(t, tt.args...)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("selection = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// An empty --agents is an explicit request to wrap nothing, which is a mistake
// rather than a silently wrapper-less shell.
func TestActivateAgentsFlagRejectsEmptySelection(t *testing.T) {
	for _, args := range [][]string{{"--agents", ""}, {"--agents="}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			got, err := parseAgentSelection(t, args...)
			if err == nil {
				t.Fatalf("expected an error, got %#v", got)
			}
			if !strings.Contains(err.Error(), "no agents selected") {
				t.Errorf("error %q does not explain the empty selection", err)
			}
		})
	}
}

// Only what was asked for is looked up, so an agent installed on this host but
// left out of --agents is not wrapped.
func TestInstalledAgentsHonoursTheSelection(t *testing.T) {
	look := fakeLookPath(map[string]string{"claude": "/usr/bin/claude", "codex": "/usr/bin/codex"})

	selected, err := resolveAgentSelection([]string{"codex"}, true)
	if err != nil {
		t.Fatalf("resolve selection: %v", err)
	}
	if got := installedAgents(selected, look); !reflect.DeepEqual(got, []string{"codex"}) {
		t.Errorf("installedAgents = %#v, want [codex]", got)
	}

	all, err := resolveAgentSelection(nil, false)
	if err != nil {
		t.Fatalf("resolve default selection: %v", err)
	}
	if got := installedAgents(all, look); !reflect.DeepEqual(got, []string{"claude", "codex"}) {
		t.Errorf("installedAgents = %#v, want [claude codex]", got)
	}
}

// The no-wrapper comment reports on the selection, not on every agent this
// build knows: with --agents codex it must not claim claude is missing, and
// with the flag omitted it still names them all.
func TestActivateNoAgentCommentNamesOnlyTheSelection(t *testing.T) {
	env, out := newSelectedWrapperEnv(shellwrap.ShellBash, []string{"codex"})
	if err := activateWrappers(env); err != nil {
		t.Fatalf("activate: %v", err)
	}
	got := out.String()
	// Asserted whole: it pins the unselected agents being absent without a
	// strings.Contains check that "copilot" would satisfy for "pi", that the
	// line stays a comment, and that a narrowed selection is not called the
	// supported set - which would claim devsandbox supports codex alone.
	want := "# none of the selected agents (codex) are installed on this host; nothing wrapped\n"
	if got != want {
		t.Errorf("activate output = %q, want %q", got, want)
	}
}

// Wrappers are still generated for the selected agents that are installed.
func TestActivateWrapsOnlySelectedInstalledAgents(t *testing.T) {
	env, out := newSelectedWrapperEnv(shellwrap.ShellFish, []string{"claude", "codex"}, "codex")
	if err := activateWrappers(env); err != nil {
		t.Fatalf("activate: %v", err)
	}
	want, err := shellwrap.Snippet(shellwrap.ShellFish, env.devsandboxPath, []string{"codex"})
	if err != nil {
		t.Fatalf("snippet: %v", err)
	}
	if out.String() != want {
		t.Errorf("activate output =\n%s\nwant\n%s", out, want)
	}
}

// Omitting the flag has to stay byte-identical to naming every agent, whatever
// this host happens to have installed - that is the whole backward-compatibility
// claim.
func TestActivateWithoutFlagMatchesSelectingEveryAgent(t *testing.T) {
	omitted, err := runActivate(t, shellwrap.ShellBash)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	explicit, err := runActivate(t, shellwrap.ShellBash, "--agents", strings.Join(agentid.KnownAgents(), ","))
	if err != nil {
		t.Fatalf("activate --agents: %v", err)
	}
	if omitted != explicit {
		t.Errorf("omitted --agents produced\n%s\nbut selecting every agent produced\n%s", omitted, explicit)
	}
	if omitted == "" {
		t.Error("activate wrote nothing")
	}
}

// The selection is what installation is checked against, so what a real run
// wraps can only ever be a subset of what was asked for.
func TestResolveWrapperEnvFiltersWithinTheSelection(t *testing.T) {
	// The lookPath is faked rather than taken from this host: on a runner with
	// no agent installed every real lookup fails, so an assertion phrased as
	// "nothing outside the selection turned up" holds however wide the lookup
	// was, and the selection having no effect at all would pass.
	installed := fakeLookPath(map[string]string{
		"claude": "/usr/bin/claude",
		"pi":     "/usr/bin/pi",
		"codex":  "/usr/bin/codex",
	})
	selection := []string{"claude", "codex"}

	env, err := resolveWrapperEnv(shellwrap.ShellBash, selection, installed, io.Discard)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !reflect.DeepEqual(env.selected, selection) {
		t.Errorf("env.selected = %#v, want %#v", env.selected, selection)
	}
	// pi is installed and deliberately left out, so it must not be wrapped.
	if !reflect.DeepEqual(env.agents, []string{"claude", "codex"}) {
		t.Errorf("env.agents = %#v, want [claude codex]", env.agents)
	}

	// A selected agent that is merely absent narrows the result further.
	env, err = resolveWrapperEnv(shellwrap.ShellBash, selection,
		fakeLookPath(map[string]string{"claude": "/usr/bin/claude"}), io.Discard)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !reflect.DeepEqual(env.agents, []string{"claude"}) {
		t.Errorf("env.agents = %#v, want [claude]", env.agents)
	}
	if !reflect.DeepEqual(env.selected, selection) {
		t.Errorf("env.selected = %#v, want %#v", env.selected, selection)
	}
}

// A rejected selection must fail before anything reaches stdout: a startup file
// evaluating a half-written snippet would define wrappers for some agents and
// not others.
func TestActivateRejectsBadSelectionBeforeWritingAnything(t *testing.T) {
	for _, args := range [][]string{
		{shellwrap.ShellBash, "--agents", "gemini"},
		{shellwrap.ShellBash, "--agents", "claude,gemini"},
		{shellwrap.ShellBash, "--agents", ""},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			got, err := runActivate(t, args...)
			if err == nil {
				t.Fatalf("expected an error, got output:\n%s", got)
			}
			if got != "" {
				t.Errorf("stdout must stay empty on a rejected selection, got:\n%s", got)
			}
		})
	}
}

// An unsupported shell must still be reported when the selection is fine, and
// an unsupported agent when the shell is fine - neither check may shadow the
// other. The message is what is asserted: any error at all passes even when one
// check has swallowed the other, which is the regression this names.
func TestActivateReportsShellAndAgentErrorsIndependently(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "valid selection does not mask a bad shell",
			args: []string{"nu", "--agents", "claude"},
			want: "unsupported shell",
		},
		{
			name: "valid shell does not mask a bad selection",
			args: []string{shellwrap.ShellBash, "--agents", "gemini"},
			want: "unsupported agent",
		},
		{
			// The selection is resolved first, so it is what a user is told to
			// fix when both are wrong.
			name: "both wrong reports the selection",
			args: []string{"nu", "--agents", "gemini"},
			want: "unsupported agent",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := runActivate(t, tt.args...)
			if err == nil {
				t.Fatalf("expected an error, got output:\n%s", got)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not report %q", err, tt.want)
			}
		})
	}
}

// activate's stdout is shell code and can say nothing to a human, so the flag
// has to be explained in the help.
func TestActivateHelpDocumentsTheAgentsFlag(t *testing.T) {
	cmd := newAgentWrappersActivateCmd()

	flag := cmd.Flags().Lookup("agents")
	if flag == nil {
		t.Fatal("activate has no --agents flag")
	}
	if flag.Value.Type() != "stringSlice" {
		t.Errorf("--agents is a %s, want stringSlice", flag.Value.Type())
	}
	for _, agent := range agentid.KnownAgents() {
		if !strings.Contains(flag.Usage, agent) {
			t.Errorf("--agents usage %q does not list the supported agent %q", flag.Usage, agent)
		}
	}

	help := cmd.Long + "\n" + cmd.Example
	for _, want := range []string{
		"--agents",
		"--agents claude,codex",
		"--agents claude --agents codex",
		"installed",
		"unsandboxed",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("activate help does not mention %q", want)
		}
	}
	for _, agent := range agentid.KnownAgents() {
		if !strings.Contains(cmd.Long, agent) {
			t.Errorf("activate help does not name the supported agent %q", agent)
		}
	}
}
