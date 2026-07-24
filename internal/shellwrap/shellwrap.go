// Package shellwrap generates the host shell snippets that make supported AI
// agents run inside devsandbox by default: typing `claude` runs
// `devsandbox run-agent claude`, while `claude-no-ds` and `command claude`
// still reach the real binary.
//
// Nothing is written to disk. `devsandbox agent-wrappers activate <shell>`
// prints the snippet and the user evaluates it from their own startup file, the
// way `mise activate` works. That makes the snippet a function of the machine as
// it is at shell start rather than of the machine as it was at install time: an
// upgrade that moves the devsandbox binary, or a newly installed agent, is
// picked up by the next shell instead of leaving a stale file behind.
//
// The generated snippet is evaluated inside the sandbox too - the sandbox binds
// fish's config directory and bash/zsh rc files - so every definition is wrapped
// in a guard on DEVSANDBOX, the marker the sandbox builder sets. ActivateLine
// carries the same guard, so the binary is not even invoked in there. The guard
// uses non-empty semantics in every shell: an empty DEVSANDBOX means "outside
// the sandbox" everywhere, matching what the run-agent entrypoint checks in Go.
//
// The package is a stdlib-only leaf so both the wrapper CLI and run-agent can
// import it without dragging in the tool registry.
package shellwrap

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Supported shell names.
const (
	ShellFish = "fish"
	ShellBash = "bash"
	ShellZsh  = "zsh"
)

// supportedShells is the single list both SupportedShells and IsSupportedShell
// answer from, so a shell can never be generatable but unlisted.
var supportedShells = []string{ShellFish, ShellBash, ShellZsh}

// SupportedShells returns the shells a snippet can be generated for, in a
// stable order.
func SupportedShells() []string {
	return slices.Clone(supportedShells)
}

// IsSupportedShell reports whether Snippet can generate for shell.
func IsSupportedShell(shell string) bool {
	return slices.Contains(supportedShells, shell)
}

// Snippet returns the wrapper definitions for shell.
//
// devsandboxPath must be devsandbox's absolute path, resolved by the process
// generating the snippet. A herdr pane shell may be a login shell whose PATH
// differs from an interactive one - mise shims being the common case - and a
// bare `devsandbox` would then fail with command-not-found in exactly the
// situation the wrapper exists to serve.
//
// The path is checked before it is used, and a missing binary fails closed
// rather than falling back to a PATH lookup: that would hand the host shell
// whatever binary PATH names first, and a project-local bin directory on PATH is
// writable by the sandbox that devsandbox is meant to contain. `run-agent`
// refuses a PATH lookup for the same reason; the snippet must not reintroduce
// one at the shell layer. Because activation regenerates the snippet on every
// shell start, the path is only stale within a session that outlived an upgrade,
// and a new shell fixes it - which is what the diagnostic says.
//
// That diagnostic goes through printf, not echo: zsh's echo expands backslash
// escapes by default and bash's does under xpg_echo, so a path containing `\c`
// would truncate the message before the reinstall command it exists to name.
// printf expands escapes in its format string only, never in a %s argument.
func Snippet(shell, devsandboxPath string, agents []string) (string, error) {
	if !IsSupportedShell(shell) {
		return "", fmt.Errorf("unsupported shell %q: supported shells are %s",
			shell, strings.Join(SupportedShells(), ", "))
	}
	if !filepath.IsAbs(devsandboxPath) {
		return "", fmt.Errorf("devsandbox path %q must be absolute", devsandboxPath)
	}
	if len(agents) == 0 {
		return "", fmt.Errorf("no agents to wrap")
	}
	for _, a := range agents {
		if err := validateAgentName(a); err != nil {
			return "", err
		}
	}

	if shell == ShellFish {
		return fishSnippet(devsandboxPath, agents), nil
	}
	return posixSnippet(devsandboxPath, agents), nil
}

func fishSnippet(devsandboxPath string, agents []string) string {
	var b strings.Builder
	b.WriteString("if test -z \"$DEVSANDBOX\"\n")
	q := fishQuote(devsandboxPath)
	for _, a := range agents {
		fmt.Fprintf(&b, "    function %s --wraps %s\n", a, a)
		fmt.Fprintf(&b, "        if test -x %s\n", q)
		fmt.Fprintf(&b, "            %s run-agent %s $argv\n", q, a)
		b.WriteString("        else\n")
		fmt.Fprintf(&b, "            printf '%%s %%s %%s\\n' \"devsandbox: no executable at\" %s \"- reinstall devsandbox, then start a new shell to refresh the wrappers\" >&2\n", q)
		b.WriteString("            return 127\n")
		b.WriteString("        end\n")
		b.WriteString("    end\n")
		fmt.Fprintf(&b, "    function %s-no-ds --wraps %s\n", a, a)
		fmt.Fprintf(&b, "        command %s $argv\n", a)
		b.WriteString("    end\n")
	}
	b.WriteString("end\n")
	return b.String()
}

func posixSnippet(devsandboxPath string, agents []string) string {
	var b strings.Builder
	b.WriteString("if [ -n \"${DEVSANDBOX:-}\" ]; then :; else\n")
	q := posixQuote(devsandboxPath)
	for _, a := range agents {
		fmt.Fprintf(&b, "  %s() { if [ -x %s ]; then %s run-agent %s \"$@\"; else printf '%%s %%s %%s\\n' \"devsandbox: no executable at\" %s \"- reinstall devsandbox, then start a new shell to refresh the wrappers\" >&2; return 127; fi; }\n", a, q, q, a, q)
		fmt.Fprintf(&b, "  %s-no-ds() { command %s \"$@\"; }\n", a, a)
	}
	b.WriteString("fi\n")
	return b.String()
}

// ActivateCommand is the command a startup file runs to obtain the snippet. It
// is resolved through PATH on purpose: it runs once per shell start, and baking
// in an absolute path would leave a line that errors on every shell start after
// an upgrade moved the binary (`mise use -g` installs into a version-scoped
// directory). The snippet that command emits still bakes in the absolute path it
// resolved for itself, so no agent invocation goes through PATH.
const ActivateCommand = "devsandbox agent-wrappers activate"

// ActivateLine returns the line the user adds to their startup file. It is empty
// for an unsupported shell.
//
// The line is guarded on DEVSANDBOX, not only because the snippet it evaluates
// is: the sandbox binds the startup files, and devsandbox itself need not exist
// in there, so an unguarded line would fail with command-not-found on every
// in-sandbox shell start.
func ActivateLine(shell string) string {
	switch shell {
	case ShellFish:
		return fmt.Sprintf("if test -z \"$DEVSANDBOX\"; %s fish | source; end", ActivateCommand)
	case ShellBash, ShellZsh:
		return fmt.Sprintf("if [ -z \"${DEVSANDBOX:-}\" ]; then eval \"$(%s %s)\"; fi", ActivateCommand, shell)
	}
	return ""
}

// StartupFile names the file ActivateLine belongs in, for use in help text. It
// is the conventional location, not a resolved path: devsandbox never reads or
// writes it.
func StartupFile(shell string) string {
	switch shell {
	case ShellFish:
		return "~/.config/fish/config.fish"
	case ShellBash:
		return "~/.bashrc"
	case ShellZsh:
		return "~/.zshrc"
	}
	return ""
}

// validateAgentName rejects anything that is not a bare shell-safe word. Agent
// names are host-derived, but the generated text is executed by a shell, so
// the charset is pinned rather than assumed.
func validateAgentName(name string) error {
	if name == "" {
		return fmt.Errorf("empty agent name")
	}
	if name[0] == '-' {
		return fmt.Errorf("invalid agent name %q: must not start with %q", name, "-")
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return fmt.Errorf("invalid agent name %q: only [A-Za-z0-9_-] is allowed", name)
		}
	}
	return nil
}

// posixQuote single-quotes s for bash, zsh, and any POSIX shell.
func posixQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// fishQuote single-quotes s for fish, where only backslash and the quote
// itself are escapable inside single quotes.
func fishQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	return "'" + r.Replace(s) + "'"
}
