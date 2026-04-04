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
// Mounts Codex config directory read-write.
type Codex struct{}

func (c *Codex) Name() string {
	return "codex"
}

func (c *Codex) Description() string {
	return "OpenAI Codex CLI assistant"
}

func (c *Codex) Available(homeDir string) bool {
	if _, err := exec.LookPath("codex"); err == nil {
		return true
	}

	if _, err := os.Stat(filepath.Join(homeDir, ".codex")); err == nil {
		return true
	}

	return false
}

func (c *Codex) Bindings(homeDir, sandboxHome string) []Binding {
	return []Binding{
		// Codex configuration, credentials, and logs
		{
			Source:   filepath.Join(homeDir, ".codex"),
			Category: CategoryConfig,
			Optional: true,
		},
	}
}

func (c *Codex) Environment(homeDir, sandboxHome string) []EnvVar {
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

	codexDir := filepath.Join(homeDir, ".codex")
	if _, err := os.Stat(codexDir); err == nil {
		result.ConfigPaths = append(result.ConfigPaths, codexDir)
	}

	result.Available = result.BinaryPath != "" || len(result.ConfigPaths) > 0

	if !result.Available {
		result.Issues = append(result.Issues, "codex binary not found and no config exists")
	}

	return result
}
