package tools

import (
	"bufio"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func init() {
	Register(&Mise{})
}

// Mise provides mise tool manager integration.
// Mise manages development tools like Node.js, Python, Go, etc.
type Mise struct {
	// ignoreGlobalConfig, when set, points MISE_GLOBAL_CONFIG_FILE at /dev/null in
	// the sandbox so mise does not read the host user's global ~/.config/mise/config.toml.
	ignoreGlobalConfig bool
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

func (m *Mise) Bindings(homeDir, sandboxHome string) []Binding {
	return []Binding{
		// ~/.local/bin is a read-only bind, not a category-based overlay.
		// A persistent overlay here lets tool self-updaters (e.g. claude's)
		// write partial binaries into the upper-dir that shadow the real
		// host files across sessions, and creates a persistent PATH hijack
		// attack surface (H6 in docs/security-assessment-2026-04-04.md).
		{
			Source:   filepath.Join(homeDir, ".local", "bin"),
			Type:     MountBind,
			ReadOnly: true,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".config", "mise"),
			Category: CategoryConfig,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".local", "share", "mise"),
			Category: CategoryData,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".cache", "mise"),
			Category: CategoryCache,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".local", "state", "mise"),
			Category: CategoryState,
			Optional: true,
		},
	}
}

// Configure implements ToolWithConfig. It reads the mise-specific settings from
// the `[tools.mise]` config section.
func (m *Mise) Configure(_ GlobalConfig, toolCfg map[string]any) {
	m.ignoreGlobalConfig = false // default: respect the host's global mise config
	if toolCfg == nil {
		return
	}
	// Validation (config.Validate) guarantees this is a bool when present, so a
	// bad type never silently degrades to the default here.
	if v, ok := toolCfg["ignore_global_config"].(bool); ok {
		m.ignoreGlobalConfig = v
	}
}

func (m *Mise) Environment(homeDir, sandboxHome string) []EnvVar {
	// MISE_SHELL is set by the builder based on detected shell
	// PATH includes mise shims, also set by builder
	if m.ignoreGlobalConfig {
		// Point the global config at /dev/null so the sandbox does not eagerly
		// resolve/install the host user's global `@latest` tools. On a proxy/egress
		// -locked sandbox those resolutions hang on `npm view`/registry lookups and a
		// swarm of them can OOM the guest. The project's `.mise.toml`, the image's
		// system config (baked node), and `~/.config/mise/settings.toml` still apply;
		// only the global `config.toml` tool list is dropped.
		return []EnvVar{{Name: "MISE_GLOBAL_CONFIG_FILE", Value: "/dev/null"}}
	}
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
// Only the download cache is a dedicated cache mount. MISE_DATA_DIR is not: the
// container backends point it at the persistent sandbox home (set in the Docker
// isolator), so installed tools persist across runs there, and the image's baked
// node is mirrored in by the in-guest shim so it resolves without a reinstall.
func (m *Mise) CacheMounts() []CacheMount {
	return []CacheMount{
		{Name: "mise/cache", EnvVar: "MISE_CACHE_DIR"},
	}
}

// hostMiseInstallsDest is the in-guest shadow path where the host's mise
// installs directory is mounted read-only. LOAD-BEARING: the in-guest shim
// (hostMiseInstalls in cmd/devsandbox-shim/main.go) hardcodes the same path to
// seed version-level symlinks from it into the sandbox MISE_DATA_DIR.
const hostMiseInstallsDest = "/opt/host-mise/installs"

// DockerBindings returns Docker-specific mounts for mise.
// In Docker mode, we only mount config and shims read-only.
// The data/cache/state directories are NOT mounted at their home paths - the
// container uses its own copies created by the entrypoint. This avoids
// read-only mount conflicts with mise's tracking config feature. On Linux
// hosts the host's installs directory is additionally shared read-only at a
// shadow path, from which the shim seeds symlinks into the sandbox data dir so
// host-installed tools resolve without a per-guest reinstall.
func (m *Mise) DockerBindings(homeDir, sandboxHome string) []DockerMount {
	mounts := []DockerMount{
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
	}
	if hm := hostMiseInstallsMount(homeDir, runtime.GOOS); hm != nil {
		mounts = append(mounts, *hm)
	}
	return mounts
}

// hostMiseInstallsMount returns the read-only mount sharing the host's mise
// installs directory with the docker/krun guest, or nil on non-Linux hosts:
// the guest is always Linux, so host binaries from another OS cannot run in it.
//
// Most mise-managed tools are upstream prebuilt releases targeting a broad
// glibc range and run fine on the guest's userland; a host-compiled tool
// linked against a newer host glibc can fail in the guest, in which case
// `mise uninstall <tool>@<version>` (removing the seeded symlink) followed by
// `mise install` inside the sandbox yields a guest-local install.
func hostMiseInstallsMount(homeDir, goos string) *DockerMount {
	if goos != "linux" {
		return nil
	}
	return &DockerMount{
		Source:   filepath.Join(homeDir, ".local", "share", "mise", "installs"),
		Dest:     hostMiseInstallsDest,
		ReadOnly: true,
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
