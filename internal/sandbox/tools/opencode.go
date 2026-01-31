package tools

import (
	"os"
	"os/exec"
	"path/filepath"
)

func init() {
	Register(&OpenCode{})
}

// OpenCode provides OpenCode AI tool integration.
// Mounts OpenCode config, data, and cache directories read-write.
type OpenCode struct{}

func (o *OpenCode) Name() string {
	return "opencode"
}

func (o *OpenCode) Description() string {
	return "OpenCode AI assistant"
}

func (o *OpenCode) Available(homeDir string) bool {
	// Check if opencode directories exist
	paths := []string{
		filepath.Join(homeDir, ".config", "opencode"),
		filepath.Join(homeDir, ".local", "share", "opencode"),
		filepath.Join(homeDir, ".cache", "opencode"),
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}

	return false
}

func (o *OpenCode) Bindings(homeDir, sandboxHome string) []Binding {
	return []Binding{
		// OpenCode configuration
		{
			Source:   filepath.Join(homeDir, ".config", "opencode"),
			ReadOnly: false,
			Optional: true,
		},
		// OpenCode data
		{
			Source:   filepath.Join(homeDir, ".local", "share", "opencode"),
			ReadOnly: false,
			Optional: true,
		},
		// OpenCode cache
		{
			Source:   filepath.Join(homeDir, ".cache", "opencode"),
			ReadOnly: false,
			Optional: true,
		},
		// Oh-my-opencode cache
		{
			Source:   filepath.Join(homeDir, ".cache", "oh-my-opencode"),
			ReadOnly: false,
			Optional: true,
		},
	}
}

func (o *OpenCode) Environment(homeDir, sandboxHome string) []EnvVar {
	return nil
}

func (o *OpenCode) ShellInit(shell string) string {
	return ""
}

func (o *OpenCode) Check(homeDir string) CheckResult {
	result := CheckResult{
		BinaryName:  "opencode",
		InstallHint: "https://opencode.ai/",
	}

	path, err := exec.LookPath("opencode")
	if err == nil {
		result.BinaryPath = path
	}

	// Check config paths
	configPaths := []string{
		filepath.Join(homeDir, ".config", "opencode"),
		filepath.Join(homeDir, ".local", "share", "opencode"),
		filepath.Join(homeDir, ".cache", "opencode"),
	}

	for _, p := range configPaths {
		if _, err := os.Stat(p); err == nil {
			result.ConfigPaths = append(result.ConfigPaths, p)
		}
	}

	// Available if binary exists or config exists
	result.Available = result.BinaryPath != "" || len(result.ConfigPaths) > 0

	if !result.Available {
		result.Issues = append(result.Issues, "opencode binary not found and no config exists")
	}

	return result
}
