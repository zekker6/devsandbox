package tools

import (
	"os"
	"os/exec"
	"path/filepath"
)

func init() {
	Register(&Claude{})
}

// Claude provides Claude AI tool integration.
// Mounts Claude config and credential directories read-write.
type Claude struct{}

func (c *Claude) Name() string {
	return "claude"
}

func (c *Claude) Description() string {
	return "Claude Code AI assistant"
}

func (c *Claude) Available(homeDir string) bool {
	// Check if claude is installed or if claude config exists
	if _, err := exec.LookPath("claude"); err == nil {
		return true
	}

	// Also check for claude directories/files
	paths := []string{
		filepath.Join(homeDir, ".claude"),
		filepath.Join(homeDir, ".claude.json"),
		filepath.Join(homeDir, ".config", "Claude"),
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}

	return false
}

func (c *Claude) Bindings(homeDir, sandboxHome string) []Binding {
	bindings := []Binding{
		// Claude directory
		{
			Source:   filepath.Join(homeDir, ".claude"),
			ReadOnly: false,
			Optional: true,
		},
		// Claude config directory
		{
			Source:   filepath.Join(homeDir, ".config", "Claude"),
			ReadOnly: false,
			Optional: true,
		},
		// Claude CLI cache
		{
			Source:   filepath.Join(homeDir, ".cache", "claude-cli-nodejs"),
			ReadOnly: false,
			Optional: true,
		},
		// Claude Code installation
		{
			Source:   filepath.Join(homeDir, ".local", "share", "claude"),
			ReadOnly: false,
			Optional: true,
		},
		// Claude config files
		{
			Source:   filepath.Join(homeDir, ".claude.json"),
			ReadOnly: false,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".claude.json.backup"),
			ReadOnly: false,
			Optional: true,
		},
	}

	return bindings
}

func (c *Claude) Environment(homeDir, sandboxHome string) []EnvVar {
	return nil
}

func (c *Claude) ShellInit(shell string) string {
	return ""
}

func (c *Claude) Check(homeDir string) CheckResult {
	result := CheckResult{
		BinaryName:  "claude",
		InstallHint: "https://code.claude.com/docs/en/setup",
	}

	path, err := exec.LookPath("claude")
	if err == nil {
		result.BinaryPath = path
	}

	// Check config paths
	configPaths := []string{
		filepath.Join(homeDir, ".claude"),
		filepath.Join(homeDir, ".claude.json"),
		filepath.Join(homeDir, ".config", "Claude"),
	}

	for _, p := range configPaths {
		if _, err := os.Stat(p); err == nil {
			result.ConfigPaths = append(result.ConfigPaths, p)
		}
	}

	// Available if binary exists or config exists
	result.Available = result.BinaryPath != "" || len(result.ConfigPaths) > 0

	if !result.Available {
		result.Issues = append(result.Issues, "claude binary not found and no config exists")
	}

	return result
}
