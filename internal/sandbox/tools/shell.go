package tools

import (
	"os"
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
		// Note: .local/share/fish is NOT mounted read-only because fish needs
		// to write universal variables to fish_variables. Let fish use the
		// sandbox home's copy which is writable.
	}
}

func (f *Fish) Environment(homeDir, sandboxHome string) []EnvVar {
	return nil
}

func (f *Fish) ShellInit(shell string) string {
	return ""
}

func (f *Fish) Check(homeDir string) CheckResult {
	result := CheckBinary("fish", "Install via system package manager")

	// Check config - fish shell availability is based on config existence
	result.AddConfigPath(filepath.Join(homeDir, ".config", "fish"))

	if len(result.ConfigPaths) == 0 {
		result.Available = false
		result.AddIssue("no ~/.config/fish found")
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
	result := CheckBinary("zsh", "Install via system package manager")

	// Check config paths - zsh availability is based on config existence
	result.AddConfigPaths(
		filepath.Join(homeDir, ".zshrc"),
		filepath.Join(homeDir, ".zshenv"),
		filepath.Join(homeDir, ".config", "zsh"),
	)

	if len(result.ConfigPaths) == 0 {
		result.Available = false
		result.AddIssue("no zsh config files found")
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
		// Core bash configuration files
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
			Source:   filepath.Join(homeDir, ".bash_logout"),
			ReadOnly: true,
			Optional: true,
		},
		// Custom tools support: aliases, functions, completions
		{
			Source:   filepath.Join(homeDir, ".bash_aliases"),
			ReadOnly: true,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".bash_functions"),
			ReadOnly: true,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".bash_completion"),
			ReadOnly: true,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".bash_completion.d"),
			ReadOnly: true,
			Optional: true,
		},
		// Readline configuration (affects bash input)
		{
			Source:   filepath.Join(homeDir, ".inputrc"),
			ReadOnly: true,
			Optional: true,
		},
		// XDG config locations
		{
			Source:   filepath.Join(homeDir, ".config", "bash"),
			ReadOnly: true,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".config", "readline"),
			ReadOnly: true,
			Optional: true,
		},
		// Local bash data (history excluded for privacy)
		{
			Source:   filepath.Join(homeDir, ".local", "share", "bash"),
			ReadOnly: true,
			Optional: true,
		},
		// Bash-it framework (popular bash customization)
		{
			Source:   filepath.Join(homeDir, ".bash_it"),
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
	result := CheckBinary("bash", "Install via system package manager")

	// Check all config paths that we mount - bash availability is based on config existence
	result.AddConfigPaths(
		filepath.Join(homeDir, ".bashrc"),
		filepath.Join(homeDir, ".bash_profile"),
		filepath.Join(homeDir, ".profile"),
		filepath.Join(homeDir, ".bash_logout"),
		filepath.Join(homeDir, ".bash_aliases"),
		filepath.Join(homeDir, ".bash_functions"),
		filepath.Join(homeDir, ".bash_completion"),
		filepath.Join(homeDir, ".bash_completion.d"),
		filepath.Join(homeDir, ".inputrc"),
		filepath.Join(homeDir, ".config", "bash"),
		filepath.Join(homeDir, ".config", "readline"),
		filepath.Join(homeDir, ".local", "share", "bash"),
		filepath.Join(homeDir, ".bash_it"),
	)

	if len(result.ConfigPaths) == 0 {
		result.Available = false
		result.AddIssue("no bash config files found")
	}

	return result
}
