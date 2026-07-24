package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"devsandbox/internal/agentid"
	"devsandbox/internal/shellwrap"
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
	// agents are the known agents actually installed on this host.
	agents []string
	out    io.Writer
}

func newAgentWrappersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent-wrappers",
		Short: "Shell wrappers that run supported AI agents inside devsandbox",
		Long: `Shell wrappers that run supported AI agents inside devsandbox.

Once activated, typing ` + "`claude`" + ` runs ` + "`devsandbox claude`" + ` in the current
directory. Two escape hatches always reach the real, unsandboxed binary:
` + "`claude-no-ds`" + ` and the shell builtin ` + "`command claude`" + `.

Nothing is installed and no startup file is ever edited. ` + "`activate`" + ` prints the
wrapper definitions and you evaluate them from your own startup file, when and
where you see fit:

` + activationExamples() + `Because the snippet is generated at every shell start, it never goes stale: a
newly installed agent or an upgrade that moved the devsandbox binary is picked
up by the next shell.

The wrappers are independent of herdr: they are useful on their own, and herdr's
native session restore builds on them - which means the line has to be in the
startup file of the shell herdr opens panes with, not only your login shell.`,
	}

	cmd.AddCommand(newAgentWrappersActivateCmd())

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

func newAgentWrappersActivateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "activate [shell]",
		Short: "Print the wrapper definitions for your shell",
		Long: `Print the wrapper definitions for your shell on stdout.

The output is shell code meant to be evaluated, not read:

` + activationExamples() + `The shell defaults to the base name of $SHELL. Only agents actually installed
on this host are wrapped; with none installed the output is a comment saying so,
so a startup file that evaluates it keeps working.

Inside a sandbox the definitions are inert - the whole snippet is guarded on
DEVSANDBOX - so a wrapper cannot recurse.`,
		Example: `  devsandbox agent-wrappers activate fish | source
  eval "$(devsandbox agent-wrappers activate bash)"`,
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var shellArg string
			if len(args) == 1 {
				shellArg = args[0]
			}
			env, err := resolveWrapperEnv(shellArg, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return activateWrappers(env)
		},
	}
	return cmd
}

// resolveWrapperEnv gathers the host facts activate operates on.
func resolveWrapperEnv(shellArg string, out io.Writer) (wrapperEnv, error) {
	shell, err := detectShell(shellArg, os.Getenv("SHELL"))
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
		agents:         installedAgents(agentid.KnownAgents(), exec.LookPath),
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

// installedAgents filters known agents down to the ones present on this host,
// so a wrapper is never generated for a binary the user does not have.
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
// Having no agent installed is not an error: this runs on every shell start,
// and there is nothing to wrap when none of the binaries exist. It is still
// said out loud, in the output itself, rather than emitting nothing.
func activateWrappers(env wrapperEnv) error {
	snippet := fmt.Sprintf("# none of the supported agents (%s) are installed on this host; nothing wrapped\n",
		strings.Join(agentid.KnownAgents(), ", "))
	if len(env.agents) > 0 {
		generated, err := shellwrap.Snippet(env.shell, env.devsandboxPath, env.agents)
		if err != nil {
			return err
		}
		snippet = generated
	}

	// A write error is reported rather than swallowed: the output is the whole
	// deliverable here, and a shell that evaluates a truncated snippet gets
	// wrappers for some agents and not others.
	if _, err := io.WriteString(env.out, snippet); err != nil {
		return fmt.Errorf("write wrapper snippet: %w", err)
	}
	return nil
}
