package sandbox

import (
	"strings"
)

func BuildShellCommand(cfg *Config, args []string) []string {
	miseActivation := "if command -q mise; mise activate fish | source; end"

	if len(args) == 0 {
		greeting := `set -gx fish_greeting "🔒 Sandbox: $SANDBOX_PROJECT | .env blocked | No SSH/git push"`
		fishInit := miseActivation + "; " + greeting + "; exec fish"
		return []string{"/usr/bin/fish", "-c", fishInit}
	}

	cmdString := strings.Join(args, " ")
	fishCmd := miseActivation + "; " + cmdString
	return []string{"/usr/bin/fish", "-c", fishCmd}
}
