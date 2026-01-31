package tools

import (
	"fmt"
	"os/exec"
	"path/filepath"
)

func init() {
	Register(&Mise{})
}

// Mise provides mise tool manager integration.
// Mise manages development tools like Node.js, Python, Go, etc.
type Mise struct{}

func (m *Mise) Name() string {
	return "mise"
}

func (m *Mise) Available(homeDir string) bool {
	_, err := exec.LookPath("mise")
	return err == nil
}

func (m *Mise) Bindings(homeDir, sandboxHome string) []Binding {
	return []Binding{
		// User's local bin directory (may contain mise shims)
		{
			Source:   filepath.Join(homeDir, ".local", "bin"),
			ReadOnly: true,
			Optional: true,
		},
		// Mise configuration
		{
			Source:   filepath.Join(homeDir, ".config", "mise"),
			ReadOnly: true,
			Optional: true,
		},
		// Mise installed tools and data
		{
			Source:   filepath.Join(homeDir, ".local", "share", "mise"),
			ReadOnly: true,
			Optional: true,
		},
		// Mise cache
		{
			Source:   filepath.Join(homeDir, ".cache", "mise"),
			ReadOnly: true,
			Optional: true,
		},
		// Mise state
		{
			Source:   filepath.Join(homeDir, ".local", "state", "mise"),
			ReadOnly: true,
			Optional: true,
		},
	}
}

func (m *Mise) Environment(homeDir, sandboxHome string) []EnvVar {
	// MISE_SHELL is set by the builder based on detected shell
	// PATH includes mise shims, also set by builder
	return nil
}

func (m *Mise) ShellInit(shell string) string {
	switch shell {
	case "fish":
		return `if command -q mise; mise activate fish | source; end`
	case "zsh":
		return `if command -v mise &>/dev/null; then eval "$(mise activate zsh)"; fi`
	case "bash":
		return `if command -v mise &>/dev/null; then eval "$(mise activate bash)"; fi`
	default:
		return fmt.Sprintf(`if command -v mise &>/dev/null; then eval "$(mise activate %s)"; fi`, shell)
	}
}
