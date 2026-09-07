package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"devsandbox/internal/fsutil"
	"devsandbox/internal/notice"
)

func init() {
	Register(&Claude{})
}

// Claude provides Claude AI tool integration.
// Mounts Claude config directory with tmpoverlay (protects settings/credentials)
// and projects subdirectory with persistent overlay (preserves session history and memory).
type Claude struct{ Mounting }

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

// stateDir returns the host path of the Claude directory whose projects/
// subdirectory holds session history: CLAUDE_CONFIG_DIR when set, otherwise
// ~/.claude.
func (c *Claude) stateDir(homeDir string) string {
	if dir := c.configDir(); dir != "" {
		return dir
	}
	return filepath.Join(homeDir, ".claude")
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
				Source:   c.AgentSessionDir(homeDir),
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
				Source:   c.AgentSessionDir(homeDir),
				Category: CategoryData,
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
		// ~/.local/share/claude holds Claude Code's installed binaries
		// (versions/<X>). Mount read-only so the host's installation is the
		// single source of truth: claude's in-sandbox auto-updater cannot
		// leave partial/empty shadow files in a persistent overlay upper-dir
		// that would mask the real host binary in future sessions.
		Binding{
			Source:   filepath.Join(homeDir, ".local", "share", "claude"),
			Type:     MountBind,
			ReadOnly: true,
			Optional: true,
		},
	)

	return bindings
}

// AgentSessionDir implements ToolWithAgentSessionDir. It reuses configDir() so
// the bound this returns cannot disagree with the projects binding Bindings
// emits — the two must name the same directory or the herdr proxy would deny
// every real session report.
func (c *Claude) AgentSessionDir(homeDir string) string {
	return filepath.Join(c.stateDir(homeDir), "projects")
}

// Setup seeds the sandbox's private copy of ~/.claude.json and creates the
// host projects directory so the persistent overlay declared for it actually
// applies.
//
// The projects binding is Optional, and the builder skips an Optional overlay whose host
// source is missing. A user who authenticated claude on the host but only ever
// runs it sandboxed therefore has ~/.claude present — tmpoverlay, writes
// discarded — and projects/ absent, so every transcript would vanish with the
// sandbox even with the split binding in place, and a session herdr captured
// would resolve to nothing on restore.
//
// Nothing is created when the Claude directory itself is absent: no tmpoverlay
// is mounted in that case, so writes already land in the sandbox home and
// persist.
func (c *Claude) Setup(homeDir, sandboxHome string) error {
	if err := c.seedClaudeJSON(homeDir, sandboxHome); err != nil {
		return err
	}
	if _, err := os.Stat(c.stateDir(homeDir)); err != nil {
		return nil
	}
	projects := c.AgentSessionDir(homeDir)
	if err := os.MkdirAll(projects, 0o700); err != nil {
		return fmt.Errorf("create claude projects directory %s: %w", projects, err)
	}
	return nil
}

// seedClaudeJSON copies the host's ~/.claude.json into the sandbox home once.
//
// The file used to be a writable bind of the host file. The host's own Claude
// Code reads it back - mcpServers, project trust flags, oauth state - so a
// sandboxed agent could plant an MCP server the host then launched. A private
// copy gives the sandboxed Claude a file it can write without reaching the
// host; once it exists it is sandbox state and wins, so host changes after the
// first launch do not flow in and nothing flows back.
//
// A 0-byte destination counts as absent: bwrap creates a missing bind-mount
// target, so every sandbox home created while the bind existed holds an empty
// placeholder here, and Claude Code reads an empty file as no state at all.
//
// A symlink at the destination is replaced, never followed. The sandbox home
// is bound read-write into the sandbox, so a session can leave a link there
// naming any host file, and writing through it would truncate that file. It is
// replaced rather than refused because Setup errors abort a bwrap launch - a
// refusal would let the sandbox block its own next launch at will. The Alert
// is what tells the user it happened, and it has to be an Alert because Setup
// runs in PhaseRunning, where a plain Warn is diverted to the log file.
//
// Anything else that is not a regular file - a directory, a FIFO - is left in
// place with the same Alert treatment: only the sandbox can have put it there,
// and removing a directory could discard state a session wrote under it.
//
// Nothing is done under CLAUDE_CONFIG_DIR: Claude Code keeps its state under
// that directory, which the bindings already mount, and does not read the
// file.
func (c *Claude) seedClaudeJSON(homeDir, sandboxHome string) error {
	if c.configDir() != "" {
		return nil
	}
	dst := filepath.Join(sandboxHome, ".claude.json")
	info, err := os.Lstat(dst)
	switch {
	case err == nil && info.Mode().IsRegular() && info.Size() > 0:
		return nil
	case err == nil && info.Mode()&os.ModeSymlink == 0 && !info.Mode().IsRegular():
		kind := info.Mode().String()
		if info.IsDir() {
			kind = "directory"
		}
		notice.Alert("claude: %s is a %s, not a file; leaving it alone, so Claude Code in the sandbox will not find its state file", dst, kind)
		return nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("stat %s: %w", dst, err)
	}

	src := filepath.Join(homeDir, ".claude.json")
	data, err := os.ReadFile(src)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read %s: %w", src, err)
	}

	if info != nil && info.Mode()&os.ModeSymlink != 0 {
		notice.Alert("claude: replaced a symlink at %s with a fresh copy of ~/.claude.json", dst)
		if err := os.Remove(dst); err != nil {
			return fmt.Errorf("remove symlink %s: %w", dst, err)
		}
	}
	// WriteFileAtomic renames a fresh inode over the name, so even a link
	// planted between the Remove and the rename is replaced, not opened.
	if err := fsutil.WriteFileAtomic(dst, data, 0o600); err != nil {
		return fmt.Errorf("seed %s: %w", dst, err)
	}
	return nil
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

// Ensure interfaces are implemented.
var (
	_ Tool                    = (*Claude)(nil)
	_ ToolWithCheck           = (*Claude)(nil)
	_ ToolWithSetup           = (*Claude)(nil)
	_ ToolWithAgentSessionDir = (*Claude)(nil)
)
