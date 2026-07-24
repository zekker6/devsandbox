// Package agentid maps a command invocation to the canonical name of an AI
// agent devsandbox knows how to launch, and recognizes the argv shape herdr
// uses to resume that agent's native session.
//
// The identity is always derived host-side from what devsandbox was asked to
// run - never from anything the sandbox reports - so the package stays a
// dependency-free leaf that the shell-wrapper generator and the run-agent
// entrypoint can import without pulling in the tool registry.
package agentid

import (
	"path/filepath"
	"strings"
)

type agent struct {
	name string
	// resumeFlag is the flag herdr v0.7.4 compiles into the command it types
	// into a pane to resume this agent's native session. Empty for an agent
	// that resumes through a subcommand instead.
	resumeFlag string
	// resumeSubcommand is the first positional argument herdr passes instead of
	// a resume flag. Empty for an agent that resumes through a flag.
	resumeSubcommand string
	// resumeAliases are the agent's own flags that reopen a prior session
	// without herdr's involvement. herdr never types these, but the wrapper
	// intercepts everything the user types and they reach the same session
	// store, so the worktree guard has to recognize them or they silently
	// begin a fresh session in the wrong sandbox.
	resumeAliases []string
}

// agents is the set of agents this build recognizes.
//
// resumeFlag/resumeSubcommand hold the resume shape herdr v0.7.4 compiles in
// (read out of `herdr::agent_resume::plan`: source "herdr:codex" builds the argv
// ["codex", "resume", <id>], where the others build ["<agent>", "<flag>", <id>]).
// herdr knows only claude/pi/codex, so opencode and copilot carry no herdr
// resume flag; their resume verbs live in resumeAliases, which the worktree
// guard consults for the resumes a user types by hand.
var agents = []agent{
	// claude's -c/--continue reopens the most recent conversation for the
	// current project, which is the same store --resume reaches.
	{name: "claude", resumeFlag: "--resume", resumeAliases: []string{"--continue", "-c"}},
	{name: "pi", resumeFlag: "--session"},
	{name: "codex", resumeSubcommand: "resume"},
	{name: "opencode", resumeAliases: []string{"--continue", "-c", "--session", "-s"}},
	{name: "copilot", resumeAliases: []string{"--resume", "-r", "--continue"}},
}

// KnownAgents returns every agent this build recognizes, in a stable order.
func KnownAgents() []string {
	names := make([]string, 0, len(agents))
	for _, a := range agents {
		names = append(names, a.name)
	}
	return names
}

// CanonicalAgent maps argv to a known agent name, or "" when none matches.
//
// Only filepath.Base(argv[0]) is considered: never a substring of it, and never
// a later argument, so `sh -c "claude --resume x"` is not claude.
func CanonicalAgent(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	name := filepath.Base(argv[0])
	if _, ok := lookup(name); !ok {
		return ""
	}
	return name
}

// IsResumeInvocation reports whether args reopen the given agent's native
// session: the shape herdr types, plus the agent's own equivalents a user may
// type by hand. args excludes the agent name itself.
func IsResumeInvocation(agent string, args []string) bool {
	a, ok := lookup(agent)
	if !ok {
		return false
	}
	if a.resumeSubcommand != "" {
		// A subcommand is only a subcommand in first position: anywhere else it
		// is a value belonging to a preceding flag, or a prompt.
		return len(args) > 0 && args[0] == a.resumeSubcommand
	}
	flags := a.resumeAliases
	if a.resumeFlag != "" {
		flags = append([]string{a.resumeFlag}, flags...)
	}
	if len(flags) == 0 {
		return false
	}
	for _, arg := range args {
		// Everything past "--" is a positional argument for the agent, not a
		// flag herdr could have placed there.
		if arg == "--" {
			return false
		}
		for _, f := range flags {
			if arg == f || strings.HasPrefix(arg, f+"=") {
				return true
			}
		}
	}
	return false
}

func lookup(name string) (agent, bool) {
	for _, a := range agents {
		if a.name == name {
			return a, true
		}
	}
	return agent{}, false
}
