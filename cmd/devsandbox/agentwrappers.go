package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"devsandbox/internal/agentid"
	"devsandbox/internal/shellwrap"
)

// wrapperState describes an installed snippet relative to the one devsandbox
// would generate now.
type wrapperState int

const (
	// wrapperAbsent means no file exists at the install path.
	wrapperAbsent wrapperState = iota
	// wrapperCurrent means the file matches what would be generated now.
	wrapperCurrent
	// wrapperStale means devsandbox generated the file, but its content differs
	// (a devsandbox upgrade, a moved binary, or a newly installed agent).
	wrapperStale
	// wrapperForeign means the file lacks the generated header, so devsandbox
	// did not write it and must not touch it.
	wrapperForeign
)

func (s wrapperState) String() string {
	switch s {
	case wrapperAbsent:
		return "not installed"
	case wrapperCurrent:
		return "installed (current)"
	case wrapperStale:
		return "installed (out of date)"
	case wrapperForeign:
		return "not written by devsandbox"
	}
	return "unknown"
}

// wrapperEnv is everything the three subcommands need, resolved once so each
// operation is a pure function of it and therefore testable without touching
// the real home directory or PATH.
type wrapperEnv struct {
	// shell is a supported shell name, never a path.
	shell string
	// homeDir is the host home the install path is derived from.
	homeDir string
	// devsandboxPath is devsandbox's absolute path, baked into the snippet so a
	// pane shell with a different PATH still resolves it.
	devsandboxPath string
	// agents are the known agents actually installed on this host.
	agents []string
	// getenv reads the host environment (injected for tests).
	getenv func(string) string
	out    io.Writer
}

// printf writes user-facing output. A write error on the command's own output
// stream is not actionable - the operation it describes already happened.
func (e wrapperEnv) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(e.out, format, args...)
}

func newAgentWrappersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent-wrappers",
		Short: "Manage shell wrappers that run supported AI agents inside devsandbox",
		Long: `Manage shell wrappers that run supported AI agents inside devsandbox.

Once installed, typing ` + "`claude`" + ` runs ` + "`devsandbox claude`" + ` in the current
directory. Two escape hatches always reach the real, unsandboxed binary:
` + "`claude-no-ds`" + ` and the shell builtin ` + "`command claude`" + `.

Nothing is installed automatically. For bash and zsh the snippet is written to a
standalone file and the ` + "`source`" + ` line is printed for you to add - no rc file is
ever edited by devsandbox. fish gets a conf.d drop-in, so no existing file is
touched there either.

The wrappers are independent of herdr: they are useful on their own, and herdr's
native session restore builds on them.`,
	}

	cmd.AddCommand(newAgentWrappersInstallCmd())
	cmd.AddCommand(newAgentWrappersStatusCmd())
	cmd.AddCommand(newAgentWrappersUninstallCmd())

	return cmd
}

func newAgentWrappersInstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the wrapper snippet for your shell",
		Long: `Generate and install the wrapper snippet for your shell.

Only agents that are actually installed on this host are wrapped. Installing
again after adding an agent, or after moving the devsandbox binary, refreshes
the snippet.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			shellFlag, _ := cmd.Flags().GetString("shell")
			env, err := resolveWrapperEnv(shellFlag, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return installWrappers(env)
		},
	}
	addWrapperShellFlag(cmd)
	return cmd
}

func newAgentWrappersStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "status",
		Short:        "Show whether the wrappers are installed and current",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			shellFlag, _ := cmd.Flags().GetString("shell")
			env, err := resolveWrapperEnv(shellFlag, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return statusWrappers(env)
		},
	}
	addWrapperShellFlag(cmd)
	return cmd
}

func newAgentWrappersUninstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the wrapper snippet devsandbox installed",
		Long: `Remove the wrapper snippet devsandbox installed.

Only a file carrying the generated header is removed. For bash and zsh the
` + "`source`" + ` line you added is printed so you can remove it yourself.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			shellFlag, _ := cmd.Flags().GetString("shell")
			env, err := resolveWrapperEnv(shellFlag, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return uninstallWrappers(env)
		},
	}
	addWrapperShellFlag(cmd)
	return cmd
}

func addWrapperShellFlag(cmd *cobra.Command) {
	cmd.Flags().String("shell", "", fmt.Sprintf("target shell (%s); defaults to $SHELL",
		strings.Join(shellwrap.SupportedShells(), ", ")))
}

// resolveWrapperEnv gathers the host facts the subcommands operate on.
func resolveWrapperEnv(shellFlag string, out io.Writer) (wrapperEnv, error) {
	shell, err := detectShell(shellFlag, os.Getenv("SHELL"))
	if err != nil {
		return wrapperEnv{}, err
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return wrapperEnv{}, fmt.Errorf("resolve home directory: %w", err)
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
		homeDir:        homeDir,
		devsandboxPath: exe,
		agents:         installedAgents(agentid.KnownAgents(), exec.LookPath),
		getenv:         os.Getenv,
		out:            out,
	}, nil
}

// detectShell resolves the shell to generate for. The flag wins; otherwise
// $SHELL is used, which is a path, so only its base name is meaningful. A login
// shell's argv[0] convention of a leading "-" is tolerated.
func detectShell(shellFlag, shellEnv string) (string, error) {
	name := shellFlag
	source := "--shell"
	if name == "" {
		name = shellEnv
		source = "$SHELL"
	}
	if name == "" {
		return "", fmt.Errorf("cannot detect your shell: $SHELL is unset; pass --shell (%s)",
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

// inspectSnippet classifies the file at path against the snippet that would be
// generated now, returning the content it read so a caller that needs both does
// not race a second read against an edit in between.
func inspectSnippet(path, want string) (wrapperState, string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return wrapperAbsent, "", nil
	}
	if err != nil {
		return wrapperAbsent, "", fmt.Errorf("read %s: %w", path, err)
	}
	content := string(data)
	if !strings.HasPrefix(content, shellwrap.Header) {
		return wrapperForeign, content, nil
	}
	if content == want {
		return wrapperCurrent, content, nil
	}
	return wrapperStale, content, nil
}

// wrappedAgents lists the agents an installed snippet wraps, read back from the
// file rather than assumed, so status describes what is really in effect.
func wrappedAgents(content string) []string {
	var agents []string
	seen := make(map[string]struct{})
	for line := range strings.SplitSeq(content, "\n") {
		_, rest, ok := strings.Cut(line, " run-agent ")
		if !ok {
			continue
		}
		name := strings.Fields(rest)
		if len(name) == 0 {
			continue
		}
		if _, dup := seen[name[0]]; dup {
			continue
		}
		seen[name[0]] = struct{}{}
		agents = append(agents, name[0])
	}
	return agents
}

func installWrappers(env wrapperEnv) error {
	if len(env.agents) == 0 {
		return fmt.Errorf("none of the supported agents (%s) are installed on this host",
			strings.Join(agentid.KnownAgents(), ", "))
	}

	snippet, err := shellwrap.Snippet(env.shell, env.devsandboxPath, env.agents)
	if err != nil {
		return err
	}
	path := shellwrap.InstallPath(env.shell, env.homeDir)

	state, _, err := inspectSnippet(path, snippet)
	if err != nil {
		return err
	}
	if state == wrapperForeign {
		return fmt.Errorf("refusing to overwrite %s: it does not carry the devsandbox header, so devsandbox did not write it", path)
	}

	if state == wrapperCurrent {
		env.printf("Already up to date: %s\n", path)
	} else {
		if err := writeSnippet(path, snippet); err != nil {
			return err
		}
		env.printf("Installed %s\n", path)
	}
	env.printf("Wrapped agents: %s\n", strings.Join(env.agents, ", "))

	if line := shellwrap.SourceLine(env.shell, path); line != "" {
		env.printf("\nAdd this line to your %s startup file (devsandbox never edits one):\n\n  %s\n",
			env.shell, line)
	} else {
		env.printf("\nOpen a new %s shell to pick it up.\n", env.shell)
	}
	env.printf("\nEscape hatches: `<agent>-no-ds` and `command <agent>` run the real binary unsandboxed.\n")
	return nil
}

// writeSnippet writes the snippet atomically. It is world-readable because a
// shell sources it, and it contains no secrets - only a path and agent names.
func writeSnippet(path, snippet string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(snippet), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("commit %s: %w", path, err)
	}
	return nil
}

func statusWrappers(env wrapperEnv) error {
	path := shellwrap.InstallPath(env.shell, env.homeDir)
	env.printf("Shell:   %s\n", env.shell)
	env.printf("Snippet: %s\n", path)

	// With no agent installed there is nothing to generate, so an installed
	// snippet compares against the empty string and reports as out of date -
	// which it is, since installing now would wrap nothing.
	want := ""
	if len(env.agents) > 0 {
		snippet, err := shellwrap.Snippet(env.shell, env.devsandboxPath, env.agents)
		if err != nil {
			return err
		}
		want = snippet
	}
	state, content, err := inspectSnippet(path, want)
	if err != nil {
		return err
	}
	env.printf("Status:  %s\n", state)

	switch state {
	case wrapperAbsent:
		env.printf("Agents:  none wrapped (would wrap: %s)\n", joinOrNone(env.agents))
	case wrapperForeign:
		env.printf("Agents:  unknown - devsandbox will not read a file it did not write\n")
	default:
		env.printf("Agents:  %s\n", joinOrNone(wrappedAgents(content)))
		if state == wrapperStale {
			env.printf("         run `devsandbox agent-wrappers install` to refresh (would wrap: %s)\n",
				joinOrNone(env.agents))
		}
	}

	if line := shellwrap.SourceLine(env.shell, path); line != "" {
		sourcing := rcFilesSourcing(env.homeDir, path)
		if len(sourcing) == 0 {
			env.printf("Sourced: no startup file references it; add this line yourself:\n\n  %s\n", line)
		} else {
			env.printf("Sourced: %s\n", strings.Join(sourcing, ", "))
		}
	}

	reportHerdrPaneShell(env)
	return nil
}

// reportHerdrPaneShell warns when herdr starts its panes with a shell that has
// no snippet. That is the silent failure mode of the restore feature: herdr
// types the resume command into a pane shell that does not intercept it, and
// the agent runs unsandboxed against a session it cannot see.
func reportHerdrPaneShell(env wrapperEnv) {
	paneShell, source := herdrPaneShell(env.homeDir, env.getenv)
	if paneShell == "" {
		return
	}
	env.printf("\nherdr pane shell: %s (from %s)\n", paneShell, source)
	if !shellwrap.IsSupportedShell(paneShell) {
		env.printf("  warning: devsandbox generates no wrapper snippet for %s, so a herdr\n"+
			"           session restore would run the agent unsandboxed.\n", paneShell)
		return
	}
	panePath := shellwrap.InstallPath(paneShell, env.homeDir)
	if _, err := os.Stat(panePath); err != nil {
		env.printf("  warning: no snippet at %s, so a herdr session restore would run\n"+
			"           the agent unsandboxed. Run `devsandbox agent-wrappers install --shell %s`.\n",
			panePath, paneShell)
	}
}

// shellStartupFiles are the files a bash or zsh user might add the source line
// to. Checking all of them keeps status honest when the line lives in a profile
// rather than an rc file.
var shellStartupFiles = []string{
	".bashrc", ".bash_profile", ".profile", ".zshrc", ".zshenv",
}

// rcFilesSourcing returns the startup files that mention the snippet path.
func rcFilesSourcing(homeDir, snippetPath string) []string {
	var found []string
	for _, name := range shellStartupFiles {
		p := filepath.Join(homeDir, name)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), snippetPath) {
			found = append(found, p)
		}
	}
	return found
}

// herdrPaneShell returns the shell herdr starts new interactive panes with, and
// where that answer came from. herdr resolves an empty `[terminal] default_shell`
// to $SHELL, so this does too.
func herdrPaneShell(homeDir string, getenv func(string) string) (shell, source string) {
	path := herdrConfigPath(homeDir, getenv)
	var cfg struct {
		Terminal struct {
			DefaultShell string `toml:"default_shell"`
		} `toml:"terminal"`
	}
	// A malformed config is not an error here: herdr itself falls back to its
	// defaults, so reporting the fallback is the accurate answer.
	if _, err := toml.DecodeFile(path, &cfg); err == nil && cfg.Terminal.DefaultShell != "" {
		return strings.TrimPrefix(filepath.Base(cfg.Terminal.DefaultShell), "-"), path
	}
	if s := getenv("SHELL"); s != "" {
		return strings.TrimPrefix(filepath.Base(s), "-"), "$SHELL"
	}
	return "", ""
}

// herdrConfigPath mirrors herdr's own resolution order: HERDR_CONFIG_PATH, then
// $XDG_CONFIG_HOME/herdr/config.toml, then ~/.config/herdr/config.toml.
func herdrConfigPath(homeDir string, getenv func(string) string) string {
	if p := getenv("HERDR_CONFIG_PATH"); p != "" {
		return p
	}
	config := getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(config) {
		config = filepath.Join(homeDir, ".config")
	}
	return filepath.Join(config, "herdr", "config.toml")
}

func uninstallWrappers(env wrapperEnv) error {
	path := shellwrap.InstallPath(env.shell, env.homeDir)

	// The snippet compared against is irrelevant here: uninstall cares only
	// about absent vs. foreign vs. ours.
	state, _, err := inspectSnippet(path, "")
	if err != nil {
		return err
	}
	if state == wrapperAbsent {
		env.printf("Nothing to remove: %s does not exist\n", path)
		return nil
	}
	if state == wrapperForeign {
		return fmt.Errorf("refusing to remove %s: it does not carry the devsandbox header, so devsandbox did not write it", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	env.printf("Removed %s\n", path)

	if line := shellwrap.SourceLine(env.shell, path); line != "" {
		env.printf("\nRemove this line from your %s startup file:\n\n  %s\n", env.shell, line)
	}
	return nil
}

func joinOrNone(items []string) string {
	if len(items) == 0 {
		return "none"
	}
	return strings.Join(items, ", ")
}
