package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"devsandbox/internal/agentid"
	"devsandbox/internal/herdrstate"
)

// newRunAgentCmd creates the entrypoint the generated shell wrappers call.
// The wrapper is deliberately dumb - it forwards argv unchanged - so every
// decision about where an agent actually runs lives here, in Go.
func newRunAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run-agent <agent> [args...]",
		Short: "Run a supported AI agent inside devsandbox",
		Long: `Run a supported AI agent inside devsandbox.

This is the target of the shell wrappers installed by
` + "`devsandbox agent-wrappers install`" + `: typing ` + "`claude`" + ` runs
` + "`devsandbox run-agent claude`" + `, which re-enters the sandbox in the current
directory. Arguments are passed through untouched, so ` + "`claude --resume ID`" + `
resumes the session inside the sandbox that created it.

Inside a sandbox (DEVSANDBOX is set) the real agent is executed directly, so a
wrapper that is visible in-sandbox cannot recurse.`,
		Example: `  devsandbox run-agent claude
  devsandbox run-agent claude --resume 0d4c2b1e-6f8a-4c31-9f7d-2a5b8c1e4f60`,
		Args:                  cobra.ArbitraryArgs,
		DisableFlagsInUseLine: true,
		SilenceUsage:          true,
		RunE: func(_ *cobra.Command, args []string) error {
			return runAgent(args)
		},
	}

	// The root command sets both of these on itself, and a subcommand inherits
	// neither. Without them `run-agent claude --resume ID` fails with
	// "unknown flag" instead of reaching the agent.
	cmd.Flags().SetInterspersed(false)

	return cmd
}

// agentInvocation is the process run-agent replaces itself with.
type agentInvocation struct {
	// Path is the absolute path of the program to execute.
	Path string
	// Argv is the full argument vector, including argv[0].
	Argv []string
}

// planAgentInvocation resolves an agent invocation to the process that should
// replace run-agent, implementing branches 1 and 3 of the restore routing:
//
//  1. inside a sandbox (sandboxMarker non-empty) -> execute the real agent.
//  2. otherwise -> re-enter devsandbox as `devsandbox <agent> [args...]`.
//
// The sandbox marker is tested for non-emptiness, matching the generated shell
// snippets, so DEVSANDBOX="" means the same thing in Go as it does in fish,
// bash and zsh.
//
// self and lookPath are injected so every branch is testable without execing.
func planAgentInvocation(agent string, args []string, sandboxMarker string, self func() (string, error), lookPath func(string) (string, error)) (agentInvocation, error) {
	if agent == "" {
		return agentInvocation{}, fmt.Errorf("run-agent requires an agent name (supported: %s)", strings.Join(agentid.KnownAgents(), ", "))
	}
	// Match on the name as given, not on a path: the wrapper always passes a
	// bare agent name, and accepting a path would let any binary named after a
	// known agent through.
	if agentid.CanonicalAgent([]string{agent}) != agent {
		return agentInvocation{}, fmt.Errorf("unknown agent %q (supported: %s)", agent, strings.Join(agentid.KnownAgents(), ", "))
	}

	if sandboxMarker != "" {
		prog, err := lookPath(agent)
		if err != nil {
			return agentInvocation{}, fmt.Errorf("%s not found in PATH: %w", agent, err)
		}
		return agentInvocation{Path: prog, Argv: append([]string{agent}, args...)}, nil
	}

	// Re-enter through our own binary rather than exec.LookPath("devsandbox"),
	// so a shadowing PATH entry cannot redirect re-entry into something else.
	//
	// The agent itself is deliberately NOT resolved here: devsandbox resolves it
	// inside the sandbox, where PATH can legitimately differ from the host's
	// (a docker image with the agent baked in has no host binary at all).
	exe, err := self()
	if err != nil {
		return agentInvocation{}, fmt.Errorf("resolve devsandbox executable: %w", err)
	}
	argv := make([]string, 0, len(args)+2)
	argv = append(argv, filepath.Base(exe), agent)
	argv = append(argv, args...)
	return agentInvocation{Path: exe, Argv: argv}, nil
}

// herdrRestoreContext is the host-derived state the worktree guard reads. Every
// field comes from the host environment or the host-owned pane store; nothing
// in it can be influenced by sandboxed code.
type herdrRestoreContext struct {
	// PaneID is the pane herdr created for this process, empty outside a herdr
	// pane. It is the only pane anchor, matching what the record writer and the
	// proxy's capability grant key on: a pane that exports HERDR_PANE_ID gets a
	// mapping recorded, so the same condition must arm the guard that honors it.
	PaneID string
	// Cwd is the directory a re-entry would launch the sandbox from.
	Cwd string
	// Load reads the pane mapping recorded at sandbox launch.
	Load func(paneID string) (herdrstate.Record, error)
}

// checkHerdrWorktreeGuard implements branch 2 of the restore routing: a resume
// typed into a herdr pane that would not re-enter the sandbox holding the
// session fails closed.
//
// A session is reachable again only from the directory it was launched in: the
// agent's session store lives in the synthetic home under the sandbox state
// root, and the agent keys its own sessions by project path. Re-entering from
// anywhere else opens a different store, or the same store under a different
// project key, and silently starts a fresh session while looking like a resume.
//
// --worktree is the case that makes this non-obvious, and the reason the check
// cannot be a sandbox-root comparison alone: the root is re-derived from the
// repo root while the project directory is the worktree path, so a pane sitting
// in the repo root derives the *matching* root and would otherwise pass.
//
// Every other case returns nil and falls through to ordinary re-entry. That is
// safe by construction: re-entry also enters a sandbox, so no branch here can
// run an agent on the host.
func checkHerdrWorktreeGuard(agent string, args []string, ctx herdrRestoreContext) error {
	if ctx.PaneID == "" || ctx.Load == nil {
		return nil
	}
	if !agentid.IsResumeInvocation(agent, args) {
		return nil
	}

	rec, err := ctx.Load(ctx.PaneID)
	if err != nil {
		if errors.Is(err, herdrstate.ErrNotFound) {
			// This pane never launched a sandboxed agent, so there is nothing
			// to protect.
			return nil
		}
		// A record that exists but cannot be read is the one Load failure that
		// must not fall through: it may be the very mapping this guard exists to
		// honor, and re-entering blind is the silent-new-session outcome.
		return fmt.Errorf("refusing to resume %s: the sandbox mapping recorded for this herdr pane could not be read: %w", agent, err)
	}
	if err := herdrstate.Validate(rec, ctx.PaneID, agent); err != nil {
		return nil
	}

	if !filepath.IsAbs(ctx.Cwd) {
		return fmt.Errorf("refusing to resume %s: the current directory could not be determined, so the sandbox recorded for this pane cannot be verified", agent)
	}
	cwd := filepath.Clean(ctx.Cwd)
	if cwd == filepath.Clean(rec.ProjectDir) && herdrstate.DerivesSameSandboxRoot(rec, cwd) {
		return nil
	}

	// A record whose own sandbox root does not derive from its own project
	// directory is the --worktree signature, and it holds from either vantage
	// point: standing in the worktree (where re-entry derives a different root)
	// and standing in the repo root (where it derives the same root but the
	// wrong project). Naming the cause is what makes the error actionable.
	if !herdrstate.DerivesSameSandboxRoot(rec, rec.ProjectDir) {
		return fmt.Errorf("refusing to resume %s: this pane's session was launched with --worktree, so it ran in %s "+
			"with its sandbox state in %s - re-entering from %s would open a different agent session store "+
			"and silently begin a new session instead. "+
			"Re-run the original `devsandbox --worktree ... %s` invocation, or start a fresh session with `%s`",
			agent, rec.ProjectDir, rec.SandboxRoot, cwd, agent, agent)
	}
	return fmt.Errorf("refusing to resume %s: this pane's session ran in %s, "+
		"while starting from %s would use a different sandbox - "+
		"the resume would silently begin a new session instead. "+
		"Change to %s first, or start a fresh session with `%s`",
		agent, rec.ProjectDir, cwd, rec.ProjectDir, agent)
}

// loadHerdrPaneRecord reads a pane mapping without creating the store, so a
// plain `run-agent` outside a herdr pane leaves no state behind.
func loadHerdrPaneRecord(paneID string) (herdrstate.Record, error) {
	dir, err := herdrstate.DefaultDir()
	if err != nil {
		return herdrstate.Record{}, err
	}
	return herdrstate.NewStore(dir).Load(paneID)
}

// resolveAgentRun applies the restore routing in order: the worktree guard runs
// only outside a sandbox, because branch 1 (execute the real agent in-sandbox)
// takes precedence over it.
func resolveAgentRun(agent string, args []string, sandboxMarker string, guard herdrRestoreContext, self func() (string, error), lookPath func(string) (string, error)) (agentInvocation, error) {
	if sandboxMarker == "" {
		if err := checkHerdrWorktreeGuard(agent, args, guard); err != nil {
			return agentInvocation{}, err
		}
	}
	return planAgentInvocation(agent, args, sandboxMarker, self, lookPath)
}

func runAgent(args []string) error {
	agent := ""
	if len(args) > 0 {
		agent = args[0]
	}
	var rest []string
	if len(args) > 1 {
		rest = args[1:]
	}

	guard := herdrRestoreContext{
		PaneID: os.Getenv("HERDR_PANE_ID"),
		Load:   loadHerdrPaneRecord,
	}
	if wd, err := os.Getwd(); err == nil {
		guard.Cwd = wd
	}

	inv, err := resolveAgentRun(agent, rest, os.Getenv("DEVSANDBOX"), guard, os.Executable, exec.LookPath)
	if err != nil {
		return err
	}

	// The environment passes through whole. The HERDR_* variables must survive:
	// the sandbox launch re-reads HERDR_ENV to decide whether to start the
	// filtered proxy, and the in-sandbox integration reports against
	// HERDR_PANE_ID.
	return syscall.Exec(inv.Path, inv.Argv, os.Environ())
}
