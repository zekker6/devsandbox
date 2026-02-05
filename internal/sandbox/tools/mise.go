package tools

import (
	"fmt"
	"os/exec"
	"path/filepath"
)

func init() {
	Register(&Mise{})
}

// Mise provides mise tool manager integration.
// Mise manages development tools like Node.js, Python, Go, etc.
type Mise struct {
	writable   bool
	persistent bool
}

func (m *Mise) Name() string {
	return "mise"
}

func (m *Mise) Description() string {
	return "Tool version manager (node, python, go, etc.)"
}

func (m *Mise) Available(homeDir string) bool {
	_, err := exec.LookPath("mise")
	return err == nil
}

// Configure implements ToolWithConfig.
// Parses mise-specific config from the raw map.
func (m *Mise) Configure(globalCfg GlobalConfig, toolCfg map[string]any) {
	// If overlays are globally disabled, don't enable writable mode
	if !globalCfg.OverlayEnabled {
		m.writable = false
		m.persistent = false
		return
	}

	if toolCfg == nil {
		return
	}
	if v, ok := toolCfg["writable"].(bool); ok {
		m.writable = v
	}
	if v, ok := toolCfg["persistent"].(bool); ok {
		m.persistent = v
	}
}

func (m *Mise) Bindings(homeDir, sandboxHome string) []Binding {
	// User's local bin directory (may contain mise shims)
	// Always read-only - shims just redirect to actual tools
	bindings := []Binding{
		{
			Source:   filepath.Join(homeDir, ".local", "bin"),
			ReadOnly: true,
			Optional: true,
		},
		// Mise configuration - always read-only
		{
			Source:   filepath.Join(homeDir, ".config", "mise"),
			ReadOnly: true,
			Optional: true,
		},
	}

	// Mise installed tools and data
	if m.writable {
		mountType := MountTmpOverlay
		if m.persistent {
			mountType = MountOverlay
		}
		bindings = append(bindings,
			Binding{
				Source:   filepath.Join(homeDir, ".local", "share", "mise"),
				Type:     mountType,
				Optional: true,
			},
			Binding{
				Source:   filepath.Join(homeDir, ".cache", "mise"),
				Type:     mountType,
				Optional: true,
			},
			Binding{
				Source:   filepath.Join(homeDir, ".local", "state", "mise"),
				Type:     mountType,
				Optional: true,
			},
		)
	} else {
		// Default: read-only bind mounts
		bindings = append(bindings,
			Binding{
				Source:   filepath.Join(homeDir, ".local", "share", "mise"),
				ReadOnly: true,
				Optional: true,
			},
			Binding{
				Source:   filepath.Join(homeDir, ".cache", "mise"),
				ReadOnly: true,
				Optional: true,
			},
			Binding{
				Source:   filepath.Join(homeDir, ".local", "state", "mise"),
				ReadOnly: true,
				Optional: true,
			},
		)
	}

	return bindings
}

func (m *Mise) Environment(homeDir, sandboxHome string) []EnvVar {
	// MISE_SHELL is set by the builder based on detected shell
	// PATH includes mise shims, also set by builder
	return nil
}

func (m *Mise) ShellInit(shell string) string {
	switch shell {
	case "fish":
		return `if command -q mise; mise activate fish | source; end`
	case "zsh":
		return `if command -v mise &>/dev/null; then eval "$(mise activate zsh)"; fi`
	case "bash":
		return `if command -v mise &>/dev/null; then eval "$(mise activate bash)"; fi`
	default:
		return fmt.Sprintf(`if command -v mise &>/dev/null; then eval "$(mise activate %s)"; fi`, shell)
	}
}

func (m *Mise) Check(homeDir string) CheckResult {
	result := CheckBinary("mise", "https://mise.jdx.dev/installing-mise.html")
	if !result.Available {
		return result
	}

	result.AddConfigPaths(
		filepath.Join(homeDir, ".config", "mise"),
		filepath.Join(homeDir, ".local", "share", "mise"),
		filepath.Join(homeDir, ".local", "bin"),
	)

	return result
}

// DockerBindings returns Docker-specific mounts for mise.
// In Docker mode, we only mount config and shims read-only.
// The data/cache/state directories are NOT mounted - the container uses its own
// copies created by the entrypoint. This avoids read-only mount conflicts with
// mise's tracking config feature.
func (m *Mise) DockerBindings(homeDir, sandboxHome string) []DockerMount {
	return []DockerMount{
		// User's local bin directory (may contain mise shims)
		{
			Source:   filepath.Join(homeDir, ".local", "bin"),
			Dest:     "/home/sandboxuser/.local/bin",
			ReadOnly: true,
		},
		// Mise configuration - always read-only
		{
			Source:   filepath.Join(homeDir, ".config", "mise"),
			Dest:     "/home/sandboxuser/.config/mise",
			ReadOnly: true,
		},
		// Note: data/cache/state directories are NOT mounted in Docker mode.
		// The container uses its own copies at /home/sandboxuser/.local/share/mise etc.
		// This allows mise to write tracking configs without read-only mount errors.
	}
}
