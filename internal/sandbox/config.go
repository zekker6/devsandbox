package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"devsandbox/internal/sandbox/mounts"
)

const (
	// SandboxBaseDir is the directory under ~/.local/share for sandbox data
	SandboxBaseDir = "devsandbox"
)

// Shell represents a supported shell type
type Shell string

const (
	ShellFish Shell = "fish"
	ShellBash Shell = "bash"
	ShellZsh  Shell = "zsh"
)

type Config struct {
	HomeDir     string
	ProjectDir  string
	ProjectName string
	SandboxBase string // Base directory for all sandboxes (e.g., ~/.local/share/devsandbox)
	SandboxRoot string // ~/.local/share/devsandbox/<project>
	SandboxHome string // ~/.local/share/devsandbox/<project>/home (mounted at $HOME)
	XDGRuntime  string
	Shell       Shell  // Detected shell (fish, bash, zsh)
	ShellPath   string // Full path to shell binary

	// Proxy settings
	ProxyEnabled bool
	ProxyPort    int
	ProxyCAPath  string
	GatewayIP    string
	// True if network namespace is isolated (pasta)
	NetworkIsolated bool

	// Overlay settings
	OverlayEnabled bool           // Global overlay enable/disable
	ToolsConfig    map[string]any // Per-tool configuration from config file

	// Custom mount settings
	MountsConfig *mounts.Engine // Compiled mount rules

	// ConfigVisibility controls how .devsandbox.toml is exposed to the sandbox
	// Values: "hidden", "readonly", "readwrite"
	ConfigVisibility string
}

// Options allows customizing sandbox configuration.
type Options struct {
	// BasePath overrides the default sandbox base directory.
	BasePath string
}

func NewConfig(opts *Options) (*Config, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	projectDir, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	baseDir := filepath.Join(homeDir, ".local", "share", SandboxBaseDir)
	if opts != nil && opts.BasePath != "" {
		baseDir = opts.BasePath
	}

	projectName := GenerateSandboxName(projectDir)

	sandboxRoot := filepath.Join(baseDir, projectName)
	sandboxHome := filepath.Join(sandboxRoot, "home")

	xdgRuntime := os.Getenv("XDG_RUNTIME_DIR")
	if xdgRuntime == "" {
		xdgRuntime = filepath.Join("/run/user", string(rune(os.Getuid())))
	}

	shell, shellPath := DetectShell()

	return &Config{
		HomeDir:     homeDir,
		ProjectDir:  projectDir,
		ProjectName: projectName,
		SandboxBase: baseDir,
		SandboxRoot: sandboxRoot,
		SandboxHome: sandboxHome,
		XDGRuntime:  xdgRuntime,
		Shell:       shell,
		ShellPath:   shellPath,
	}, nil
}

func DetectShell() (Shell, string) {
	shellEnv := os.Getenv("SHELL")
	if shellEnv == "" {
		shellEnv = "/bin/bash" // Default fallback
	}

	shellName := filepath.Base(shellEnv)

	switch {
	case strings.Contains(shellName, "fish"):
		return ShellFish, shellEnv
	case strings.Contains(shellName, "zsh"):
		return ShellZsh, shellEnv
	default:
		// Default to bash for unknown shells
		if shellEnv == "" || !strings.Contains(shellName, "bash") {
			return ShellBash, "/bin/bash"
		}
		return ShellBash, shellEnv
	}
}

var nonAlphanumericRe = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

func SanitizeProjectName(name string) string {
	return nonAlphanumericRe.ReplaceAllString(name, "_")
}

// GenerateSandboxName creates a unique sandbox name from project path.
// Format: <basename>-<short-hash> (e.g., "myproject-a1b2c3d4")
func GenerateSandboxName(projectDir string) string {
	basename := SanitizeProjectName(filepath.Base(projectDir))
	hash := sha256.Sum256([]byte(projectDir))
	shortHash := hex.EncodeToString(hash[:])[:8]
	return basename + "-" + shortHash
}

func (c *Config) EnsureSandboxDirs() error {
	dirs := []string{
		c.SandboxHome,
		filepath.Join(c.SandboxHome, ".config"),
		filepath.Join(c.SandboxHome, ".cache"),
		filepath.Join(c.SandboxHome, ".cache", "go-build"), // Go build cache (isolated)
		filepath.Join(c.SandboxHome, ".cache", "go-mod"),   // Go module cache (isolated)
		filepath.Join(c.SandboxHome, ".local", "share"),
		filepath.Join(c.SandboxHome, ".local", "state"),
		filepath.Join(c.SandboxHome, "go"), // GOPATH (isolated)
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	metadataPath := filepath.Join(c.SandboxRoot, MetadataFile)
	if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
		m := CreateMetadata(c)
		if err := SaveMetadata(m, c.SandboxRoot); err != nil {
			return err
		}
	} else {
		m, err := LoadMetadata(c.SandboxRoot)
		if err == nil {
			_ = m.UpdateLastUsed()
		}
	}

	return nil
}

// GetSandboxBase returns the base path for all sandboxes.
// Deprecated: Use the SandboxBase field directly.
func (c *Config) GetSandboxBase() string {
	return c.SandboxBase
}

// SandboxBasePath returns the base path for all sandboxes given a home directory
func SandboxBasePath(homeDir string) string {
	return filepath.Join(homeDir, ".local", "share", SandboxBaseDir)
}
