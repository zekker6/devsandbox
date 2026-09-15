package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/spf13/cobra"

	"devsandbox/internal/isolator"
	"devsandbox/internal/shellwrap"
)

// runCommandSandbox is the sandbox launch path run-command hands a wrapped
// command to outside a sandbox. Tests replace it to observe the process
// boundary without launching a sandbox.
var runCommandSandbox = runSandbox

// newRunCommandCmd creates the entrypoint the generated shell wrappers for
// shell_wrappers.commands call. It is a subcommand of its own, rather than
// `devsandbox <command>`, so a wrapped command named `config` or `tools` runs
// as a workload instead of reaching the management subcommand of that name.
func newRunCommandCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run-command <command> [args...]",
		Short: "Run a wrapped shell command inside devsandbox",
		Long: `Run a command configured in shell_wrappers.commands inside devsandbox.

This is the target of the shell wrappers emitted for configured commands:
typing ` + "`npm install`" + ` runs ` + "`devsandbox run-command npm install`" + `, which launches
the sandbox in the current directory. The command is resolved inside the
sandbox, so it need not exist on the host. Arguments after the command name
are passed through untouched.

Inside a sandbox (DEVSANDBOX is set) the real command is executed directly, so
a wrapper that is visible in-sandbox cannot recurse.`,
		Example: `  devsandbox run-command npm install
  devsandbox run-command bun run dev`,
		Args:                  cobra.ArbitraryArgs,
		DisableFlagsInUseLine: true,
		SilenceUsage:          true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCommand(cmd, args)
		},
	}

	// Parsing stops at the command name: everything after it belongs to the
	// workload, including flags that share a name with a sandbox flag.
	cmd.Flags().SetInterspersed(false)
	addSandboxFlags(cmd)

	return cmd
}

// commandInvocation is how run-command runs a wrapped command.
type commandInvocation struct {
	// Direct is true inside a sandbox, where Path replaces run-command. Outside
	// one, Argv is handed to the sandbox launch path instead.
	Direct bool
	// Path is the absolute path of the program to execute when Direct is set.
	Path string
	// Argv is the full argument vector, including the command name.
	Argv []string
}

// planCommandInvocation decides how a wrapped command runs. The sandbox marker
// is tested for non-emptiness, matching the generated shell snippets, so
// DEVSANDBOX="" means the same thing in Go as it does in fish, bash and zsh.
func planCommandInvocation(args []string, sandboxMarker string, lookPath func(string) (string, error)) (commandInvocation, error) {
	if len(args) == 0 {
		return commandInvocation{}, errors.New("run-command requires a command name")
	}
	name := args[0]
	if err := shellwrap.ValidateCommandName(name); err != nil {
		return commandInvocation{}, err
	}
	argv := append([]string(nil), args...)

	if sandboxMarker == "" {
		// Not resolved on the host: the sandbox resolves it against its own
		// PATH, which can legitimately differ.
		return commandInvocation{Argv: argv}, nil
	}

	prog, err := lookPath(name)
	if err != nil {
		return commandInvocation{}, fmt.Errorf("%s not found in PATH: %w", name, err)
	}
	return commandInvocation{Direct: true, Path: prog, Argv: argv}, nil
}

func runCommand(cmd *cobra.Command, args []string) error {
	inv, err := planCommandInvocation(args, os.Getenv("DEVSANDBOX"), exec.LookPath)
	if errors.Is(err, exec.ErrNotFound) {
		// A shell reports a missing command as 127, and a wrapper must not
		// change what the calling shell sees. main maps CommandExitError to the
		// status without printing, so this diagnostic is the only one.
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "devsandbox run-command: %s: command not found\n", args[0])
		return &isolator.CommandExitError{Code: 127}
	}
	if err != nil {
		return err
	}

	if !inv.Direct {
		return runCommandSandbox(cmd, inv.Argv)
	}
	return syscall.Exec(inv.Path, inv.Argv, os.Environ())
}
