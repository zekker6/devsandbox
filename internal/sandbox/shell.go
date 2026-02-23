package sandbox

import (
	"fmt"
	"strings"
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

// shellQuote quotes a string for safe use in a shell command.
// Returns the string unchanged if it's safe, otherwise wraps in single quotes.
func shellQuote(s string) string {
	// If the string is empty, return quoted empty string
	if s == "" {
		return "''"
	}

	// Characters that require quoting in shell
	needsQuoting := false
	for _, c := range s {
		switch c {
		case ' ', '\t', '\n', '"', '\'', '`', '$', '\\', '!', '*', '?', '[', ']', '(', ')', '{', '}', '<', '>', '|', '&', ';', '#', '~':
			needsQuoting = true
		}
	}

	if !needsQuoting {
		return s
	}

	// Use single quotes and escape any single quotes within
	// In shell, 'foo'\''bar' produces foo'bar
	escaped := strings.ReplaceAll(s, "'", "'\\''")
	return "'" + escaped + "'"
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

	cmdString := shellJoinArgs(args)
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

	cmdString := shellJoinArgs(args)
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

	cmdString := shellJoinArgs(args)
	zshCmd := miseActivation + "; " + cmdString
	return []string{cfg.ShellPath, "-c", zshCmd}
}

// shellJoinArgs joins arguments with proper shell quoting.
func shellJoinArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuote(arg)
	}
	return strings.Join(quoted, " ")
}
