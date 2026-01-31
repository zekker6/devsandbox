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

func (c *Copilot) Description() string {
	return "GitHub Copilot integration"
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

func (c *Copilot) Check(homeDir string) CheckResult {
	result := CheckResult{
		BinaryName:  "gh",
		InstallHint: "Install GitHub CLI: mise install gh, then: gh extension install github/gh-copilot",
	}

	// Copilot uses gh CLI, but availability is based on config dirs
	configPaths := []string{
		filepath.Join(homeDir, ".config", "github-copilot"),
		filepath.Join(homeDir, ".cache", "github-copilot"),
	}

	for _, p := range configPaths {
		if _, err := os.Stat(p); err == nil {
			result.ConfigPaths = append(result.ConfigPaths, p)
		}
	}

	result.Available = len(result.ConfigPaths) > 0

	if !result.Available {
		result.Issues = append(result.Issues, "no GitHub Copilot config directories found")
	}

	return result
}
