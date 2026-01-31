package tools

import (
	"os"
	"os/exec"
	"path/filepath"
)

func init() {
	Register(&Nvim{})
}

// Nvim provides Neovim editor configuration.
// Mounts config, data, state, and cache directories read-only.
type Nvim struct{}

func (n *Nvim) Name() string {
	return "nvim"
}

func (n *Nvim) Description() string {
	return "Neovim editor configuration"
}

func (n *Nvim) Available(homeDir string) bool {
	_, err := exec.LookPath("nvim")
	return err == nil
}

func (n *Nvim) Bindings(homeDir, sandboxHome string) []Binding {
	return []Binding{
		// Neovim configuration
		{
			Source:   filepath.Join(homeDir, ".config", "nvim"),
			ReadOnly: true,
			Optional: true,
		},
		// Neovim data (plugins, etc.)
		{
			Source:   filepath.Join(homeDir, ".local", "share", "nvim"),
			ReadOnly: true,
			Optional: true,
		},
		// Neovim state
		{
			Source:   filepath.Join(homeDir, ".local", "state", "nvim"),
			ReadOnly: true,
			Optional: true,
		},
		// Neovim cache
		{
			Source:   filepath.Join(homeDir, ".cache", "nvim"),
			ReadOnly: true,
			Optional: true,
		},
	}
}

func (n *Nvim) Environment(homeDir, sandboxHome string) []EnvVar {
	// Only set EDITOR/VISUAL if nvim is actually available
	if _, err := exec.LookPath("nvim"); err != nil {
		return nil
	}

	// Check if nvim config exists (indicates user uses nvim)
	nvimConfig := filepath.Join(homeDir, ".config", "nvim")
	if _, err := os.Stat(nvimConfig); os.IsNotExist(err) {
		return nil
	}

	return []EnvVar{
		{Name: "EDITOR", Value: "nvim"},
		{Name: "VISUAL", Value: "nvim"},
	}
}

func (n *Nvim) ShellInit(shell string) string {
	return ""
}

func (n *Nvim) Check(homeDir string) CheckResult {
	result := CheckResult{
		BinaryName:  "nvim",
		InstallHint: "mise install neovim",
	}

	path, err := exec.LookPath("nvim")
	if err != nil {
		result.Issues = append(result.Issues, "nvim binary not found in PATH")
		return result
	}

	result.Available = true
	result.BinaryPath = path

	// Check config paths
	configPaths := []string{
		filepath.Join(homeDir, ".config", "nvim"),
		filepath.Join(homeDir, ".local", "share", "nvim"),
	}

	for _, p := range configPaths {
		if _, err := os.Stat(p); err == nil {
			result.ConfigPaths = append(result.ConfigPaths, p)
		}
	}

	return result
}
