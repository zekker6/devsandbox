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

// Git provides safe git configuration.
// Creates a sanitized gitconfig with only user.name and user.email,
// excluding credentials, signing keys, and other sensitive data.
type Git struct{}

func (g *Git) Name() string {
	return "git"
}

func (g *Git) Description() string {
	return "Git configuration (safe mode, no credentials)"
}

func (g *Git) Available(homeDir string) bool {
	// Check if user has a gitconfig
	gitconfig := filepath.Join(homeDir, ".gitconfig")
	_, err := os.Stat(gitconfig)
	return err == nil
}

func (g *Git) Bindings(homeDir, sandboxHome string) []Binding {
	safeGitconfig := filepath.Join(sandboxHome, ".gitconfig.safe")

	return []Binding{
		{
			Source:   safeGitconfig,
			Dest:     filepath.Join(homeDir, ".gitconfig"),
			ReadOnly: true,
			Optional: true, // Safe config might not exist if Setup failed
		},
	}
}

func (g *Git) Environment(homeDir, sandboxHome string) []EnvVar {
	return nil
}

func (g *Git) ShellInit(shell string) string {
	return ""
}

// Setup implements ToolWithSetup to generate the safe gitconfig.
func (g *Git) Setup(homeDir, sandboxHome string) error {
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
	result := CheckResult{
		BinaryName:  "git",
		InstallHint: "Install via system package manager (apt install git, pacman -S git)",
	}

	path, err := exec.LookPath("git")
	if err != nil {
		result.Issues = append(result.Issues, "git binary not found in PATH")
		return result
	}

	result.BinaryPath = path
	result.Available = true

	// Check for gitconfig
	gitconfig := filepath.Join(homeDir, ".gitconfig")
	if _, err := os.Stat(gitconfig); err == nil {
		result.ConfigPaths = append(result.ConfigPaths, gitconfig)
	} else {
		result.Issues = append(result.Issues, "no ~/.gitconfig found (will use defaults)")
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
