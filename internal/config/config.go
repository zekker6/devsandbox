// Package config provides configuration file support for devsandbox.
package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config represents the devsandbox configuration file.
type Config struct {
	// Proxy contains proxy mode settings.
	Proxy ProxyConfig `toml:"proxy"`

	// Sandbox contains sandbox settings.
	Sandbox SandboxConfig `toml:"sandbox"`
}

// ProxyConfig contains proxy-related configuration.
type ProxyConfig struct {
	// Enabled sets whether proxy mode is enabled by default.
	Enabled bool `toml:"enabled"`

	// Port is the default proxy server port.
	Port int `toml:"port"`
}

// SandboxConfig contains sandbox-related configuration.
type SandboxConfig struct {
	// BasePath is the directory where sandbox homes are stored.
	// Defaults to ~/.local/share/devsandbox if not set.
	BasePath string `toml:"base_path"`
}

// DefaultConfig returns a Config with default values.
func DefaultConfig() *Config {
	return &Config{
		Proxy: ProxyConfig{
			Enabled: false,
			Port:    8080,
		},
		Sandbox: SandboxConfig{
			BasePath: "", // Empty means use default XDG path
		},
	}
}

// ConfigPath returns the path to the config file.
// Uses XDG_CONFIG_HOME/devsandbox/config.toml or ~/.config/devsandbox/config.toml
func ConfigPath() string {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "devsandbox", "config.toml")
}

// Load reads the configuration from the default path.
// Returns default config if file doesn't exist.
func Load() (*Config, error) {
	return LoadFrom(ConfigPath())
}

// LoadFrom reads the configuration from the specified path.
// Returns default config if file doesn't exist.
func LoadFrom(path string) (*Config, error) {
	cfg := DefaultConfig()

	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}

	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	// Expand ~ in base path
	if cfg.Sandbox.BasePath != "" {
		cfg.Sandbox.BasePath = expandHome(cfg.Sandbox.BasePath)
	}

	return cfg, nil
}

// expandHome expands ~ to the user's home directory.
func expandHome(path string) string {
	if len(path) == 0 || path[0] != '~' {
		return path
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}

	if len(path) == 1 {
		return home
	}

	if path[1] == '/' {
		return filepath.Join(home, path[2:])
	}

	return path
}

// GenerateDefault returns the default configuration as a TOML string
// with comments explaining each option.
func GenerateDefault() string {
	return `# devsandbox configuration file
# Location: ~/.config/devsandbox/config.toml

# Proxy mode settings
[proxy]
# Enable proxy mode by default (can be overridden with --proxy flag)
enabled = false

# Default proxy server port
port = 8080

# Sandbox settings
[sandbox]
# Base directory for sandbox homes
# Defaults to ~/.local/share/devsandbox if not set
# base_path = "~/.local/share/devsandbox"
`
}
