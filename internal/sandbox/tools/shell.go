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

// Zsh provides zsh shell configuration.
type Zsh struct{}

func (z *Zsh) Name() string {
	return "shell-zsh"
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

// Bash provides bash shell configuration.
type Bash struct{}

func (b *Bash) Name() string {
	return "shell-bash"
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
