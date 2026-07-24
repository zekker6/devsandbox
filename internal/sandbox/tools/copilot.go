package tools

import (
	"os"
	"os/exec"
	"path/filepath"
)

func init() {
	Register(&Copilot{})
}

// Copilot provides GitHub Copilot integration for two distinct products that
// share the name:
//
//   - the standalone GitHub Copilot CLI (npm `@github/copilot`), a coding agent
//     invoked as `copilot` that keeps everything - config, MCP servers, sessions
//     and auth - under ~/.copilot. This is the one the shell wrappers wrap, so
//     its home is mounted read-write and persistent (CategoryData) or a
//     sandboxed `copilot` would run unauthenticated and lose every session.
//   - the older `gh copilot` extension, invoked through `gh`, whose config and
//     cache live under ~/.config/github-copilot and ~/.cache/github-copilot.
type Copilot struct{}

func (c *Copilot) Name() string {
	return "copilot"
}

func (c *Copilot) Description() string {
	return "GitHub Copilot integration"
}

// cliHome is where the standalone `copilot` CLI stores its config, sessions and
// auth. The gh extension uses the two paths below it instead.
func (c *Copilot) cliHome(homeDir string) string {
	return filepath.Join(homeDir, ".copilot")
}

func (c *Copilot) ghConfigDir(homeDir string) string {
	return filepath.Join(homeDir, ".config", "github-copilot")
}

func (c *Copilot) ghCacheDir(homeDir string) string {
	return filepath.Join(homeDir, ".cache", "github-copilot")
}

func (c *Copilot) Available(homeDir string) bool {
	paths := []string{
		c.cliHome(homeDir),
		c.ghConfigDir(homeDir),
		c.ghCacheDir(homeDir),
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}

	return false
}

func (c *Copilot) Bindings(homeDir, sandboxHome string) []Binding {
	return []Binding{
		// Standalone `copilot` CLI home: config, MCP config, sessions and auth.
		// Persistent so authentication carries in and sessions survive the
		// sandbox, which is what makes `copilot --resume` work across runs.
		{
			Source:   c.cliHome(homeDir),
			Category: CategoryData,
			Optional: true,
		},
		// gh copilot extension configuration.
		{
			Source:   c.ghConfigDir(homeDir),
			Category: CategoryConfig,
			Optional: true,
		},
		// gh copilot extension cache.
		{
			Source:   c.ghCacheDir(homeDir),
			Category: CategoryCache,
			Optional: true,
		},
	}
}

func (c *Copilot) Environment(homeDir, sandboxHome string) []EnvVar {
	return nil
}

func (c *Copilot) ShellInit(shell string) string {
	return ""
}

func (c *Copilot) Check(homeDir string) CheckResult {
	result := CheckResult{
		BinaryName:  "copilot",
		InstallHint: "Standalone CLI: npm install -g @github/copilot; or the gh extension: gh extension install github/gh-copilot",
	}

	if path, err := exec.LookPath("copilot"); err == nil {
		result.BinaryPath = path
	}

	for _, p := range []string{c.cliHome(homeDir), c.ghConfigDir(homeDir), c.ghCacheDir(homeDir)} {
		if _, err := os.Stat(p); err == nil {
			result.ConfigPaths = append(result.ConfigPaths, p)
		}
	}

	result.Available = result.BinaryPath != "" || len(result.ConfigPaths) > 0

	if !result.Available {
		result.Issues = append(result.Issues, "no copilot binary and no GitHub Copilot config directories found")
	}

	return result
}
