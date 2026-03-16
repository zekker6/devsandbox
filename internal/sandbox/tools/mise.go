package tools

import (
	"bufio"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
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

// CacheMounts implements ToolWithCache.
// Returns cache directories for mise's installed tools and download cache.
func (m *Mise) CacheMounts() []CacheMount {
	return []CacheMount{
		{Name: "mise", EnvVar: "MISE_DATA_DIR"},
		{Name: "mise/cache", EnvVar: "MISE_CACHE_DIR"},
	}
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

// MiseTrustStatus represents the trust status of a mise config directory.
type MiseTrustStatus struct {
	Path    string
	Trusted bool
}

// CheckMiseTrust checks if mise config files in the given directory are trusted.
// Only returns statuses for config files within the specified directory, ignoring
// parent directory configs that mise also reports.
// Returns nil if mise is not available or no config files are found.
func CheckMiseTrust(dir string) ([]MiseTrustStatus, error) {
	if _, err := exec.LookPath("mise"); err != nil {
		return nil, nil
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolving directory path: %w", err)
	}

	cmd := exec.Command("mise", "trust", "--show")
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return nil, nil
	}

	var statuses []MiseTrustStatus
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		idx := strings.LastIndex(line, ": ")
		if idx == -1 {
			continue
		}
		path := line[:idx]
		status := line[idx+2:]

		// Only include configs that are within the requested directory.
		// mise trust --show also reports configs from parent directories.
		absPath, pathErr := filepath.Abs(path)
		if pathErr != nil {
			continue
		}
		if !strings.HasPrefix(absPath, absDir+string(filepath.Separator)) && absPath != absDir {
			continue
		}

		statuses = append(statuses, MiseTrustStatus{
			Path:    path,
			Trusted: status == "trusted",
		})
	}

	return statuses, nil
}

// TrustMiseConfig runs `mise trust` for the given directory.
func TrustMiseConfig(dir string) error {
	cmd := exec.Command("mise", "trust")
	cmd.Dir = dir
	return cmd.Run()
}
