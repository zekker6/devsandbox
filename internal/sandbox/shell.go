package sandbox

import (
	"fmt"
	"strings"

	"devsandbox/internal/shellwrap"
)

// escapeForShellDoubleQuote escapes a string for safe inclusion inside
// double-quoted strings in bash/zsh. Escapes \, $, ", and `.
func escapeForShellDoubleQuote(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`$`, `\$`,
		`"`, `\"`,
		"`", "\\`",
	)
	return replacer.Replace(s)
}

// escapeForFishDoubleQuote escapes a string for safe inclusion inside
// double-quoted strings in fish. Escapes \, $, and ".
// Note: fish only recognizes \\, \$, \" and \newline inside double quotes;
// other \x sequences are kept literal, so we must not escape backticks here.
func escapeForFishDoubleQuote(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`$`, `\$`,
		`"`, `\"`,
	)
	return replacer.Replace(s)
}

// shellQuote quotes a string for safe use in a shell command run by shell.
// Returns the string unchanged if it's safe, otherwise wraps in single quotes.
// Fish needs its own quoting: it still treats \\ and \' as escapes inside
// single quotes, so POSIX quoting alters backslashes and a trailing backslash
// leaves the quote unbalanced.
func shellQuote(s string, shell Shell) string {
	// A leading = is zsh's EQUALS expansion (=cmd becomes the path of cmd).
	needsQuoting := s == "" || strings.HasPrefix(s, "=")
	for _, c := range s {
		switch c {
		case ' ', '\t', '\n', '"', '\'', '`', '$', '\\', '!', '*', '?', '[', ']', '(', ')', '{', '}', '<', '>', '|', '&', ';', '#', '~':
			needsQuoting = true
		}
	}

	if !needsQuoting {
		return s
	}
	if shell == ShellFish {
		return shellwrap.FishQuote(s)
	}
	return shellwrap.PosixQuote(s)
}

// BuildShellCommand creates the command to run inside the sandbox
func BuildShellCommand(cfg *Config, args []string) []string {
	switch cfg.Shell {
	case ShellFish:
		return buildFishCommand(cfg, args)
	case ShellZsh:
		return buildZshCommand(cfg, args)
	default:
		return buildBashCommand(cfg, args)
	}
}

func buildFishCommand(cfg *Config, args []string) []string {
	miseActivation := "if command -q mise; mise activate fish | source; end"

	if len(args) == 0 {
		greeting := fmt.Sprintf(`set -gx fish_greeting "🔒 Sandbox: %s | .env blocked | No SSH/git push"`, escapeForFishDoubleQuote(cfg.ProjectName))
		fishInit := miseActivation + "; " + greeting + "; exec fish"
		return []string{cfg.ShellPath, "-c", fishInit}
	}

	cmdString := shellJoinArgs(args, cfg.Shell)
	fishCmd := miseActivation + "; " + cmdString
	return []string{cfg.ShellPath, "-c", fishCmd}
}

func buildBashCommand(cfg *Config, args []string) []string {
	miseActivation := `if command -v mise &>/dev/null; then eval "$(mise activate bash)"; fi`

	if len(args) == 0 {
		// Set PS1 prompt with sandbox indicator
		ps1 := fmt.Sprintf(`PS1="🔒 [%s] \w $ "`, escapeForShellDoubleQuote(cfg.ProjectName))
		bashInit := miseActivation + "; " + ps1 + "; exec bash --norc --noprofile"
		return []string{cfg.ShellPath, "-c", bashInit}
	}

	cmdString := shellJoinArgs(args, cfg.Shell)
	bashCmd := miseActivation + "; " + cmdString
	return []string{cfg.ShellPath, "-c", bashCmd}
}

func buildZshCommand(cfg *Config, args []string) []string {
	miseActivation := `if command -v mise &>/dev/null; then eval "$(mise activate zsh)"; fi`

	if len(args) == 0 {
		// Set PROMPT with sandbox indicator
		prompt := fmt.Sprintf(`PROMPT="🔒 [%s] %%~ $ "`, escapeForShellDoubleQuote(cfg.ProjectName))
		zshInit := miseActivation + "; " + prompt + "; exec zsh --no-rcs"
		return []string{cfg.ShellPath, "-c", zshInit}
	}

	cmdString := shellJoinArgs(args, cfg.Shell)
	zshCmd := miseActivation + "; " + cmdString
	return []string{cfg.ShellPath, "-c", zshCmd}
}

// shellJoinArgs joins arguments with proper shell quoting.
func shellJoinArgs(args []string, shell Shell) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuote(arg, shell)
	}
	return strings.Join(quoted, " ")
}
