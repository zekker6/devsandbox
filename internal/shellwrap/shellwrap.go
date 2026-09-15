// Package shellwrap generates the host shell snippets that make supported AI
// agents and configured commands run inside devsandbox by default: typing
// `claude` runs `devsandbox run-agent claude` and a configured `npm` runs
// `devsandbox run-command npm`, while `claude-no-ds` and `command claude` still
// reach the real binary.
//
// Nothing is written to disk. `devsandbox shell-wrappers activate <shell>`
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
// The package depends only on the stdlib and the dependency-free
// internal/agentid, so the wrapper CLI, run-agent and config validation can
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
//
// agents route through `run-agent`, which keeps the resume and worktree guards;
// commands are configured custom names and route through `run-command`. Either
// list may be empty, and duplicates within a list are emitted once.
//
// Each snippet replaces the previous one. It records the public names it defines
// in a shell variable, and the next snippet removes exactly those functions and
// their bypasses before defining its own, so re-sourcing after a config change
// drops wrappers the config no longer names. Nothing unrecorded is ever removed.
// The record is never exported: a child shell has none of the functions, so an
// inherited record could only name functions its own startup files defined.
func Snippet(shell, devsandboxPath string, agents, commands []string) (string, error) {
	if !IsSupportedShell(shell) {
		return "", fmt.Errorf("unsupported shell %q: supported shells are %s",
			shell, strings.Join(SupportedShells(), ", "))
	}
	if !filepath.IsAbs(devsandboxPath) {
		return "", fmt.Errorf("devsandbox path %q must be absolute", devsandboxPath)
	}
	wrappers, err := wrapperSet(agents, commands)
	if err != nil {
		return "", err
	}

	if shell == ShellFish {
		return fishSnippet(devsandboxPath, wrappers), nil
	}
	return posixSnippet(devsandboxPath, wrappers), nil
}

// snapshotVar records the public names the last evaluated snippet defined.
const snapshotVar = "__devsandbox_wrappers"

// wrapper is one generated public function and the devsandbox subcommand it
// routes through.
type wrapper struct {
	name       string
	subcommand string
}

// wrapperSet validates both lists and returns the wrappers in emission order:
// agents, then commands, each deduplicated in first-seen order.
//
// ValidateCommandName reserves the bypass suffix and agent names, but agent
// names are only charset-checked, so the complete namespace of public and
// bypass functions is checked as well: two wrappers generating one function
// would leave whichever is defined last in charge of the other's name.
func wrapperSet(agents, commands []string) ([]wrapper, error) {
	var wrappers []wrapper
	add := func(subcommand string, names []string, validate func(string) error) error {
		seen := make(map[string]bool, len(names))
		for _, name := range names {
			if err := validate(name); err != nil {
				return err
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			wrappers = append(wrappers, wrapper{name: name, subcommand: subcommand})
		}
		return nil
	}
	if err := add("run-agent", agents, validateAgentName); err != nil {
		return nil, err
	}
	if err := add("run-command", commands, ValidateCommandName); err != nil {
		return nil, err
	}

	owners := make(map[string]wrapper, 2*len(wrappers))
	for _, w := range wrappers {
		for _, fn := range []string{w.name, w.name + BypassSuffix} {
			if prev, ok := owners[fn]; ok {
				return nil, fmt.Errorf("wrapper function %q is generated for both %s %q and %s %q",
					fn, prev.subcommand, prev.name, w.subcommand, w.name)
			}
			owners[fn] = w
		}
	}
	return wrappers, nil
}

func fishSnippet(devsandboxPath string, wrappers []wrapper) string {
	var b strings.Builder
	b.WriteString("if test -z \"$DEVSANDBOX\"\n")
	fmt.Fprintf(&b, "    for __devsandbox_name in $%s\n", snapshotVar)
	fmt.Fprintf(&b, "        functions -e $__devsandbox_name $__devsandbox_name%s\n", BypassSuffix)
	b.WriteString("    end\n")
	b.WriteString("    set -e __devsandbox_name\n")
	q := fishQuote(devsandboxPath)
	for _, w := range wrappers {
		fmt.Fprintf(&b, "    function %s --wraps %s\n", w.name, w.name)
		fmt.Fprintf(&b, "        if test -x %s\n", q)
		fmt.Fprintf(&b, "            %s %s %s $argv\n", q, w.subcommand, w.name)
		b.WriteString("        else\n")
		fmt.Fprintf(&b, "            printf '%%s %%s %%s\\n' \"devsandbox: no executable at\" %s \"- reinstall devsandbox, then start a new shell to refresh the wrappers\" >&2\n", q)
		b.WriteString("            return 127\n")
		b.WriteString("        end\n")
		b.WriteString("    end\n")
		fmt.Fprintf(&b, "    function %s%s --wraps %s\n", w.name, BypassSuffix, w.name)
		fmt.Fprintf(&b, "        command %s $argv\n", w.name)
		b.WriteString("    end\n")
	}
	// -u unexports a record inherited from the environment as well.
	b.WriteString("    set -gu " + snapshotVar)
	for _, w := range wrappers {
		b.WriteString(" " + w.name)
	}
	b.WriteString("\nend\n")
	return b.String()
}

// posixSnippet generates for bash and zsh.
//
// The record is walked with parameter expansion rather than word splitting:
// zsh does not split an unquoted expansion, and bash splits on a user-settable
// IFS. It is unset before being reassigned, which drops an export attribute an
// inherited value carries, and assigned with allexport off: under `set -a` the
// assignment would export it again, and zsh does not export the functions with
// it, so a child shell would inherit a record naming functions it never had.
//
// Definitions use the function keyword because both shells alias-expand the
// name in `name() {`, so an alias such as `alias npm=pnpm` would make the whole
// snippet a syntax error.
func posixSnippet(devsandboxPath string, wrappers []wrapper) string {
	var b strings.Builder
	b.WriteString("if [ -n \"${DEVSANDBOX:-}\" ]; then :; else\n")
	fmt.Fprintf(&b, "  __devsandbox_rest=${%s-}\n", snapshotVar)
	fmt.Fprintf(&b, "  unset %s\n", snapshotVar)
	fmt.Fprintf(&b, "  while [ -n \"$__devsandbox_rest\" ]; do __devsandbox_name=${__devsandbox_rest%%%% *}; __devsandbox_rest=${__devsandbox_rest#\"$__devsandbox_name\"}; __devsandbox_rest=${__devsandbox_rest# }; unset -f \"$__devsandbox_name\" \"${__devsandbox_name}%s\" 2>/dev/null; done\n", BypassSuffix)
	b.WriteString("  unset __devsandbox_rest __devsandbox_name\n")
	q := posixQuote(devsandboxPath)
	names := make([]string, 0, len(wrappers))
	for _, w := range wrappers {
		fmt.Fprintf(&b, "  function %s { if [ -x %s ]; then %s %s %s \"$@\"; else printf '%%s %%s %%s\\n' \"devsandbox: no executable at\" %s \"- reinstall devsandbox, then start a new shell to refresh the wrappers\" >&2; return 127; fi; }\n", w.name, q, q, w.subcommand, w.name, q)
		fmt.Fprintf(&b, "  function %s%s { command %s \"$@\"; }\n", w.name, BypassSuffix, w.name)
		names = append(names, w.name)
	}
	record := snapshotVar + "=" + posixQuote(strings.Join(names, " "))
	fmt.Fprintf(&b, "  case $- in *a*) set +a; %s; set -a ;; *) %s ;; esac\n", record, record)
	b.WriteString("fi\n")
	return b.String()
}

// ActivateCommand is the command a startup file runs to obtain the snippet. It
// is resolved through PATH on purpose: it runs once per shell start, and baking
// in an absolute path would leave a line that errors on every shell start after
// an upgrade moved the binary (`mise use -g` installs into a version-scoped
// directory). The snippet that command emits still bakes in the absolute path it
// resolved for itself, so no agent invocation goes through PATH.
const ActivateCommand = "devsandbox shell-wrappers activate"

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
// names are host-derived, but the generated text is executed by a shell.
func validateAgentName(name string) error {
	return validateWord("agent", name)
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
