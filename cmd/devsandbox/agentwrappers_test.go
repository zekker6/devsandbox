package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/spf13/cobra"

	"devsandbox/internal/agentid"
	"devsandbox/internal/config"
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
// and nothing else - no progress line, no trailing advice. Agents and configured
// commands go to the generator as separate route sets.
func TestActivateWritesOnlyTheSnippet(t *testing.T) {
	for _, shell := range shellwrap.SupportedShells() {
		for _, commands := range [][]string{nil, {"bun", "npm"}} {
			t.Run(shell+"/"+strings.Join(commands, ","), func(t *testing.T) {
				env, out := newWrapperEnv(shell, "claude", "codex")
				env.commands = commands
				if err := activateWrappers(env); err != nil {
					t.Fatalf("activate: %v", err)
				}
				want, err := shellwrap.Snippet(shell, env.devsandboxPath, []string{"claude", "codex"}, commands)
				if err != nil {
					t.Fatalf("snippet: %v", err)
				}
				if out.String() != want {
					t.Errorf("activate output =\n%s\nwant\n%s", out, want)
				}
			})
		}
	}
}

// cleanupSnippet is what activate emits with nothing to wrap: the generated
// snippet still runs, because its cleanup is what removes the wrappers the
// previous activation defined.
func cleanupSnippet(t *testing.T, shell string, commands ...string) string {
	t.Helper()
	s, err := shellwrap.Snippet(shell, "/opt/bin/devsandbox", nil, commands)
	if err != nil {
		t.Fatalf("snippet: %v", err)
	}
	return s
}

// A host with no agent installed is not an error: activate runs on every shell
// start, and failing there would break the startup file. The output still says
// what happened instead of being bare, and still carries the snippet so that
// re-sourcing after the last wrapper went away removes the old definitions.
func TestActivateWithNoAgentsEmitsCommentAndCleanup(t *testing.T) {
	for _, shell := range shellwrap.SupportedShells() {
		t.Run(shell, func(t *testing.T) {
			env, out := newWrapperEnv(shell)
			if err := activateWrappers(env); err != nil {
				t.Fatalf("activate: %v", err)
			}
			comment, rest, ok := strings.Cut(out.String(), "\n")
			if !ok || !strings.HasPrefix(comment, "#") {
				t.Fatalf("output does not open with a comment line:\n%s", out)
			}
			if !strings.HasSuffix(comment, "; nothing wrapped") {
				t.Errorf("comment %q does not say nothing was wrapped", comment)
			}
			if named := agentsNamedInComment(t, comment); !reflect.DeepEqual(named, agentid.KnownAgents()) {
				t.Errorf("comment names %#v, want every supported agent %#v", named, agentid.KnownAgents())
			}
			if want := cleanupSnippet(t, shell); rest != want {
				t.Errorf("output after the comment =\n%s\nwant the cleanup-only snippet\n%s", rest, want)
			}
		})
	}
}

// Configured commands do not depend on any agent being installed, so they are
// still wrapped - and the comment must not claim nothing was.
func TestActivateWithOnlyConfiguredCommands(t *testing.T) {
	env, out := newWrapperEnv(shellwrap.ShellBash)
	env.commands = []string{"npm"}
	if err := activateWrappers(env); err != nil {
		t.Fatalf("activate: %v", err)
	}
	want := "# none of the supported agents (" + strings.Join(agentid.KnownAgents(), ", ") +
		") are installed on this host; no agent wrapped\n" + cleanupSnippet(t, shellwrap.ShellBash, "npm")
	if out.String() != want {
		t.Errorf("activate output =\n%s\nwant\n%s", out, want)
	}
}

// A snippet that cannot be generated must not leave a comment, or anything
// else, on stdout.
func TestActivateGenerationErrorWritesNothing(t *testing.T) {
	for _, agents := range [][]string{nil, {"claude"}} {
		env, out := newWrapperEnv(shellwrap.ShellBash, agents...)
		env.devsandboxPath = "devsandbox"
		if err := activateWrappers(env); err == nil {
			t.Fatalf("expected a generation error for a relative devsandbox path")
		}
		if out.Len() != 0 {
			t.Errorf("stdout must stay empty on a generation error, got:\n%s", out)
		}
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
	if err := activateWrappers(env); !errors.Is(err, errDiskOnFire) {
		t.Fatalf("activate error = %v, want the writer's error reported", err)
	}
}

// A stream write cannot be made atomic, so a writer that accepts a prefix and
// then fails is still reported rather than read as success.
func TestActivateReportsPartialWriteFailure(t *testing.T) {
	env, _ := newWrapperEnv(shellwrap.ShellBash, "claude")
	env.commands = []string{"npm"}
	w := &partialWriter{limit: 10}
	env.out = w
	if err := activateWrappers(env); !errors.Is(err, errDiskOnFire) {
		t.Fatalf("activate error = %v, want the writer's error reported", err)
	}
	if w.written != 10 {
		t.Errorf("writer accepted %d bytes, want the 10-byte prefix", w.written)
	}
}

var errDiskOnFire = errors.New("disk on fire")

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errDiskOnFire
}

type partialWriter struct {
	limit   int
	written int
}

func (w *partialWriter) Write(p []byte) (int, error) {
	n := min(len(p), w.limit-w.written)
	w.written += n
	if n < len(p) {
		return n, errDiskOnFire
	}
	return n, nil
}

// shell-wrappers is the canonical name; agent-wrappers stays an alias of the
// same command, so every startup file written for the old name keeps working
// and cannot drift from the new one.
func TestShellWrappersCommandRegistersActivate(t *testing.T) {
	cmd := newShellWrappersCmd()
	if cmd.Name() != "shell-wrappers" {
		t.Errorf("command name = %q, want shell-wrappers", cmd.Name())
	}
	if !slices.Contains(cmd.Aliases, "agent-wrappers") {
		t.Errorf("aliases = %v, want agent-wrappers kept for compatibility", cmd.Aliases)
	}
	var names []string
	for _, sub := range cmd.Commands() {
		names = append(names, sub.Name())
	}
	if !reflect.DeepEqual(names, []string{"activate"}) {
		t.Errorf("shell-wrappers subcommands = %v, want [activate]", names)
	}
}

// The help has to carry the line to paste, since activate's stdout is reserved
// for shell code and can say nothing to a human.
func TestShellWrappersHelpNamesTheActivationLine(t *testing.T) {
	long := newShellWrappersCmd().Long
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

// wrapperConfigEnv is a hermetic config layout for activation: a global config
// directory and a project directory that is the working directory.
type wrapperConfigEnv struct {
	configDir  string
	projectDir string
}

func (e wrapperConfigEnv) writeGlobal(t *testing.T, content string) {
	t.Helper()
	writeTestFile(t, filepath.Join(e.configDir, "config.toml"), content)
}

func (e wrapperConfigEnv) writeProject(t *testing.T, content string) {
	t.Helper()
	writeTestFile(t, filepath.Join(e.projectDir, ".devsandbox.toml"), content)
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// isolateWrapperConfig points config loading at empty temporary directories so
// activation never reads this host's config or the repository's working tree,
// and fails the test on any trust prompt it did not set up.
func isolateWrapperConfig(t *testing.T) wrapperConfigEnv {
	t.Helper()
	root := t.TempDir()
	xdg := filepath.Join(root, "config")
	env := wrapperConfigEnv{configDir: filepath.Join(xdg, "devsandbox")}
	project := filepath.Join(root, "project")
	for _, dir := range []string{env.configDir, project} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Chdir(project)
	// The loader keys trust and includes on the working directory as the
	// process sees it, which differs from the path given when TMPDIR is a
	// symlink.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	env.projectDir = wd
	setWrapperTrustPrompt(t, func(string, string, bool) (bool, error) {
		t.Errorf("unexpected trust prompt")
		return false, nil
	})
	return env
}

func setWrapperTrustPrompt(t *testing.T, prompt func(projectDir, content string, changed bool) (bool, error)) {
	t.Helper()
	prev := wrapperTrustPrompt
	wrapperTrustPrompt = prompt
	t.Cleanup(func() { wrapperTrustPrompt = prev })
}

// runActivate drives the real command the way a startup file does, and returns
// what a shell would have evaluated. Call isolateWrapperConfig first.
func runActivate(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newWrappersActivateCmd()
	out := &strings.Builder{}
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// runRootWrappers drives activation through a root command, so the name the
// user types - canonical or compatibility - is what is resolved.
func runRootWrappers(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "devsandbox", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(newShellWrappersCmd())
	out := &strings.Builder{}
	root.SetOut(out)
	root.SetErr(io.Discard)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// parseAgentSelection resolves --agents exactly as the command does, so the
// flag's own parsing is under test and not only the helper behind it.
func parseAgentSelection(t *testing.T, args ...string) ([]string, error) {
	t.Helper()
	cmd := newWrappersActivateCmd()
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
	want := "# none of the selected agents (codex) are installed on this host; nothing wrapped\n" +
		cleanupSnippet(t, shellwrap.ShellBash)
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
	want, err := shellwrap.Snippet(shellwrap.ShellFish, env.devsandboxPath, []string{"codex"}, nil)
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
	isolateWrapperConfig(t)
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

	env, err := resolveWrapperEnv(shellwrap.ShellBash, selection, noCommands, installed, io.Discard)
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
	env, err = resolveWrapperEnv(shellwrap.ShellBash, selection, noCommands,
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
	isolateWrapperConfig(t)
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
	isolateWrapperConfig(t)
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
	cmd := newWrappersActivateCmd()

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

// noCommands is a configured-commands loader for tests that are about agents.
func noCommands() ([]string, error) { return nil, nil }

// Every layer a launch in this directory would apply contributes: the global
// config, a host-owned include matching the project, and the trusted project
// file. The union is deduplicated and canonically ordered.
func TestConfiguredCommandsMergesGlobalIncludeAndTrustedProject(t *testing.T) {
	env := isolateWrapperConfig(t)
	include := filepath.Join(env.configDir, "work.toml")
	writeTestFile(t, include, "[shell_wrappers]\ncommands = [\"node\", \"bun\"]\n")
	env.writeGlobal(t, "[shell_wrappers]\ncommands = [\"npm\", \"bun\"]\n\n"+
		"[[include]]\nif = \"dir:"+env.projectDir+"\"\npath = \""+include+"\"\n")
	env.writeProject(t, "[shell_wrappers]\ncommands = [\"pnpm\", \"npm\", \"node\"]\n")
	setWrapperTrustPrompt(t, func(string, string, bool) (bool, error) { return true, nil })

	got, err := configuredCommands(io.Discard)
	if err != nil {
		t.Fatalf("configuredCommands: %v", err)
	}
	if want := []string{"bun", "node", "npm", "pnpm"}; !reflect.DeepEqual(got, want) {
		t.Errorf("configuredCommands = %#v, want %#v", got, want)
	}
}

func TestConfiguredCommandsEmptyByDefault(t *testing.T) {
	isolateWrapperConfig(t)
	got, err := configuredCommands(io.Discard)
	if err != nil {
		t.Fatalf("configuredCommands: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("configuredCommands = %#v, want none without config", got)
	}
}

// A project file that is not trusted contributes nothing, while the host-owned
// layers still apply - and a trusted file that changed is asked about again
// before its new commands reach a host shell.
func TestConfiguredCommandsFollowProjectTrust(t *testing.T) {
	env := isolateWrapperConfig(t)
	include := filepath.Join(env.configDir, "work.toml")
	writeTestFile(t, include, "[shell_wrappers]\ncommands = [\"bun\"]\n")
	env.writeGlobal(t, "[shell_wrappers]\ncommands = [\"npm\"]\n\n"+
		"[[include]]\nif = \"dir:"+env.projectDir+"\"\npath = \""+include+"\"\n")
	env.writeProject(t, "[shell_wrappers]\ncommands = [\"node\"]\n")

	type promptCall struct{ changed bool }
	var calls []promptCall
	answer := false
	setWrapperTrustPrompt(t, func(projectDir, content string, changed bool) (bool, error) {
		if projectDir != env.projectDir {
			t.Errorf("prompt for %q, want %q", projectDir, env.projectDir)
		}
		if !strings.Contains(content, "node") && !strings.Contains(content, "deno") {
			t.Errorf("prompt does not show the project commands:\n%s", content)
		}
		calls = append(calls, promptCall{changed: changed})
		return answer, nil
	})

	load := func(want ...string) {
		t.Helper()
		got, err := configuredCommands(io.Discard)
		if err != nil {
			t.Fatalf("configuredCommands: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("configuredCommands = %#v, want %#v", got, want)
		}
	}

	load("bun", "npm")
	if !reflect.DeepEqual(calls, []promptCall{{changed: false}}) {
		t.Fatalf("prompt calls after decline = %#v, want one new-file prompt", calls)
	}

	answer = true
	load("bun", "node", "npm")
	calls = nil
	load("bun", "node", "npm")
	if len(calls) != 0 {
		t.Errorf("an unchanged trusted file prompted again: %#v", calls)
	}

	env.writeProject(t, "[shell_wrappers]\ncommands = [\"deno\"]\n")
	answer = false
	load("bun", "npm")
	if !reflect.DeepEqual(calls, []promptCall{{changed: true}}) {
		t.Errorf("prompt calls after change = %#v, want one changed-file prompt", calls)
	}
}

// Configured commands resolve inside the sandbox, so they are emitted whether
// or not the host has them, and never looked up on the host. Agents are still
// discovered within the selection as before.
func TestResolveWrapperEnvDoesNotDiscoverConfiguredCommands(t *testing.T) {
	var looked []string
	lookPath := func(name string) (string, error) {
		looked = append(looked, name)
		return fakeLookPath(map[string]string{"claude": "/usr/bin/claude", "codex": "/usr/bin/codex"})(name)
	}
	commands := []string{"bun", "npm"}
	env, err := resolveWrapperEnv(shellwrap.ShellBash, []string{"claude"},
		func() ([]string, error) { return commands, nil }, lookPath, io.Discard)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !reflect.DeepEqual(env.commands, commands) {
		t.Errorf("env.commands = %#v, want %#v", env.commands, commands)
	}
	if !reflect.DeepEqual(env.agents, []string{"claude"}) {
		t.Errorf("env.agents = %#v, want [claude]", env.agents)
	}
	if !reflect.DeepEqual(looked, []string{"claude"}) {
		t.Errorf("host lookups = %#v, want only the selected agent", looked)
	}
}

// An unsupported shell is reported before config is loaded, so it cannot
// raise a trust prompt for a snippet that will never be generated.
func TestResolveWrapperEnvChecksShellBeforeLoadingConfig(t *testing.T) {
	_, err := resolveWrapperEnv("nu", agentid.KnownAgents(), func() ([]string, error) {
		t.Error("config loaded for an unsupported shell")
		return nil, nil
	}, fakeLookPath(nil), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unsupported shell") {
		t.Errorf("error = %v, want unsupported shell", err)
	}
}

// Both names run the same implementation, so a startup file written for either
// evaluates the same snapshot - configured commands included. The command is
// deliberately not installed on this host.
func TestShellWrappersAndAgentWrappersActivateIdentically(t *testing.T) {
	env := isolateWrapperConfig(t)
	env.writeGlobal(t, "[shell_wrappers]\ncommands = [\"devsandbox-test-absent-cmd\"]\n")

	for _, shell := range shellwrap.SupportedShells() {
		t.Run(shell, func(t *testing.T) {
			canonical, err := runRootWrappers(t, "shell-wrappers", "activate", shell)
			if err != nil {
				t.Fatalf("shell-wrappers activate: %v", err)
			}
			compat, err := runRootWrappers(t, "agent-wrappers", "activate", shell)
			if err != nil {
				t.Fatalf("agent-wrappers activate: %v", err)
			}
			if canonical != compat {
				t.Errorf("shell-wrappers produced\n%s\nbut agent-wrappers produced\n%s", canonical, compat)
			}
			if !strings.Contains(canonical, "run-command devsandbox-test-absent-cmd") {
				t.Errorf("configured command missing from the snippet:\n%s", canonical)
			}

			narrowed, err := runRootWrappers(t, "agent-wrappers", "activate", shell, "--agents", "codex")
			if err != nil {
				t.Fatalf("agent-wrappers activate --agents: %v", err)
			}
			canonicalNarrowed, err := runRootWrappers(t, "shell-wrappers", "activate", shell, "--agents", "codex")
			if err != nil {
				t.Fatalf("shell-wrappers activate --agents: %v", err)
			}
			if narrowed != canonicalNarrowed {
				t.Errorf("--agents differs between the two names:\n%s\nvs\n%s", narrowed, canonicalNarrowed)
			}
		})
	}
}

// Every failure that can be known before generation - a bad host-owned config,
// an unsupported shell - must leave stdout empty, because the startup file
// evaluates whatever arrives.
func TestActivatePreWriteFailuresWriteNothing(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, env wrapperConfigEnv)
		args  []string
		want  string
	}{
		{
			name: "invalid global config",
			setup: func(t *testing.T, env wrapperConfigEnv) {
				env.writeGlobal(t, "[shell_wrappers]\ncommands = [\"../npm\"]\n")
			},
			args: []string{shellwrap.ShellBash},
			want: "shell_wrappers.commands[0]",
		},
		{
			name: "unparsable global config beside a project file",
			setup: func(t *testing.T, env wrapperConfigEnv) {
				env.writeGlobal(t, "[[[ not toml\n")
				env.writeProject(t, "[[[ not toml\n")
			},
			args: []string{shellwrap.ShellBash},
			want: "config",
		},
		{
			name: "unsupported shell",
			setup: func(t *testing.T, env wrapperConfigEnv) {
				env.writeGlobal(t, "[shell_wrappers]\ncommands = [\"npm\"]\n")
			},
			args: []string{"nu"},
			want: "unsupported shell",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := isolateWrapperConfig(t)
			tt.setup(t, env)
			got, err := runActivate(t, tt.args...)
			if err == nil {
				t.Fatalf("expected an error, got output:\n%s", got)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
			if got != "" {
				t.Errorf("stdout must stay empty, got:\n%s", got)
			}
		})
	}
}

// The project directory is writable from inside the sandbox, and the project
// file is read and parsed before trust is asked. A project file that fails in
// any way is therefore skipped with a warning, never allowed to fail activation:
// that would let sandboxed code leave the next host shell with no wrappers, so
// a restored `claude --resume` would run on the host.
func TestActivateSkipsProjectConfigThatFailsToLoad(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, env wrapperConfigEnv)
		want  string
	}{
		{
			name: "unparsable project file",
			setup: func(t *testing.T, env wrapperConfigEnv) {
				env.writeProject(t, "[[[ not toml\n")
			},
			want: "parse",
		},
		{
			name: "project file is a directory",
			setup: func(t *testing.T, env wrapperConfigEnv) {
				if err := os.Mkdir(filepath.Join(env.projectDir, ".devsandbox.toml"), 0o700); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
			},
			want: "read",
		},
		{
			name: "project file is a fifo",
			setup: func(t *testing.T, env wrapperConfigEnv) {
				if err := syscall.Mkfifo(filepath.Join(env.projectDir, ".devsandbox.toml"), 0o600); err != nil {
					t.Fatalf("mkfifo: %v", err)
				}
			},
			want: "not a regular file",
		},
		{
			name: "invalid trusted project file",
			setup: func(t *testing.T, env wrapperConfigEnv) {
				env.writeProject(t, "[shell_wrappers]\ncommands = [\"npm-no-ds\"]\n")
				setWrapperTrustPrompt(t, func(string, string, bool) (bool, error) { return true, nil })
			},
			want: "shell_wrappers.commands[0]",
		},
		{
			name: "trust prompt error",
			setup: func(t *testing.T, env wrapperConfigEnv) {
				env.writeProject(t, "[shell_wrappers]\ncommands = [\"bun\"]\n")
				setWrapperTrustPrompt(t, func(string, string, bool) (bool, error) {
					return false, errors.New("no terminal")
				})
			},
			want: "no terminal",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binDir := t.TempDir()
			writeTestFile(t, filepath.Join(binDir, "claude"), "#!/bin/sh\n")
			if err := os.Chmod(filepath.Join(binDir, "claude"), 0o755); err != nil {
				t.Fatalf("chmod: %v", err)
			}
			t.Setenv("PATH", binDir)
			env := isolateWrapperConfig(t)
			env.writeGlobal(t, "[shell_wrappers]\ncommands = [\"npm\"]\n")

			for _, shell := range shellwrap.SupportedShells() {
				want, err := runActivate(t, shell)
				if err != nil {
					t.Fatalf("activate without a project file: %v", err)
				}
				for _, route := range []string{"run-agent claude", "run-command npm"} {
					if !strings.Contains(want, route) {
						t.Fatalf("baseline %s snippet lacks %q:\n%s", shell, route, want)
					}
				}

				tt.setup(t, env)
				cmd := newWrappersActivateCmd()
				out, stderr := &strings.Builder{}, &strings.Builder{}
				cmd.SetOut(out)
				cmd.SetErr(stderr)
				cmd.SetArgs([]string{shell})
				if err := cmd.Execute(); err != nil {
					t.Fatalf("activate %s failed on a broken project file: %v", shell, err)
				}
				if out.String() != want {
					t.Errorf("%s snippet with a broken project file:\n%s\nwant the host-owned snapshot:\n%s", shell, out, want)
				}
				if !strings.Contains(stderr.String(), ".devsandbox.toml") || !strings.Contains(stderr.String(), tt.want) {
					t.Errorf("stderr = %q, want a warning naming .devsandbox.toml and %q", stderr, tt.want)
				}
				if err := os.RemoveAll(filepath.Join(env.projectDir, ".devsandbox.toml")); err != nil {
					t.Fatalf("remove project file: %v", err)
				}
			}
		})
	}
}

// Sandboxed code can remove the project subdirectory a host shell sits in, and
// the config loader cannot even skip the project file without a working
// directory. Activation must still emit the agents and the global commands.
func TestActivateFromRemovedDirectoryAppliesGlobalConfig(t *testing.T) {
	binDir := t.TempDir()
	writeTestFile(t, filepath.Join(binDir, "claude"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(binDir, "claude"), 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Setenv("PATH", binDir)
	env := isolateWrapperConfig(t)
	env.writeGlobal(t, "[shell_wrappers]\ncommands = [\"npm\"]\n")
	if err := os.RemoveAll(env.projectDir); err != nil {
		t.Fatalf("remove working directory: %v", err)
	}
	if _, err := os.Getwd(); err == nil {
		t.Skip("this platform still resolves a removed working directory")
	}

	for _, shell := range shellwrap.SupportedShells() {
		cmd := newWrappersActivateCmd()
		out, stderr := &strings.Builder{}, &strings.Builder{}
		cmd.SetOut(out)
		cmd.SetErr(stderr)
		cmd.SetArgs([]string{shell})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("activate %s from a removed directory: %v", shell, err)
		}
		for _, route := range []string{"run-agent claude", "run-command npm"} {
			if !strings.Contains(out.String(), route) {
				t.Errorf("%s snippet lacks %q:\n%s", shell, route, out)
			}
		}
		if !strings.Contains(stderr.String(), "global config only") {
			t.Errorf("stderr = %q, want a warning that only the global config applies", stderr)
		}
	}
}

// Activation runs at shell start, where herdr may already have typed a resume
// line into the pane. By default it must decline an untrusted or changed
// project file without reading stdin, keep stdout to the host-owned snippet,
// and say on stderr how to approve the file.
func TestActivateDefaultDeclinesProjectTrustWithoutReadingStdin(t *testing.T) {
	env := isolateWrapperConfig(t)
	setWrapperTrustPrompt(t, nil)

	const typed = "y\n"
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	t.Cleanup(func() { _ = stdinR.Close() })
	if _, err := io.WriteString(stdinW, typed); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if err := stdinW.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}
	prevStdin := os.Stdin
	os.Stdin = stdinR
	t.Cleanup(func() { os.Stdin = prevStdin })

	env.writeGlobal(t, "[shell_wrappers]\ncommands = [\"npm\"]\n")
	env.writeProject(t, "[shell_wrappers]\ncommands = [\"bun\"]\n")

	want, err := shellwrap.Snippet(shellwrap.ShellBash, mustExecutable(t), installedAgents(agentid.KnownAgents(), exec.LookPath), []string{"npm"})
	if err != nil {
		t.Fatalf("Snippet: %v", err)
	}

	activate := func(wantState string) {
		t.Helper()
		cmd := newWrappersActivateCmd()
		out, stderr := &strings.Builder{}, &strings.Builder{}
		cmd.SetOut(out)
		cmd.SetErr(stderr)
		cmd.SetArgs([]string{shellwrap.ShellBash})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("activate: %v", err)
		}
		if !strings.HasSuffix(out.String(), want) {
			t.Errorf("stdout = %q, want it to end in the snippet without the project's commands %q", out, want)
		}
		for _, s := range []string{wantState, ".devsandbox.toml", "devsandbox launch"} {
			if !strings.Contains(stderr.String(), s) {
				t.Errorf("stderr = %q, want it to mention %q", stderr, s)
			}
		}
	}

	activate("untrusted")

	store, err := config.LoadTrustStore(config.TrustStorePath())
	if err != nil {
		t.Fatalf("load trust store: %v", err)
	}
	store.AddTrust(env.projectDir, "stale-hash")
	if err := store.Save(); err != nil {
		t.Fatalf("save trust store: %v", err)
	}
	activate("changed")

	rest, err := io.ReadAll(stdinR)
	if err != nil {
		t.Fatalf("read stdin: %v", err)
	}
	if string(rest) != typed {
		t.Errorf("stdin left after activation = %q, want the typed line %q untouched", rest, typed)
	}
}

// The project directory's name is chosen by whoever created it, which may be
// the sandbox, so it reaches the host terminal escaped.
func TestDeclineProjectTrustEscapesProjectDir(t *testing.T) {
	var stderr strings.Builder
	if _, err := declineProjectTrust(&stderr)("/tmp/proj\x1b[2K\u009bx", "", false); err != nil {
		t.Fatalf("declineProjectTrust: %v", err)
	}
	if strings.ContainsAny(stderr.String(), "\x1b\u009b") {
		t.Errorf("stderr = %q, want control characters escaped", stderr.String())
	}
}

func mustExecutable(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return exe
}

// activate's stdout cannot explain anything, so the help has to cover what
// configured commands add: where they come from, that the snapshot only
// changes when activation is evaluated again, and both ways to the host
// command.
func TestShellWrappersHelpDocumentsConfiguredCommands(t *testing.T) {
	parent := newShellWrappersCmd()
	activate := newWrappersActivateCmd()
	for _, help := range []string{parent.Long, activate.Long} {
		for _, want := range []string{
			"shell_wrappers.commands",
			".devsandbox.toml",
			"run-command",
			"-no-ds",
			"command npm",
			"again",
		} {
			if !strings.Contains(help, want) {
				t.Errorf("help does not mention %q:\n%s", want, help)
			}
		}
	}
	if !strings.Contains(activate.Example, "devsandbox shell-wrappers activate") {
		t.Errorf("examples do not use the canonical command:\n%s", activate.Example)
	}
	if strings.Contains(activate.Example, "agent-wrappers") {
		t.Errorf("examples still use the compatibility name:\n%s", activate.Example)
	}
}

// Wrapping a command is opt-in at every layer: an absent section and an empty
// list, globally or in a trusted project, emit no run-command wrapper.
func TestActivateWrapsNoCommandWithoutConfig(t *testing.T) {
	tests := []struct {
		name, global, project string
	}{
		{name: "absent"},
		{name: "empty global list", global: "[shell_wrappers]\ncommands = []\n"},
		{name: "empty trusted project list", project: "[shell_wrappers]\ncommands = []\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := isolateWrapperConfig(t)
			if tt.global != "" {
				env.writeGlobal(t, tt.global)
			}
			if tt.project != "" {
				env.writeProject(t, tt.project)
				setWrapperTrustPrompt(t, func(string, string, bool) (bool, error) { return true, nil })
			}
			for _, shell := range shellwrap.SupportedShells() {
				out, err := runActivate(t, shell)
				if err != nil {
					t.Fatalf("activate %s: %v", shell, err)
				}
				if strings.Contains(out, "run-command") {
					t.Errorf("%s snippet wraps a command without config:\n%s", shell, out)
				}
			}
		})
	}
}

// The acceptance path end to end: global and trusted project config feed the
// real activate command, and a real shell evaluates two of its snapshots - one
// taken while the project adds a command, one after the project list was
// emptied. The second must take down the project's wrapper and its bypass
// while keeping the global one.
func TestActivateSnapshotsFromConfigReconcileInShell(t *testing.T) {
	names := []string{"npm", "npm-no-ds", "bun", "bun-no-ds"}
	for _, shell := range shellwrap.SupportedShells() {
		t.Run(shell, func(t *testing.T) {
			bin, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s not installed", shell)
			}
			env := isolateWrapperConfig(t)
			env.writeGlobal(t, "[shell_wrappers]\ncommands = [\"npm\"]\n")
			env.writeProject(t, "[shell_wrappers]\ncommands = [\"bun\"]\n")
			setWrapperTrustPrompt(t, func(string, string, bool) (bool, error) { return true, nil })

			snapshot := func(label string) string {
				t.Helper()
				out, err := runActivate(t, shell)
				if err != nil {
					t.Fatalf("activate: %v", err)
				}
				path := filepath.Join(t.TempDir(), label)
				writeTestFile(t, path, out)
				return path
			}
			first := snapshot("first")
			env.writeProject(t, "[shell_wrappers]\ncommands = []\n")
			second := snapshot("second")

			probes := ""
			for _, n := range names {
				if shell == shellwrap.ShellFish {
					probes += "functions -q " + n + "; and echo fn:" + n + "\n"
				} else {
					probes += "typeset -f " + n + " >/dev/null 2>&1 && echo fn:" + n + "\n"
				}
			}
			script := "source '" + first + "'\n" + probes + "echo ---\n" +
				"source '" + second + "'\n" + probes + "true\n"
			driver := filepath.Join(t.TempDir(), "driver")
			writeTestFile(t, driver, script)

			args := map[string][]string{
				shellwrap.ShellFish: {"--no-config", driver},
				shellwrap.ShellBash: {"--norc", "--noprofile", driver},
				shellwrap.ShellZsh:  {"-f", driver},
			}[shell]
			cmd := exec.Command(bin, args...)
			cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir()}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s failed: %v\noutput:\n%s", shell, err, out)
			}
			want := "fn:npm\nfn:npm-no-ds\nfn:bun\nfn:bun-no-ds\n---\nfn:npm\nfn:npm-no-ds\n"
			if string(out) != want {
				t.Errorf("output = %q, want %q", out, want)
			}
		})
	}
}
