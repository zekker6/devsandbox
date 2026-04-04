package tools

import (
	"os"
	"os/exec"
	"path/filepath"
)

func init() {
	Register(&OhMyPosh{})
}

// OhMyPosh provides oh-my-posh prompt configuration.
// Creates a modified config with a sandbox indicator segment.
type OhMyPosh struct{}

func (o *OhMyPosh) Name() string {
	return "oh-my-posh"
}

func (o *OhMyPosh) Description() string {
	return "Oh My Posh prompt with sandbox indicator"
}

func (o *OhMyPosh) Available(homeDir string) bool {
	// Check if oh-my-posh is installed and user has a config
	if _, err := exec.LookPath("oh-my-posh"); err != nil {
		return false
	}

	// Check common config locations
	configPaths := []string{
		filepath.Join(homeDir, ".config", "ohmyposh"),
		filepath.Join(homeDir, ".poshthemes"),
	}

	for _, p := range configPaths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}

	// Check if POSH_THEME is set
	if os.Getenv("POSH_THEME") != "" {
		return true
	}

	return false
}

func (o *OhMyPosh) Bindings(homeDir, sandboxHome string) []Binding {
	return []Binding{
		{Source: filepath.Join(homeDir, ".config", "ohmyposh"), Category: CategoryConfig, Optional: true},
		{Source: filepath.Join(homeDir, ".poshthemes"), Category: CategoryData, Optional: true},
	}
}

func (o *OhMyPosh) Environment(homeDir, sandboxHome string) []EnvVar {
	return nil
}

func (o *OhMyPosh) ShellInit(shell string) string {
	// oh-my-posh can read DEVSANDBOX env var in its config
	// The user can add a custom segment that checks $DEVSANDBOX
	return ""
}

func (o *OhMyPosh) Check(homeDir string) CheckResult {
	result := CheckResult{
		BinaryName:  "oh-my-posh",
		InstallHint: "mise install oh-my-posh",
	}

	path, err := exec.LookPath("oh-my-posh")
	if err != nil {
		result.Issues = append(result.Issues, "oh-my-posh binary not found in PATH")
		return result
	}

	result.BinaryPath = path

	// Check config paths
	configPaths := []string{
		filepath.Join(homeDir, ".config", "ohmyposh"),
		filepath.Join(homeDir, ".poshthemes"),
	}

	for _, p := range configPaths {
		if _, err := os.Stat(p); err == nil {
			result.ConfigPaths = append(result.ConfigPaths, p)
		}
	}

	result.Available = len(result.ConfigPaths) > 0 || os.Getenv("POSH_THEME") != ""

	if !result.Available {
		result.Issues = append(result.Issues, "no oh-my-posh config found")
	}

	return result
}
