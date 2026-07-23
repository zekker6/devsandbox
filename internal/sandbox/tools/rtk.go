package tools

import (
	"os"
	"os/exec"
	"path/filepath"
)

func init() {
	Register(&RTK{})
}

// RTK provides rtk (Rust Token Killer) CLI proxy support.
//
// rtk resolves its directories through the `dirs` crate, i.e. XDG with the
// usual fallbacks. The sandbox sets XDG_CONFIG_HOME and XDG_DATA_HOME to
// $HOME/.config and $HOME/.local/share with $HOME being the host home path, so
// the host paths bound here are exactly the paths rtk resolves in-sandbox.
//
// Config gets the default tmpoverlay so the user's filters and config.toml are
// visible while `rtk config --create` cannot rewrite them; the data directory
// gets a persistent overlay so the tracking database survives across sandbox
// runs without the sandbox mutating the host copy.
type RTK struct{}

func (r *RTK) Name() string {
	return "rtk"
}

func (r *RTK) Description() string {
	return "rtk CLI proxy (token-optimized output)"
}

// configDir holds config.toml, the global filters.toml, and filters/*.toml.
func (r *RTK) configDir(homeDir string) string {
	return filepath.Join(homeDir, ".config", "rtk")
}

// dataDir holds history.db, trusted_filters.json, tee/, hook-audit.log and the
// telemetry markers.
func (r *RTK) dataDir(homeDir string) string {
	return filepath.Join(homeDir, ".local", "share", "rtk")
}

func (r *RTK) Available(homeDir string) bool {
	if _, err := exec.LookPath("rtk"); err == nil {
		return true
	}

	for _, dir := range []string{r.configDir(homeDir), r.dataDir(homeDir)} {
		if _, err := os.Stat(dir); err == nil {
			return true
		}
	}

	return false
}

func (r *RTK) Bindings(homeDir, sandboxHome string) []Binding {
	return []Binding{
		{Source: r.configDir(homeDir), Category: CategoryConfig, Optional: true},
		{Source: r.dataDir(homeDir), Category: CategoryData, Optional: true},
	}
}

func (r *RTK) Environment(homeDir, sandboxHome string) []EnvVar {
	return nil
}

func (r *RTK) ShellInit(shell string) string {
	return ""
}

func (r *RTK) Check(homeDir string) CheckResult {
	result := CheckResult{
		BinaryName:  "rtk",
		InstallHint: "mise use -g rtk",
	}

	path, err := exec.LookPath("rtk")
	if err == nil {
		result.BinaryPath = path
	}

	result.AddConfigPaths(r.configDir(homeDir), r.dataDir(homeDir))

	result.Available = result.BinaryPath != "" || len(result.ConfigPaths) > 0
	if !result.Available {
		result.AddIssue("rtk binary not found in PATH and no rtk config or data directory exists")
	}

	return result
}

// Ensure interfaces are implemented.
var (
	_ Tool          = (*RTK)(nil)
	_ ToolWithCheck = (*RTK)(nil)
)
