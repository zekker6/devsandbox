package sandbox

import (
	"os"
	"path/filepath"
	"regexp"
)

const (
	SandboxBase = ".sandboxes"
)

type Config struct {
	HomeDir     string
	ProjectDir  string
	ProjectName string
	SandboxHome string
	XDGRuntime  string

	// Proxy settings
	ProxyEnabled bool
	ProxyPort    int
	ProxyLog     bool
	ProxyCAPath  string
	GatewayIP    string
}

func NewConfig() (*Config, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	projectDir, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	projectName := SanitizeProjectName(filepath.Base(projectDir))
	sandboxHome := filepath.Join(homeDir, SandboxBase, projectName)

	xdgRuntime := os.Getenv("XDG_RUNTIME_DIR")
	if xdgRuntime == "" {
		xdgRuntime = filepath.Join("/run/user", string(rune(os.Getuid())))
	}

	return &Config{
		HomeDir:     homeDir,
		ProjectDir:  projectDir,
		ProjectName: projectName,
		SandboxHome: sandboxHome,
		XDGRuntime:  xdgRuntime,
	}, nil
}

var nonAlphanumericRe = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

func SanitizeProjectName(name string) string {
	return nonAlphanumericRe.ReplaceAllString(name, "_")
}

func (c *Config) EnsureSandboxDirs() error {
	dirs := []string{
		c.SandboxHome,
		filepath.Join(c.SandboxHome, ".config"),
		filepath.Join(c.SandboxHome, ".cache"),
		filepath.Join(c.SandboxHome, ".local", "share"),
		filepath.Join(c.SandboxHome, ".local", "state"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	return nil
}

func (c *Config) SandboxBase() string {
	return filepath.Join(c.HomeDir, SandboxBase)
}
