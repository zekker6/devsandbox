package tools

import (
	"os"
	"path/filepath"
)

func init() {
	Register(&Copilot{})
}

// Copilot provides GitHub Copilot integration.
// Mounts Copilot config and cache directories read-write.
type Copilot struct{}

func (c *Copilot) Name() string {
	return "copilot"
}

func (c *Copilot) Available(homeDir string) bool {
	// Check if copilot directories exist
	paths := []string{
		filepath.Join(homeDir, ".config", "github-copilot"),
		filepath.Join(homeDir, ".cache", "github-copilot"),
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}

	return false
}

func (c *Copilot) Bindings(homeDir, sandboxHome string) []Binding {
	return []Binding{
		// Copilot configuration
		{
			Source:   filepath.Join(homeDir, ".config", "github-copilot"),
			ReadOnly: false,
			Optional: true,
		},
		// Copilot cache
		{
			Source:   filepath.Join(homeDir, ".cache", "github-copilot"),
			ReadOnly: false,
			Optional: true,
		},
	}
}

func (c *Copilot) Environment(homeDir, sandboxHome string) []EnvVar {
	return nil
}

func (c *Copilot) ShellInit(shell string) string {
	return ""
}
