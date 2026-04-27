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
//
// OPENCODE_CONFIG_DIR is loaded by opencode in addition to the standard
// ~/.config/opencode directory (it does not replace it). When set, the host
// value is passed through to the sandbox and the directory is mounted so
// opencode finds the same agents/commands/modes/plugins inside.
type OpenCode struct{}

func (o *OpenCode) Name() string {
	return "opencode"
}

func (o *OpenCode) Description() string {
	return "OpenCode AI assistant"
}

// extraConfigDir returns OPENCODE_CONFIG_DIR if set, or empty string.
func (o *OpenCode) extraConfigDir() string {
	return os.Getenv("OPENCODE_CONFIG_DIR")
}

func (o *OpenCode) Available(homeDir string) bool {
	// Check if opencode directories exist
	paths := []string{
		filepath.Join(homeDir, ".config", "opencode"),
		filepath.Join(homeDir, ".local", "share", "opencode"),
		filepath.Join(homeDir, ".cache", "opencode"),
	}
	if dir := o.extraConfigDir(); dir != "" {
		paths = append(paths, dir)
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}

	return false
}

func (o *OpenCode) Bindings(homeDir, sandboxHome string) []Binding {
	bindings := []Binding{
		// OpenCode configuration
		{
			Source:   filepath.Join(homeDir, ".config", "opencode"),
			Category: CategoryConfig,
			Optional: true,
		},
		// OpenCode data
		{
			Source:   filepath.Join(homeDir, ".local", "share", "opencode"),
			Category: CategoryData,
			Optional: true,
		},
		// OpenCode cache
		{
			Source:   filepath.Join(homeDir, ".cache", "opencode"),
			Category: CategoryCache,
			Optional: true,
		},
		// Oh-my-opencode cache
		{
			Source:   filepath.Join(homeDir, ".cache", "oh-my-opencode"),
			Category: CategoryCache,
			Optional: true,
		},
	}

	if dir := o.extraConfigDir(); dir != "" {
		bindings = append(bindings, Binding{
			Source:   dir,
			Category: CategoryConfig,
			Optional: true,
		})
	}

	return bindings
}

func (o *OpenCode) Environment(homeDir, sandboxHome string) []EnvVar {
	if o.extraConfigDir() != "" {
		return []EnvVar{
			{Name: "OPENCODE_CONFIG_DIR", FromHost: true},
		}
	}
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
	if dir := o.extraConfigDir(); dir != "" {
		configPaths = append(configPaths, dir)
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
