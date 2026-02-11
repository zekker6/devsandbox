package tools

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func init() {
	Register(&Git{})
}

// GitMode defines the level of git access in the sandbox.
type GitMode string

const (
	// GitModeReadOnly mounts .git as read-only to prevent commits.
	// Provides safe gitconfig with only user.name and user.email.
	// No credentials, signing keys, or other sensitive data.
	GitModeReadOnly GitMode = "readonly"

	// GitModeReadWrite provides full git access including credentials,
	// SSH keys, and GPG keys for signing commits. .git is writable.
	GitModeReadWrite GitMode = "readwrite"

	// GitModeDisabled completely disables git configuration.
	// Git commands will work but without any user configuration.
	// .git remains writable (controlled by project bindings).
	GitModeDisabled GitMode = "disabled"
)

// ValidGitMode returns true if the given string is a valid git mode value.
func ValidGitMode(mode string) bool {
	switch strings.ToLower(mode) {
	case "readonly", "readwrite", "disabled":
		return true
	default:
		return false
	}
}

// Git provides configurable git configuration.
// Supports three modes: readonly (default), readwrite, and disabled.
type Git struct {
	mode       GitMode
	projectDir string
}

func (g *Git) Name() string {
	return "git"
}

func (g *Git) Description() string {
	switch g.mode {
	case GitModeReadWrite:
		return "Git configuration (full access with credentials)"
	case GitModeDisabled:
		return "Git configuration (disabled)"
	default:
		return "Git configuration (read-only, no commits)"
	}
}

func (g *Git) Available(homeDir string) bool {
	// Git tool is always "available" - it handles all modes including disabled
	// Check if git binary exists
	_, err := exec.LookPath("git")
	return err == nil
}

// Configure implements ToolWithConfig.
func (g *Git) Configure(globalCfg GlobalConfig, toolCfg map[string]any) {
	g.mode = GitModeReadOnly // default
	g.projectDir = globalCfg.ProjectDir

	if toolCfg == nil {
		return
	}

	if modeVal, ok := toolCfg["mode"]; ok {
		if modeStr, ok := modeVal.(string); ok {
			switch strings.ToLower(modeStr) {
			case "readwrite", "read-write", "rw":
				g.mode = GitModeReadWrite
			case "disabled", "none", "off":
				g.mode = GitModeDisabled
			default:
				g.mode = GitModeReadOnly
			}
		}
	}
}

func (g *Git) Bindings(homeDir, sandboxHome string) []Binding {
	switch g.mode {
	case GitModeDisabled:
		return nil

	case GitModeReadWrite:
		return g.readWriteBindings(homeDir, sandboxHome)

	default: // GitModeReadOnly
		return g.readOnlyBindings(homeDir, sandboxHome)
	}
}

// readOnlyBindings returns bindings for readonly mode (safe gitconfig + read-only .git).
func (g *Git) readOnlyBindings(homeDir, sandboxHome string) []Binding {
	safeGitconfig := filepath.Join(sandboxHome, ".gitconfig.safe")

	bindings := []Binding{
		{
			Source:   safeGitconfig,
			Dest:     filepath.Join(homeDir, ".gitconfig"),
			ReadOnly: true,
			Optional: true, // Safe config might not exist if Setup failed
		},
	}

	// Mount .git as read-only to prevent commits
	if g.projectDir != "" {
		gitDir := filepath.Join(g.projectDir, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			bindings = append(bindings, Binding{
				Source:   gitDir,
				ReadOnly: true,
				Optional: false, // .git must exist if we're mounting it
			})
		}
	}

	return bindings
}

// readWriteBindings returns bindings for readwrite mode (full git access).
func (g *Git) readWriteBindings(homeDir, _ string) []Binding {
	bindings := []Binding{
		// Full gitconfig (read-write)
		{
			Source:   filepath.Join(homeDir, ".gitconfig"),
			ReadOnly: false,
			Optional: true,
		},
		// Git credentials
		{
			Source:   filepath.Join(homeDir, ".git-credentials"),
			ReadOnly: true, // Read-only to prevent accidental modification
			Optional: true,
		},
		// SSH directory for git over SSH
		{
			Source:   filepath.Join(homeDir, ".ssh"),
			ReadOnly: true, // Read-only to protect private keys
			Optional: true,
		},
		// GPG for commit signing
		{
			Source:   filepath.Join(homeDir, ".gnupg"),
			ReadOnly: true, // Read-only to protect keys
			Optional: true,
		},
	}

	return bindings
}

func (g *Git) Environment(homeDir, sandboxHome string) []EnvVar {
	if g.mode == GitModeDisabled {
		return nil
	}

	// Pass through SSH auth socket for ssh-agent
	if g.mode == GitModeReadWrite {
		return []EnvVar{
			{Name: "SSH_AUTH_SOCK", FromHost: true},
			{Name: "GPG_TTY", FromHost: true},
		}
	}

	return nil
}

func (g *Git) ShellInit(shell string) string {
	return ""
}

// Setup implements ToolWithSetup to generate the safe gitconfig.
func (g *Git) Setup(homeDir, sandboxHome string) error {
	// Only generate safe gitconfig for readonly mode
	if g.mode != GitModeReadOnly {
		return nil
	}

	gitconfigPath := filepath.Join(homeDir, ".gitconfig")
	safeGitconfigPath := filepath.Join(sandboxHome, ".gitconfig.safe")

	// Check if gitconfig exists
	if _, err := os.Stat(gitconfigPath); os.IsNotExist(err) {
		return nil
	}

	// Check if safe config already exists and is newer than source
	srcInfo, _ := os.Stat(gitconfigPath)
	dstInfo, err := os.Stat(safeGitconfigPath)
	if err == nil && dstInfo.ModTime().After(srcInfo.ModTime()) {
		return nil // Safe config is up to date
	}

	return generateSafeGitconfig(gitconfigPath, safeGitconfigPath)
}

// generateSafeGitconfig creates a sanitized gitconfig with only safe settings.
func generateSafeGitconfig(src, dst string) error {
	// Try to get user info from git config
	name, _ := exec.Command("git", "config", "--global", "user.name").Output()
	email, _ := exec.Command("git", "config", "--global", "user.email").Output()

	// If git config fails, try parsing the file directly
	if len(name) == 0 || len(email) == 0 {
		parsedName, parsedEmail := parseGitconfig(src)
		if len(name) == 0 {
			name = []byte(parsedName)
		}
		if len(email) == 0 {
			email = []byte(parsedEmail)
		}
	}

	// Generate minimal safe gitconfig
	content := "[user]\n"
	if len(strings.TrimSpace(string(name))) > 0 {
		content += "\tname = " + strings.TrimSpace(string(name)) + "\n"
	}
	if len(strings.TrimSpace(string(email))) > 0 {
		content += "\temail = " + strings.TrimSpace(string(email)) + "\n"
	}

	return os.WriteFile(dst, []byte(content), 0o644)
}

func (g *Git) Check(homeDir string) CheckResult {
	result := CheckBinary("git", "Install via system package manager (apt install git, pacman -S git)")
	if !result.Available {
		return result
	}

	// Add mode info
	switch g.mode {
	case GitModeReadWrite:
		result.AddIssue("mode: readwrite (full access)")
	case GitModeDisabled:
		result.AddIssue("mode: disabled")
	default:
		result.AddIssue("mode: readonly (safe, default)")
	}

	// Check for gitconfig
	gitconfig := filepath.Join(homeDir, ".gitconfig")
	result.AddConfigPath(gitconfig)
	if len(result.ConfigPaths) == 0 {
		result.AddIssue("no ~/.gitconfig found (will use defaults)")
	}

	// Check for SSH and GPG in readwrite mode
	if g.mode == GitModeReadWrite {
		result.AddConfigPaths(
			filepath.Join(homeDir, ".ssh"),
			filepath.Join(homeDir, ".gnupg"),
		)
	}

	return result
}

// parseGitconfig extracts user.name and user.email from a gitconfig file.
func parseGitconfig(path string) (name, email string) {
	file, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	inUserSection := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if strings.HasPrefix(line, "[") {
			inUserSection = strings.HasPrefix(strings.ToLower(line), "[user]")
			continue
		}

		if !inUserSection {
			continue
		}

		if strings.HasPrefix(line, "name") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				name = strings.TrimSpace(parts[1])
			}
		} else if strings.HasPrefix(line, "email") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				email = strings.TrimSpace(parts[1])
			}
		}
	}

	return name, email
}
