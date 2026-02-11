package proxy

import (
	"net"
	"os"
	"path/filepath"

	"devsandbox/internal/config"
	"devsandbox/internal/logging"
)

const (
	// DefaultBindAddress is used for bwrap mode (localhost only).
	DefaultBindAddress = "127.0.0.1"
	// DockerBridgeInterface is the default Docker bridge network interface.
	DockerBridgeInterface = "docker0"
)

const (
	DefaultProxyPort   = 8080 // Standard proxy port, matches config.toml default
	MaxPortRetries     = 50   // Number of ports to try if default is busy
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
	BindAddress    string // IP to bind to (default "127.0.0.1"). For Docker, use DockerBridgeIP().
	CADir          string
	CACertPath     string
	CAKeyPath      string
	LogDir         string // logs/proxy - for proxy request logs
	InternalLogDir string // logs/internal - for internal error logs

	// LogReceivers is the list of remote log receiver configurations.
	LogReceivers []config.ReceiverConfig

	// LogAttributes are custom attributes added to all log entries.
	LogAttributes map[string]string

	// Filter contains HTTP request filtering configuration.
	Filter *FilterConfig

	// CredentialInjectors add authentication to requests for specific domains.
	// Built by BuildCredentialInjectors() from [proxy.credentials] config.
	// If nil/empty, no credential injection is performed.
	CredentialInjectors []CredentialInjector

	// Dispatcher is an optional shared log dispatcher for remote forwarding.
	// If set, the server uses it instead of creating its own from LogReceivers.
	Dispatcher *logging.Dispatcher
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

func (c *Config) EnsureCADir() error {
	return os.MkdirAll(c.CADir, 0o700)
}

func (c *Config) CAExists() bool {
	_, certErr := os.Stat(c.CACertPath)
	_, keyErr := os.Stat(c.CAKeyPath)
	return certErr == nil && keyErr == nil
}

// GetBindAddress returns the bind address, defaulting to 127.0.0.1.
func (c *Config) GetBindAddress() string {
	if c.BindAddress != "" {
		return c.BindAddress
	}
	return DefaultBindAddress
}

// DockerBridgeIP returns the IP address of the Docker bridge interface (docker0).
// This is used when running in Docker mode so containers can reach the proxy.
// Returns empty string if the interface doesn't exist or has no IP.
func DockerBridgeIP() string {
	iface, err := net.InterfaceByName(DockerBridgeInterface)
	if err != nil {
		return ""
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}

	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok {
			if ipv4 := ipNet.IP.To4(); ipv4 != nil {
				return ipv4.String()
			}
		}
	}

	return ""
}
