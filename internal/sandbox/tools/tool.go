// Package tools provides modular tool configurations for the sandbox.
// Each tool defines its own filesystem bindings, environment variables,
// and shell initialization commands.
package tools

// MountType defines how a binding is mounted in the sandbox.
type MountType string

const (
	// MountBind is a regular bind mount (default behavior).
	MountBind MountType = "bind"

	// MountOverlay uses overlayfs with persistent writes.
	// Changes are stored in the sandbox directory and persist across sessions.
	MountOverlay MountType = "overlay"

	// MountTmpOverlay uses overlayfs with writes to an invisible tmpfs.
	// Changes are discarded when the sandbox exits.
	MountTmpOverlay MountType = "tmpoverlay"
)

// Binding represents a filesystem binding for bwrap.
type Binding struct {
	Source   string // Host path (lower layer for overlays)
	Dest     string // Container path (defaults to Source if empty)
	ReadOnly bool   // Mount as read-only (only for bind mounts)
	Optional bool   // Skip if source doesn't exist

	// Type specifies how to mount (bind, overlay, tmpoverlay).
	// Defaults to MountBind if empty.
	Type MountType

	// OverlaySources specifies additional lower layers for overlay mounts.
	// These are stacked below the primary Source (first is bottom layer).
	OverlaySources []string
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

	// Description returns a short description of what this tool provides.
	Description() string

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

// CheckResult contains detailed availability information for a tool.
type CheckResult struct {
	Available   bool     // Whether the tool is available
	BinaryPath  string   // Path to the tool's binary (if applicable)
	BinaryName  string   // Name of the binary to look for
	ConfigPaths []string // Configuration paths that exist
	Issues      []string // Any issues or warnings
	InstallHint string   // How to install if missing
}

// ToolWithCheck extends Tool with detailed availability checking.
// Use this to provide richer information for the `tools check` command.
type ToolWithCheck interface {
	Tool

	// Check performs detailed availability checking.
	// Returns information about binary location, config paths, and any issues.
	Check(homeDir string) CheckResult
}

// GlobalConfig contains global settings that tools may need.
type GlobalConfig struct {
	// OverlayEnabled indicates if overlays are globally enabled.
	OverlayEnabled bool
}

// ToolWithConfig extends Tool with configuration support.
// Tools that have configurable options should implement this interface.
type ToolWithConfig interface {
	Tool

	// Configure applies configuration to the tool.
	// globalCfg contains global settings (overlay enabled, etc.)
	// toolCfg contains the tool's section from [tools.<name>] in config.toml.
	// Called before Bindings() to set up tool state.
	Configure(globalCfg GlobalConfig, toolCfg map[string]any)
}
