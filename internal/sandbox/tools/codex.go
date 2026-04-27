package tools

import (
	"os"
	"os/exec"
	"path/filepath"
)

func init() {
	Register(&Codex{})
}

// Codex provides OpenAI Codex CLI integration.
// Mounts Codex config directory read-write. The directory defaults to
// ~/.codex and can be overridden via the CODEX_HOME environment variable;
// when set, the host value is passed through to the sandbox so codex
// resolves the same path inside.
type Codex struct{}

func (c *Codex) Name() string {
	return "codex"
}

func (c *Codex) Description() string {
	return "OpenAI Codex CLI assistant"
}

// configDir returns the host path of Codex's state directory, honoring
// CODEX_HOME when set and otherwise falling back to ~/.codex.
func (c *Codex) configDir(homeDir string) string {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return dir
	}
	return filepath.Join(homeDir, ".codex")
}

func (c *Codex) Available(homeDir string) bool {
	if _, err := exec.LookPath("codex"); err == nil {
		return true
	}

	if _, err := os.Stat(c.configDir(homeDir)); err == nil {
		return true
	}

	return false
}

func (c *Codex) Bindings(homeDir, sandboxHome string) []Binding {
	return []Binding{
		// Codex configuration, credentials, and logs
		{
			Source:   c.configDir(homeDir),
			Category: CategoryConfig,
			Optional: true,
		},
	}
}

func (c *Codex) Environment(homeDir, sandboxHome string) []EnvVar {
	if os.Getenv("CODEX_HOME") != "" {
		return []EnvVar{
			{Name: "CODEX_HOME", FromHost: true},
		}
	}
	return nil
}

func (c *Codex) ShellInit(shell string) string {
	return ""
}

func (c *Codex) Check(homeDir string) CheckResult {
	result := CheckResult{
		BinaryName:  "codex",
		InstallHint: "npm install -g @openai/codex",
	}

	path, err := exec.LookPath("codex")
	if err == nil {
		result.BinaryPath = path
	}

	codexDir := c.configDir(homeDir)
	if _, err := os.Stat(codexDir); err == nil {
		result.ConfigPaths = append(result.ConfigPaths, codexDir)
	}

	result.Available = result.BinaryPath != "" || len(result.ConfigPaths) > 0

	if !result.Available {
		result.Issues = append(result.Issues, "codex binary not found and no config exists")
	}

	return result
}
