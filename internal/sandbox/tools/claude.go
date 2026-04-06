package tools

import (
	"os"
	"os/exec"
	"path/filepath"
)

func init() {
	Register(&Claude{})
}

// Claude provides Claude AI tool integration.
// Mounts Claude config directory with tmpoverlay (protects settings/credentials)
// and projects subdirectory with persistent overlay (preserves session history and memory).
type Claude struct{}

func (c *Claude) Name() string {
	return "claude"
}

func (c *Claude) Description() string {
	return "Claude Code AI assistant"
}

// configDir returns the CLAUDE_CONFIG_DIR value if set, or empty string for defaults.
func (c *Claude) configDir() string {
	return os.Getenv("CLAUDE_CONFIG_DIR")
}

func (c *Claude) Available(homeDir string) bool {
	// Check if claude is installed or if claude config exists
	if _, err := exec.LookPath("claude"); err == nil {
		return true
	}

	// Check custom config directory from CLAUDE_CONFIG_DIR
	if dir := c.configDir(); dir != "" {
		if _, err := os.Stat(dir); err == nil {
			return true
		}
	}

	// Also check for claude directories/files
	paths := []string{
		filepath.Join(homeDir, ".claude"),
		filepath.Join(homeDir, ".claude.json"),
		filepath.Join(homeDir, ".config", "Claude"),
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}

	return false
}

func (c *Claude) Bindings(homeDir, sandboxHome string) []Binding {
	bindings := []Binding{
		// Claude Code system installation (npm global) — explicit escape hatch, read-only
		{
			Source:   "/opt/claude-code",
			Type:     MountBind,
			ReadOnly: true,
			Optional: true,
		},
	}

	if dir := c.configDir(); dir != "" {
		// Custom config directory from CLAUDE_CONFIG_DIR — tmpoverlay protects config,
		// persistent overlay on projects/ preserves session history and memory.
		bindings = append(bindings,
			Binding{
				Source:   dir,
				Category: CategoryConfig,
				Optional: true,
			},
			Binding{
				Source:   filepath.Join(dir, "projects"),
				Category: CategoryData,
				Optional: true,
			},
		)
	} else {
		// Default config paths — tmpoverlay on ~/.claude protects settings/credentials,
		// persistent overlay on projects/ preserves session history and memory.
		bindings = append(bindings,
			Binding{
				Source:   filepath.Join(homeDir, ".claude"),
				Category: CategoryConfig,
				Optional: true,
			},
			Binding{
				Source:   filepath.Join(homeDir, ".claude", "projects"),
				Category: CategoryData,
				Optional: true,
			},
			// .claude.json files must be rw bind mounts — they are files (not dirs)
			// so overlays don't apply, and Claude Code needs write access.
			Binding{
				Source:   filepath.Join(homeDir, ".claude.json"),
				Type:     MountBind,
				Optional: true,
			},
			Binding{
				Source:   filepath.Join(homeDir, ".claude.json.backup"),
				Type:     MountBind,
				Optional: true,
			},
		)
	}

	// These bindings are always included regardless of CLAUDE_CONFIG_DIR
	bindings = append(bindings,
		Binding{
			Source:   filepath.Join(homeDir, ".config", "Claude"),
			Category: CategoryConfig,
			Optional: true,
		},
		Binding{
			Source:   filepath.Join(homeDir, ".cache", "claude-cli-nodejs"),
			Category: CategoryCache,
			Optional: true,
		},
		Binding{
			Source:   filepath.Join(homeDir, ".local", "share", "claude"),
			Category: CategoryData,
			Optional: true,
		},
	)

	return bindings
}

func (c *Claude) Environment(homeDir, sandboxHome string) []EnvVar {
	if c.configDir() != "" {
		return []EnvVar{
			{Name: "CLAUDE_CONFIG_DIR", FromHost: true},
		}
	}
	return nil
}

func (c *Claude) ShellInit(shell string) string {
	return ""
}

func (c *Claude) Check(homeDir string) CheckResult {
	result := CheckResult{
		BinaryName:  "claude",
		InstallHint: "https://code.claude.com/docs/en/setup",
	}

	path, err := exec.LookPath("claude")
	if err == nil {
		result.BinaryPath = path
	}

	// Check config paths
	configPaths := []string{
		"/opt/claude-code",
		filepath.Join(homeDir, ".claude"),
		filepath.Join(homeDir, ".claude.json"),
		filepath.Join(homeDir, ".config", "Claude"),
	}

	// Add custom config dir if set
	if dir := c.configDir(); dir != "" {
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
		result.Issues = append(result.Issues, "claude binary not found and no config exists")
	}

	return result
}
