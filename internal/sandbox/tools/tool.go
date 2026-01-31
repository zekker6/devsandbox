// Package tools provides modular tool configurations for the sandbox.
// Each tool defines its own filesystem bindings, environment variables,
// and shell initialization commands.
package tools

// Binding represents a filesystem binding for bwrap.
type Binding struct {
	Source   string // Host path
	Dest     string // Container path (defaults to Source if empty)
	ReadOnly bool   // Mount as read-only
	Optional bool   // Skip if source doesn't exist
}

// EnvVar represents an environment variable.
type EnvVar struct {
	Name     string // Variable name
	Value    string // Variable value (ignored if FromHost is true)
	FromHost bool   // Copy value from host environment
}

// Tool defines the interface for sandbox tools.
// Each tool provides its own bindings, environment, and initialization.
type Tool interface {
	// Name returns a unique identifier for this tool.
	Name() string

	// Available checks if this tool is installed/usable on the host.
	// homeDir is the user's home directory.
	Available(homeDir string) bool

	// Bindings returns filesystem bindings for this tool.
	// homeDir is the host home, sandboxHome is the sandbox home directory.
	Bindings(homeDir, sandboxHome string) []Binding

	// Environment returns environment variables for this tool.
	// homeDir is the host home, sandboxHome is the sandbox home directory.
	Environment(homeDir, sandboxHome string) []EnvVar

	// ShellInit returns shell-specific initialization commands.
	// shell is one of: "fish", "bash", "zsh".
	// Returns empty string if no initialization is needed.
	ShellInit(shell string) string
}

// ToolWithSetup extends Tool with a setup phase that runs before bindings.
// Use this for tools that need to generate files (like safe gitconfig).
type ToolWithSetup interface {
	Tool

	// Setup performs any necessary preparation before bindings are applied.
	// This can create files, modify configs, etc.
	// sandboxHome is the sandbox home directory where files can be written.
	Setup(homeDir, sandboxHome string) error
}
