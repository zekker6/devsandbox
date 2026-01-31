package proxy

import (
	"os"
	"path/filepath"
)

const (
	DefaultProxyPort = 18080 // Use non-standard port to avoid conflicts with dev servers
	MaxPortRetries   = 50    // Number of ports to try if default is busy
	CADirName        = ".ca"
	CACertFile       = "ca.crt"
	CAKeyFile        = "ca.key"
	LogDirName       = "proxy-logs"
)

type Config struct {
	Enabled    bool
	Port       int
	LogEnabled bool
	CADir      string
	CACertPath string
	CAKeyPath  string
	LogDir     string
}

func NewConfig(sandboxBase string, port int, logEnabled bool) *Config {
	caDir := filepath.Join(sandboxBase, CADirName)
	return &Config{
		Enabled:    true,
		Port:       port,
		LogEnabled: logEnabled,
		CADir:      caDir,
		CACertPath: filepath.Join(caDir, CACertFile),
		CAKeyPath:  filepath.Join(caDir, CAKeyFile),
		LogDir:     filepath.Join(sandboxBase, LogDirName),
	}
}

func DefaultConfig(homeDir string) *Config {
	sandboxBase := filepath.Join(homeDir, ".local", "share", "devsandbox")
	return NewConfig(sandboxBase, DefaultProxyPort, false)
}

func (c *Config) EnsureCADir() error {
	return os.MkdirAll(c.CADir, 0o700)
}

func (c *Config) CAExists() bool {
	_, certErr := os.Stat(c.CACertPath)
	_, keyErr := os.Stat(c.CAKeyPath)
	return certErr == nil && keyErr == nil
}
