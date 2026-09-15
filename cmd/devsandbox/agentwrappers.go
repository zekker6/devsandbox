package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"devsandbox/internal/agentid"
	"devsandbox/internal/config"
	"devsandbox/internal/shellwrap"
	"devsandbox/internal/termsafe"
)

// wrapperEnv is everything activate operates on, resolved once so the command
// is a pure function of it and therefore testable without touching the real
// PATH.
type wrapperEnv struct {
	// shell is a supported shell name, never a path.
	shell string
	// devsandboxPath is devsandbox's absolute path, baked into the snippet so a
	// pane shell with a different PATH still resolves it.
	devsandboxPath string
	// selected are the agents --agents asked for, already validated and in
	// canonical order; every known agent when the flag was omitted. Kept apart
	// from agents so the no-wrapper comment names what was asked for rather
	// than claiming nothing is installed while an unselected agent is.
	selected []string
	// agents are the selected agents actually installed on this host.
	agents []string
	// commands are the configured shell_wrappers.commands. They are not
	// filtered by host discovery: they resolve inside the sandbox.
	commands []string
	out      io.Writer
}

// wrapperTrustPrompt answers for a project .devsandbox.toml that is not already
// trusted. Tests replace it; nil is declineProjectTrust.
var wrapperTrustPrompt func(projectDir, content string, changed bool) (bool, error)

// shellWrappersHelp is the explanation shared by the parent command and
// activate, which is where a user reading about the startup line lands.
const shellWrappersHelp = `Supported agents are wrapped when installed on this host: typing ` + "`claude`" + ` runs
` + "`devsandbox run-agent claude`" + ` in the current directory. Commands listed in
shell_wrappers.commands are wrapped whether or not the host has them, since they
resolve inside the sandbox: with commands = ["npm"], typing ` + "`npm install`" + ` runs
` + "`devsandbox run-command npm install`" + `. Nothing is wrapped by that list by default.
Two escape hatches always reach the real, unsandboxed binary: ` + "`npm-no-ds`" + ` and
the shell builtin ` + "`command npm`" + `.

The command list is read the way a launch in the current directory reads it: the
global config, matching includes, and a .devsandbox.toml you have trusted, all
combined. Activation never asks for trust, because it runs at shell start where
a question would swallow the next typed line: an untrusted or changed project
file is skipped with a note on stderr until you approve it at the prompt of a
launch in that directory, which shows the file first.

The wrappers are a snapshot of that config, taken when activation is evaluated.
Changing the config, or moving to a project with its own list, changes nothing
until you evaluate activation again in that directory; doing so removes the
wrappers the previous activation defined before defining the current set, and
leaves your own functions alone.`

func newShellWrappersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "shell-wrappers",
		Aliases: []string{"agent-wrappers"},
		Short:   "Shell wrappers that run supported AI agents and configured commands inside devsandbox",
		Long: `Shell wrappers that run supported AI agents and configured commands inside devsandbox.

` + shellWrappersHelp + `

Nothing is installed and no startup file is ever edited. ` + "`activate`" + ` prints the
wrapper definitions and you evaluate them from your own startup file, when and
where you see fit:

` + activationExamples() + `Because the snippet is generated at every shell start, a newly installed agent
or an upgrade that moved the devsandbox binary is picked up by the next shell.

` + "`agent-wrappers`" + ` is the previous name of this command and remains an alias, so
existing startup lines keep working and produce the same snippet.

The wrappers are independent of herdr: they are useful on their own, and herdr's
native session restore builds on them - which means the line has to be in the
startup file of the shell herdr opens panes with, not only your login shell.`,
	}

	cmd.AddCommand(newWrappersActivateCmd())

	return cmd
}

// activationExamples renders the line to add for every supported shell. It is
// generated rather than written out so a newly supported shell cannot be
// missing from the help.
func activationExamples() string {
	var b strings.Builder
	for _, shell := range shellwrap.SupportedShells() {
		fmt.Fprintf(&b, "  # %s (%s)\n  %s\n\n", shell, shellwrap.StartupFile(shell), shellwrap.ActivateLine(shell))
	}
	return b.String()
}

func newWrappersActivateCmd() *cobra.Command {
	var agents []string

	cmd := &cobra.Command{
		Use:   "activate [shell]",
		Short: "Print the wrapper definitions for your shell",
		Long: `Print the wrapper definitions for your shell on stdout.

The output is shell code meant to be evaluated, not read:

` + activationExamples() + `The shell defaults to the base name of $SHELL.

` + shellWrappersHelp + `

With no agent installed the output opens with a comment saying so, and still
removes the previous activation's wrappers, so a startup file that evaluates it
keeps working. A global config or include that fails to load, or an unsupported
shell, stops before any shell code is written. A project .devsandbox.toml that
fails to load is skipped with a warning on stderr instead: the sandbox can write
that file, and it must not be able to leave a host shell without wrappers. For
the same reason a current directory that no longer exists applies the global
config alone, with a warning on stderr.

--agents narrows what is wrapped to the agents you name, leaving the rest to run
unsandboxed as usual. Values are given comma-separated, by repeating the flag,
or both: ` + "`--agents claude,codex`" + ` and ` + "`--agents claude --agents codex`" + ` select the
same pair. Omit the flag to wrap every supported agent (` + strings.Join(agentid.KnownAgents(), ", ") + `),
which is what activate did before the flag existed. An unsupported name fails
before any shell code is written, so a startup file never evaluates half a
snippet; a selected agent that is merely not installed is not an error, and is
picked up by the next shell once you install it. --agents does not affect
configured commands.

Inside a sandbox the definitions are inert - the whole snippet is guarded on
DEVSANDBOX - so a wrapper cannot recurse.`,
		Example: `  devsandbox shell-wrappers activate fish | source
  eval "$(devsandbox shell-wrappers activate bash)"
  devsandbox shell-wrappers activate fish --agents claude,codex | source
  eval "$(devsandbox shell-wrappers activate bash --agents claude --agents codex)"`,
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var shellArg string
			if len(args) == 1 {
				shellArg = args[0]
			}
			// Changed is what separates an omitted flag from an explicitly
			// empty one; resolved before anything is discovered so a bad
			// selection cannot reach stdout.
			selected, err := resolveAgentSelection(agents, cmd.Flags().Changed("agents"))
			if err != nil {
				return err
			}
			loadCommands := func() ([]string, error) { return configuredCommands(cmd.ErrOrStderr()) }
			env, err := resolveWrapperEnv(shellArg, selected, loadCommands, exec.LookPath, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return activateWrappers(env)
		},
	}
	cmd.Flags().StringSliceVar(&agents, "agents", nil,
		"Agents to wrap, comma-separated or repeated (default: every supported agent - "+
			strings.Join(agentid.KnownAgents(), ", ")+")")

	return cmd
}

// configuredCommands returns the effective shell_wrappers.commands for the
// current directory. The project file applies only once trusted, because its
// commands become host shell definitions.
//
// A project file that fails to load is skipped with a warning instead of
// failing activation. The project directory is writable from inside the
// sandbox, and the file is parsed before trust is checked, so failing here
// would let sandboxed code leave the next host shell with no wrappers at all -
// agents included. Only a failure of the host-owned layers is returned.
//
// The same holds for a working directory that no longer resolves: the loader
// needs it even to skip the project file, and sandboxed code can remove the
// project subdirectory a host shell sits in. With no directory no include can
// match, so the global config alone is what applies. A removed directory never
// resolves again, so checking it after the loads cannot race them.
func configuredCommands(stderr io.Writer) ([]string, error) {
	onPrompt := wrapperTrustPrompt
	if onPrompt == nil {
		onPrompt = declineProjectTrust(stderr)
	}
	cfg, _, _, err := config.LoadConfigWithOptions(&config.LoadOptions{OnLocalConfigPrompt: onPrompt})
	if err == nil {
		return cfg.ShellWrappers.EffectiveCommands(), nil
	}
	hostCfg, _, _, hostErr := config.LoadConfigWithOptions(&config.LoadOptions{SkipLocalConfig: true})
	if hostErr == nil {
		_, _ = fmt.Fprintf(stderr, "devsandbox: shell wrappers skip %s: %s\n", config.LocalConfigFile, termsafe.Escape(err.Error()))
		return hostCfg.ShellWrappers.EffectiveCommands(), nil
	}
	_, wdErr := os.Getwd()
	if wdErr == nil {
		return nil, hostErr
	}
	globalCfg, globalErr := config.LoadFrom(config.ConfigPath())
	if globalErr != nil {
		return nil, fmt.Errorf("failed to load config: %w", globalErr)
	}
	_, _ = fmt.Fprintf(stderr, "devsandbox: shell wrappers use the global config only, the current directory cannot be resolved: %s\n",
		termsafe.Escape(wdErr.Error()))
	return globalCfg.ShellWrappers.EffectiveCommands(), nil
}

// declineProjectTrust is activation's answer for a project file that is not
// already trusted. It never reads stdin: activation runs at shell start, where
// the next line on the terminal may be the `claude --resume` herdr types into a
// restored pane, and a prompt would consume it as the answer. The project
// directory is sandbox-writable, so a stale hash is something sandboxed code
// can arrange at will - which is why the note points at a launch, whose prompt
// shows the file, and not at `trust add`, which approves it unseen.
func declineProjectTrust(stderr io.Writer) func(projectDir, content string, changed bool) (bool, error) {
	return func(projectDir, _ string, changed bool) (bool, error) {
		state := "untrusted"
		if changed {
			state = "changed"
		}
		_, _ = fmt.Fprintf(stderr, "devsandbox: shell wrappers skip %s %s in %s; review and approve it at the prompt of a devsandbox launch in that directory, then run activation again\n",
			state, config.LocalConfigFile, termsafe.Escape(projectDir))
		return false, nil
	}
}

// resolveWrapperEnv gathers the host facts activate operates on, over the
// already-validated selection. The shell is checked before loadCommands runs,
// so an unsupported shell never reads the project config.
func resolveWrapperEnv(shellArg string, selected []string, loadCommands func() ([]string, error), lookPath func(string) (string, error), out io.Writer) (wrapperEnv, error) {
	shell, err := detectShell(shellArg, os.Getenv("SHELL"))
	if err != nil {
		return wrapperEnv{}, err
	}

	commands, err := loadCommands()
	if err != nil {
		return wrapperEnv{}, err
	}

	// Bake in our own absolute path rather than `command devsandbox`: a herdr
	// pane may be a login shell whose PATH differs from this one. os.Executable
	// is documented to return an absolute path.
	exe, err := os.Executable()
	if err != nil {
		return wrapperEnv{}, fmt.Errorf("resolve devsandbox executable: %w", err)
	}

	return wrapperEnv{
		shell:          shell,
		devsandboxPath: exe,
		selected:       selected,
		agents:         installedAgents(selected, lookPath),
		commands:       commands,
		out:            out,
	}, nil
}

// detectShell resolves the shell to generate for. The argument wins; otherwise
// $SHELL is used, which is a path, so only its base name is meaningful. A login
// shell's argv[0] convention of a leading "-" is tolerated.
func detectShell(shellArg, shellEnv string) (string, error) {
	name := shellArg
	source := "argument"
	if name == "" {
		name = shellEnv
		source = "$SHELL"
	}
	if name == "" {
		return "", fmt.Errorf("cannot detect your shell: $SHELL is unset; pass it as an argument (%s)",
			strings.Join(shellwrap.SupportedShells(), ", "))
	}
	name = strings.TrimPrefix(filepath.Base(name), "-")
	if !shellwrap.IsSupportedShell(name) {
		return "", fmt.Errorf("unsupported shell %q (from %s): supported shells are %s",
			name, source, strings.Join(shellwrap.SupportedShells(), ", "))
	}
	return name, nil
}

// installedAgents filters the selected agents down to the ones present on this
// host, so a wrapper is never generated for a binary the user does not have.
func installedAgents(names []string, lookPath func(string) (string, error)) []string {
	found := make([]string, 0, len(names))
	for _, n := range names {
		if _, err := lookPath(n); err == nil {
			found = append(found, n)
		}
	}
	return found
}

// activateWrappers writes the snippet to env.out.
//
// The snippet is generated even when nothing is wrapped: its cleanup is what
// removes the wrappers a previous activation defined.
//
// Having no agent installed is not an error: this runs on every shell start,
// and there is nothing to wrap when none of the binaries exist. It is still
// said out loud, in a leading comment, rather than left implicit.
// The comment names env.selected rather than every known agent: with --agents
// it would otherwise report on agents the user deliberately left out. Only the
// full set may be called "supported" - a narrowed selection worded that way
// asserts devsandbox supports nothing else, which is the opposite of what
// --agents did - and the flag-omitted wording is unchanged because the two
// lists are identical then.
func activateWrappers(env wrapperEnv) error {
	snippet, err := shellwrap.Snippet(env.shell, env.devsandboxPath, env.agents, env.commands)
	if err != nil {
		return err
	}
	if len(env.agents) == 0 {
		scope := "selected"
		if slices.Equal(env.selected, agentid.KnownAgents()) {
			scope = "supported"
		}
		outcome := "nothing wrapped"
		if len(env.commands) > 0 {
			outcome = "no agent wrapped"
		}
		snippet = fmt.Sprintf("# none of the %s agents (%s) are installed on this host; %s\n",
			scope, strings.Join(env.selected, ", "), outcome) + snippet
	}

	// A write error is reported rather than swallowed. It may arrive after a
	// prefix was already written - a stream cannot be written atomically - so
	// the error is the only signal that the shell evaluated a truncated snippet.
	if _, err := io.WriteString(env.out, snippet); err != nil {
		return fmt.Errorf("write wrapper snippet: %w", err)
	}
	return nil
}

// resolveAgentSelection turns the --agents values into the agents to consider,
// in the canonical order of agentid.KnownAgents.
//
// explicit is whether the flag was given at all, which is what separates an
// omitted flag - selecting every known agent, the behavior from before the flag
// existed - from an explicitly empty one. The latter can only ever wrap nothing,
// so it is an error rather than a startup file that silently stops defining
// wrappers.
func resolveAgentSelection(requested []string, explicit bool) ([]string, error) {
	known := agentid.KnownAgents()
	if !explicit {
		return known, nil
	}

	wanted := make(map[string]bool, len(requested))
	var unsupported []string
	for _, name := range requested {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !slices.Contains(known, name) {
			if !slices.Contains(unsupported, name) {
				unsupported = append(unsupported, name)
			}
			continue
		}
		wanted[name] = true
	}

	// Every unsupported name is reported at once: fixing them one shell start at
	// a time is the same typo found five times.
	if len(unsupported) > 0 {
		noun := "agent"
		if len(unsupported) > 1 {
			noun = "agents"
		}
		quoted := make([]string, 0, len(unsupported))
		for _, name := range unsupported {
			quoted = append(quoted, fmt.Sprintf("%q", name))
		}
		return nil, fmt.Errorf("unsupported %s %s: supported agents are %s",
			noun, strings.Join(quoted, ", "), strings.Join(known, ", "))
	}
	if len(wanted) == 0 {
		return nil, fmt.Errorf("no agents selected: pass at least one of %s, or omit --agents to select them all",
			strings.Join(known, ", "))
	}

	// Ordering follows the known-agent list rather than the command line, so the
	// generated snippet does not change with the order the values were typed.
	selected := make([]string, 0, len(wanted))
	for _, name := range known {
		if wanted[name] {
			selected = append(selected, name)
		}
	}
	return selected, nil
}
