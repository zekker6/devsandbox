package proxy

import (
	"os"
	"path/filepath"
)

const (
	DefaultProxyPort = 8080
	CADirName        = ".ca"
	CACertFile       = "ca.crt"
	CAKeyFile        = "ca.key"
)

type Config struct {
	Enabled    bool
	Port       int
	LogEnabled bool
	CADir      string
	CACertPath string
	CAKeyPath  string
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
	}
}

func DefaultConfig(homeDir string) *Config {
	sandboxBase := filepath.Join(homeDir, ".sandboxes")
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
