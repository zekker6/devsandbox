package tools

import (
	"os"
	"os/exec"
	"path/filepath"
)

func init() {
	Register(&Fish{})
	Register(&Zsh{})
	Register(&Bash{})
}

// Fish provides fish shell configuration.
type Fish struct{}

func (f *Fish) Name() string {
	return "shell-fish"
}

func (f *Fish) Description() string {
	return "Fish shell configuration"
}

func (f *Fish) Available(homeDir string) bool {
	// Check if fish config exists
	fishConfig := filepath.Join(homeDir, ".config", "fish")
	_, err := os.Stat(fishConfig)
	return err == nil
}

func (f *Fish) Bindings(homeDir, sandboxHome string) []Binding {
	return []Binding{
		{
			Source:   filepath.Join(homeDir, ".config", "fish"),
			ReadOnly: true,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".local", "share", "fish", "vendor_completions.d"),
			ReadOnly: true,
			Optional: true,
		},
	}
}

func (f *Fish) Environment(homeDir, sandboxHome string) []EnvVar {
	return nil
}

func (f *Fish) ShellInit(shell string) string {
	return ""
}

func (f *Fish) Check(homeDir string) CheckResult {
	result := CheckResult{
		BinaryName:  "fish",
		InstallHint: "Install via system package manager",
	}

	path, err := exec.LookPath("fish")
	if err == nil {
		result.BinaryPath = path
	}

	// Check config
	fishConfig := filepath.Join(homeDir, ".config", "fish")
	if _, err := os.Stat(fishConfig); err == nil {
		result.ConfigPaths = append(result.ConfigPaths, fishConfig)
		result.Available = true
	} else {
		result.Issues = append(result.Issues, "no ~/.config/fish found")
	}

	return result
}

// Zsh provides zsh shell configuration.
type Zsh struct{}

func (z *Zsh) Name() string {
	return "shell-zsh"
}

func (z *Zsh) Description() string {
	return "Zsh shell configuration"
}

func (z *Zsh) Available(homeDir string) bool {
	// Check if any zsh config exists
	paths := []string{
		filepath.Join(homeDir, ".zshrc"),
		filepath.Join(homeDir, ".zshenv"),
		filepath.Join(homeDir, ".config", "zsh"),
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}

	return false
}

func (z *Zsh) Bindings(homeDir, sandboxHome string) []Binding {
	return []Binding{
		{
			Source:   filepath.Join(homeDir, ".zshrc"),
			ReadOnly: true,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".zshenv"),
			ReadOnly: true,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".zprofile"),
			ReadOnly: true,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".config", "zsh"),
			ReadOnly: true,
			Optional: true,
		},
		// Oh-my-zsh
		{
			Source:   filepath.Join(homeDir, ".oh-my-zsh"),
			ReadOnly: true,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".local", "share", "zsh"),
			ReadOnly: true,
			Optional: true,
		},
	}
}

func (z *Zsh) Environment(homeDir, sandboxHome string) []EnvVar {
	return nil
}

func (z *Zsh) ShellInit(shell string) string {
	return ""
}

func (z *Zsh) Check(homeDir string) CheckResult {
	result := CheckResult{
		BinaryName:  "zsh",
		InstallHint: "Install via system package manager",
	}

	path, err := exec.LookPath("zsh")
	if err == nil {
		result.BinaryPath = path
	}

	// Check config paths
	configPaths := []string{
		filepath.Join(homeDir, ".zshrc"),
		filepath.Join(homeDir, ".zshenv"),
		filepath.Join(homeDir, ".config", "zsh"),
	}

	for _, p := range configPaths {
		if _, err := os.Stat(p); err == nil {
			result.ConfigPaths = append(result.ConfigPaths, p)
		}
	}

	result.Available = len(result.ConfigPaths) > 0

	if !result.Available {
		result.Issues = append(result.Issues, "no zsh config files found")
	}

	return result
}

// Bash provides bash shell configuration.
type Bash struct{}

func (b *Bash) Name() string {
	return "shell-bash"
}

func (b *Bash) Description() string {
	return "Bash shell configuration"
}

func (b *Bash) Available(homeDir string) bool {
	// Check if any bash config exists
	paths := []string{
		filepath.Join(homeDir, ".bashrc"),
		filepath.Join(homeDir, ".bash_profile"),
		filepath.Join(homeDir, ".profile"),
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}

	return false
}

func (b *Bash) Bindings(homeDir, sandboxHome string) []Binding {
	return []Binding{
		{
			Source:   filepath.Join(homeDir, ".bashrc"),
			ReadOnly: true,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".bash_profile"),
			ReadOnly: true,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".profile"),
			ReadOnly: true,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".config", "bash"),
			ReadOnly: true,
			Optional: true,
		},
	}
}

func (b *Bash) Environment(homeDir, sandboxHome string) []EnvVar {
	return nil
}

func (b *Bash) ShellInit(shell string) string {
	return ""
}

func (b *Bash) Check(homeDir string) CheckResult {
	result := CheckResult{
		BinaryName:  "bash",
		InstallHint: "Install via system package manager",
	}

	path, err := exec.LookPath("bash")
	if err == nil {
		result.BinaryPath = path
	}

	// Check config paths
	configPaths := []string{
		filepath.Join(homeDir, ".bashrc"),
		filepath.Join(homeDir, ".bash_profile"),
		filepath.Join(homeDir, ".profile"),
	}

	for _, p := range configPaths {
		if _, err := os.Stat(p); err == nil {
			result.ConfigPaths = append(result.ConfigPaths, p)
		}
	}

	result.Available = len(result.ConfigPaths) > 0

	if !result.Available {
		result.Issues = append(result.Issues, "no bash config files found")
	}

	return result
}
