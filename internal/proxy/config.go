package proxy

import (
	"os"
	"path/filepath"

	"devsandbox/internal/config"
)

const (
	DefaultProxyPort   = 18080 // Use non-standard port to avoid conflicts with dev servers
	MaxPortRetries     = 50    // Number of ports to try if default is busy
	CADirName          = ".ca"
	CACertFile         = "ca.crt"
	CAKeyFile          = "ca.key"
	LogBaseDirName     = "logs"
	ProxyLogDirName    = "proxy"
	InternalLogDirName = "internal"
)

type Config struct {
	Enabled        bool
	Port           int
	CADir          string
	CACertPath     string
	CAKeyPath      string
	LogDir         string // logs/proxy - for proxy request logs
	InternalLogDir string // logs/internal - for internal error logs

	// LogReceivers is the list of remote log receiver configurations.
	LogReceivers []config.ReceiverConfig

	// LogAttributes are custom attributes added to all log entries.
	LogAttributes map[string]string
}

func NewConfig(sandboxBase string, port int) *Config {
	caDir := filepath.Join(sandboxBase, CADirName)
	logBase := filepath.Join(sandboxBase, LogBaseDirName)
	return &Config{
		Enabled:        true,
		Port:           port,
		CADir:          caDir,
		CACertPath:     filepath.Join(caDir, CACertFile),
		CAKeyPath:      filepath.Join(caDir, CAKeyFile),
		LogDir:         filepath.Join(logBase, ProxyLogDirName),
		InternalLogDir: filepath.Join(logBase, InternalLogDirName),
	}
}

func DefaultConfig(homeDir string) *Config {
	sandboxBase := filepath.Join(homeDir, ".local", "share", "devsandbox")
	return NewConfig(sandboxBase, DefaultProxyPort)
}

func (c *Config) EnsureCADir() error {
	return os.MkdirAll(c.CADir, 0o700)
}

func (c *Config) CAExists() bool {
	_, certErr := os.Stat(c.CACertPath)
	_, keyErr := os.Stat(c.CAKeyPath)
	return certErr == nil && keyErr == nil
}
