package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func init() {
	Register(&Codex{})
}

// Codex provides OpenAI Codex CLI integration.
// Mounts the Codex home with tmpoverlay to protect config and credentials
// (auth.json), and the sessions/ subdirectory with a persistent overlay so
// `codex resume <id>` can find sessions recorded in an earlier sandbox run.
//
// The directory defaults to ~/.codex and can be overridden via the CODEX_HOME
// environment variable; when set, the host value is passed through to the
// sandbox so codex resolves the same path inside.
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
		// Codex configuration, credentials, and logs — tmpoverlay protects
		// config.toml and auth.json.
		{
			Source:   c.configDir(homeDir),
			Category: CategoryConfig,
			Optional: true,
		},
		// sessions: persistent overlay so recorded sessions survive the sandbox.
		// Verified against codex-cli 0.144.6: resolving `codex resume <id>`
		// walks sessions/YYYY/MM/DD/ for the rollout file and needs nothing
		// else under the Codex home, so this is the whole persistence surface.
		{
			Source:   c.AgentSessionDir(homeDir),
			Category: CategoryData,
			Optional: true,
		},
	}
}

// AgentSessionDir implements ToolWithAgentSessionDir. It reuses configDir() so
// the bound cannot disagree with the sessions binding Bindings emits — the two
// must name the same directory or the herdr proxy would deny every real report
// carrying a path.
//
// Codex's herdr integration (v6) reports agent_session_id only, so this bound
// exists to confine a path report rather than to admit one seen in practice.
func (c *Codex) AgentSessionDir(homeDir string) string {
	return filepath.Join(c.configDir(homeDir), "sessions")
}

// Setup creates the host sessions directory so the persistent overlay declared
// for it actually applies.
//
// The binding is Optional, and the builder skips an Optional overlay whose host
// source is missing. A user who authenticated codex on the host but only ever
// runs it sandboxed therefore has the Codex home present — tmpoverlay, writes
// discarded — and sessions/ absent, so every rollout kept vanishing with the
// sandbox even with the split binding in place.
//
// Nothing is created when the Codex home itself is absent: no tmpoverlay is
// mounted in that case, so writes already land in the sandbox home and persist.
func (c *Codex) Setup(homeDir, _ string) error {
	if _, err := os.Stat(c.configDir(homeDir)); err != nil {
		return nil
	}
	sessions := c.AgentSessionDir(homeDir)
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		return fmt.Errorf("create codex sessions directory %s: %w", sessions, err)
	}
	return nil
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
	sessionsDir := c.AgentSessionDir(homeDir)
	if _, err := os.Stat(sessionsDir); err == nil {
		result.ConfigPaths = append(result.ConfigPaths, sessionsDir)
	}

	result.Available = result.BinaryPath != "" || len(result.ConfigPaths) > 0

	if !result.Available {
		result.Issues = append(result.Issues, "codex binary not found and no config exists")
	}

	return result
}

// Ensure interfaces are implemented.
var (
	_ Tool                    = (*Codex)(nil)
	_ ToolWithCheck           = (*Codex)(nil)
	_ ToolWithSetup           = (*Codex)(nil)
	_ ToolWithAgentSessionDir = (*Codex)(nil)
)
