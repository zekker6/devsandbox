package shellwrap

import (
	"fmt"
	"slices"
	"strings"

	"devsandbox/internal/agentid"
)

// BypassSuffix names the function generated beside every wrapper that runs the
// host command directly: `npm` gets `npm-no-ds`.
const BypassSuffix = "-no-ds"

// reservedCommandNames are words a wrapper function must not shadow in any
// supported shell: syntax keywords a function cannot be named after or that
// would break parsing, and the builtins the generated snippet itself calls.
var reservedCommandNames = []string{
	"_", "alias", "and", "argparse", "begin", "break", "builtin", "case", "command",
	"continue", "coproc", "declare", "do", "done", "elif", "else", "end", "esac",
	"eval", "exec", "export", "fi", "float", "for", "foreach", "function",
	"functions", "if", "in", "integer", "local", "nocorrect", "noglob", "not", "or",
	"printf", "read", "readonly", "repeat", "return", "select", "set", "source",
	"status", "string", "switch", "test", "then", "time", "type", "typeset",
	"unalias", "unset", "until", "while",
}

// ValidateCommandName reports whether name may be wrapped as a custom command
// routed through `devsandbox run-command`. Config validation and snippet
// generation both call it, so a name the config accepts is one the generator can
// emit.
//
// devsandbox and agent names are compared case-insensitively: on a
// case-insensitive filesystem `Claude` resolves to the claude binary, and
// routing it through run-command would skip the resume and worktree guards
// run-agent applies.
func ValidateCommandName(name string) error {
	if strings.ContainsRune(name, '/') {
		return fmt.Errorf("invalid command name %q: must be a bare command name, not a path", name)
	}
	if err := validateWord("command", name); err != nil {
		return err
	}
	if strings.HasSuffix(name, BypassSuffix) {
		return fmt.Errorf("invalid command name %q: must not end in %q, which is reserved for the host bypass functions", name, BypassSuffix)
	}
	if strings.EqualFold(name, "devsandbox") {
		return fmt.Errorf("invalid command name %q: devsandbox itself cannot be wrapped", name)
	}
	if slices.Contains(reservedCommandNames, name) {
		return fmt.Errorf("invalid command name %q: reserved shell word", name)
	}
	for _, agent := range agentid.KnownAgents() {
		if strings.EqualFold(name, agent) {
			return fmt.Errorf("invalid command name %q: %s is a supported agent and is wrapped through agent selection, not shell_wrappers.commands", name, agent)
		}
	}
	return nil
}

// validateWord rejects anything that is not a bare shell-safe word. The
// generated text is executed by a shell, so the charset is pinned rather than
// assumed. kind names the value in the error.
func validateWord(kind, name string) error {
	if name == "" {
		return fmt.Errorf("empty %s name", kind)
	}
	if name[0] == '-' {
		return fmt.Errorf("invalid %s name %q: must not start with %q", kind, name, "-")
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return fmt.Errorf("invalid %s name %q: only [A-Za-z0-9_-] is allowed", kind, name)
		}
	}
	return nil
}
