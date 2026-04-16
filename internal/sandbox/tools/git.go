package tools

import (
	"bufio"
	"net/url"
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
	mode        GitMode
	projectDir  string
	gitRepoRoot string // main repo root when projectDir is a worktree; empty otherwise
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
	g.gitRepoRoot = globalCfg.GitRepoRoot

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

// gitDirSource returns the directory holding the real .git metadata.
// In worktree mode that is the main repo; otherwise it is the project dir.
func (g *Git) gitDirSource() string {
	if g.gitRepoRoot != "" && g.gitRepoRoot != g.projectDir {
		return g.gitRepoRoot
	}
	return g.projectDir
}

// readOnlyBindings returns bindings for readonly mode (safe gitconfig + read-only .git).
func (g *Git) readOnlyBindings(homeDir, sandboxHome string) []Binding {
	safeGitconfig := filepath.Join(sandboxHome, ".gitconfig.safe")

	bindings := []Binding{
		{
			Source:   safeGitconfig,
			Dest:     filepath.Join(homeDir, ".gitconfig"),
			Category: CategoryConfig,
			Optional: true, // Safe config might not exist if Setup failed
		},
	}

	// Mount .git as read-only to prevent commits. In worktree mode the
	// worktree's .git is a regular file pointing at the main repo's
	// .git/worktrees/<name>; we mount the main repo's .git so the
	// absolute gitdir: pointer resolves correctly inside the sandbox.
	gitDirHost := g.gitDirSource()
	isWorktree := g.gitRepoRoot != "" && g.gitRepoRoot != g.projectDir
	if gitDirHost != "" {
		gitDir := filepath.Join(gitDirHost, ".git")
		if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
			b := Binding{
				Source:   gitDir,
				Type:     MountBind, // Explicit: must be ro bind, not overlay
				ReadOnly: true,      // Security constraint of readonly mode
				Category: CategoryConfig,
			}
			// In worktree mode, pin Dest to the host path so the Docker
			// backend does not remap it under /home/sandboxuser. The
			// worktree's .git file contains an absolute gitdir: pointer
			// that must resolve inside the container.
			if isWorktree {
				b.Dest = gitDir
			}
			bindings = append(bindings, b)

			// Overlay .git/config with a sanitized copy. Embedded credentials in
			// remote URLs (e.g., https://ghp_xxxx@github.com/user/repo.git) and
			// any [credential] sections are stripped, but the rest of the config
			// is preserved verbatim so git itself can still read the repo —
			// otherwise even `git log` and pre-commit hooks fail with "unable to
			// access '.git/config': Permission denied".
			gitConfig := filepath.Join(gitDir, "config")
			if info, err := os.Stat(gitConfig); err == nil && info.Mode().IsRegular() {
				bindings = append(bindings, Binding{
					Source:   filepath.Join(sandboxHome, ".git-config.safe"),
					Dest:     gitConfig,
					Type:     MountBind,
					ReadOnly: true,
					Optional: true, // Setup may have skipped if source unreadable
					Category: CategoryConfig,
				})
			}
		}
	}

	return bindings
}

// readWriteBindings returns bindings for readwrite mode (full git access).
func (g *Git) readWriteBindings(homeDir, _ string) []Binding {
	bindings := []Binding{
		{
			Source:   filepath.Join(homeDir, ".gitconfig"),
			Category: CategoryConfig,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".git-credentials"),
			Category: CategoryConfig,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".ssh"),
			Category: CategoryConfig,
			Optional: true,
		},
		{
			Source:   filepath.Join(homeDir, ".gnupg"),
			Category: CategoryConfig,
			Optional: true,
		},
	}

	// In worktree mode the project mount only contains the worktree
	// directory. The worktree's .git is a file whose gitdir: pointer
	// references the main repo's .git — which must also be mounted
	// (writable, so commits can land). Pin Dest to the host path so
	// the Docker backend does not remap it under /home/sandboxuser.
	if g.gitRepoRoot != "" && g.gitRepoRoot != g.projectDir {
		gitDir := filepath.Join(g.gitRepoRoot, ".git")
		if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
			bindings = append(bindings, Binding{
				Source:   gitDir,
				Dest:     gitDir,
				Category: CategoryConfig,
			})
		}
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

// Setup implements ToolWithSetup to generate the safe gitconfig and the
// sanitized per-repo .git/config used by readonly mode.
func (g *Git) Setup(homeDir, sandboxHome string) error {
	// Only generate safe configs for readonly mode
	if g.mode != GitModeReadOnly {
		return nil
	}

	if err := g.setupUserGitconfig(homeDir, sandboxHome); err != nil {
		return err
	}

	return g.setupRepoGitconfig(sandboxHome)
}

// setupUserGitconfig generates the sanitized ~/.gitconfig overlay.
func (g *Git) setupUserGitconfig(homeDir, sandboxHome string) error {
	gitconfigPath := filepath.Join(homeDir, ".gitconfig")
	safeGitconfigPath := filepath.Join(sandboxHome, ".gitconfig.safe")

	if _, err := os.Stat(gitconfigPath); os.IsNotExist(err) {
		return nil
	}

	// Skip if safe config is already up to date.
	srcInfo, _ := os.Stat(gitconfigPath)
	if dstInfo, err := os.Stat(safeGitconfigPath); err == nil && srcInfo != nil && dstInfo.ModTime().After(srcInfo.ModTime()) {
		return nil
	}

	return generateSafeGitconfig(gitconfigPath, safeGitconfigPath)
}

// setupRepoGitconfig generates the sanitized per-repo .git/config overlay.
// Skips silently if there's no project, no .git/config, or the source is not
// a regular file (e.g. /dev/null overlay from a nested sandbox).
func (g *Git) setupRepoGitconfig(sandboxHome string) error {
	src := g.gitDirSource()
	if src == "" {
		return nil
	}

	repoConfigPath := filepath.Join(src, ".git", "config")
	safeRepoConfigPath := filepath.Join(sandboxHome, ".git-config.safe")

	srcInfo, err := os.Stat(repoConfigPath)
	if err != nil || !srcInfo.Mode().IsRegular() {
		return nil
	}

	if dstInfo, err := os.Stat(safeRepoConfigPath); err == nil && dstInfo.ModTime().After(srcInfo.ModTime()) {
		return nil
	}

	return generateSafeRepoConfig(repoConfigPath, safeRepoConfigPath)
}

// generateSafeRepoConfig writes a sanitized copy of a repo's .git/config to dst.
//
// It strips embedded credentials from remote URLs and drops any [credential]
// sections, but otherwise preserves the file verbatim — including [core],
// [branch], [remote] (minus credentials), and any custom sections — so that
// git operations like `git log`, `git status`, and pre-commit hooks continue
// to function inside the sandbox.
func generateSafeRepoConfig(src, dst string) error {
	file, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	var out strings.Builder
	scanner := bufio.NewScanner(file)

	inRemote := false
	inCredential := false

	for scanner.Scan() {
		raw := scanner.Text()
		trimmed := strings.TrimSpace(raw)

		if strings.HasPrefix(trimmed, "[") {
			lower := strings.ToLower(trimmed)
			inRemote = strings.HasPrefix(lower, "[remote")
			inCredential = strings.HasPrefix(lower, "[credential")
			if inCredential {
				continue // drop the section header itself
			}
			out.WriteString(raw)
			out.WriteByte('\n')
			continue
		}

		if inCredential {
			continue // drop section body
		}

		if inRemote {
			if key, value, ok := splitConfigKV(trimmed); ok {
				lk := strings.ToLower(key)
				if lk == "url" || lk == "pushurl" {
					indent := raw[:len(raw)-len(strings.TrimLeft(raw, " \t"))]
					out.WriteString(indent)
					out.WriteString(key)
					out.WriteString(" = ")
					out.WriteString(stripURLCredentials(value))
					out.WriteByte('\n')
					continue
				}
			}
		}

		out.WriteString(raw)
		out.WriteByte('\n')
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	return os.WriteFile(dst, []byte(out.String()), 0o644)
}

// splitConfigKV splits a "key = value" line. Returns ok=false if there's no '='.
func splitConfigKV(line string) (key, value string, ok bool) {
	k, v, found := strings.Cut(line, "=")
	if !found {
		return "", "", false
	}
	return strings.TrimSpace(k), strings.TrimSpace(v), true
}

// stripURLCredentials removes embedded credentials (userinfo) from http/https/ftp
// URLs. SSH/git URLs are returned unchanged because the user component there is
// the SSH login, not a secret. Non-URL strings (scp-style git refs, local paths)
// are also passed through.
func stripURLCredentials(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.User == nil {
		return rawURL
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "ftp", "ftps":
		u.User = nil
		return u.String()
	}
	return rawURL
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
