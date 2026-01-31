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

	// Overlay contains global overlayfs settings.
	Overlay OverlayConfig `toml:"overlay"`

	// Tools contains per-tool configuration as raw maps.
	// Each tool is responsible for parsing its own section.
	Tools map[string]any `toml:"tools"`

	// Logging contains remote logging settings.
	Logging LoggingConfig `toml:"logging"`
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

// OverlayConfig contains global overlayfs settings.
type OverlayConfig struct {
	// Enabled allows tools to use overlayfs for writable mounts.
	// When false, all overlay mounts are disabled (tools use read-only bind mounts).
	// Default: true
	Enabled *bool `toml:"enabled"`
}

// IsEnabled returns whether overlays are enabled (defaults to true).
func (o OverlayConfig) IsEnabled() bool {
	if o.Enabled == nil {
		return true
	}
	return *o.Enabled
}

// GetToolConfig returns the configuration map for a specific tool.
// Returns nil if the tool has no configuration.
func (c *Config) GetToolConfig(toolName string) map[string]any {
	if c.Tools == nil {
		return nil
	}
	if toolCfg, ok := c.Tools[toolName]; ok {
		if m, ok := toolCfg.(map[string]any); ok {
			return m
		}
	}
	return nil
}

// LoggingConfig contains remote logging configuration.
type LoggingConfig struct {
	// Receivers is a list of remote log destinations.
	Receivers []ReceiverConfig `toml:"receivers"`

	// Attributes are custom key-value pairs added to all log entries.
	Attributes map[string]string `toml:"attributes"`
}

// ReceiverConfig defines a single log receiver.
type ReceiverConfig struct {
	// Type is the receiver type: "syslog", "syslog-remote", or "otlp".
	Type string `toml:"type"`

	// Address is the remote server address (for syslog-remote and otlp).
	Address string `toml:"address"`

	// Endpoint is the OTLP endpoint URL (alias for Address, for otlp type).
	Endpoint string `toml:"endpoint"`

	// Protocol is the transport protocol:
	// - For syslog-remote: "udp" or "tcp" (default: udp)
	// - For otlp: "http" or "grpc" (default: http)
	Protocol string `toml:"protocol"`

	// Facility is the syslog facility (e.g., "local0").
	Facility string `toml:"facility"`

	// Tag is the syslog program tag.
	Tag string `toml:"tag"`

	// Headers are custom HTTP headers for OTLP.
	Headers map[string]string `toml:"headers"`

	// BatchSize is the OTLP batch size before flush.
	BatchSize int `toml:"batch_size"`

	// FlushInterval is the OTLP flush interval (e.g., "5s").
	FlushInterval string `toml:"flush_interval"`

	// Insecure disables TLS verification for gRPC connections.
	Insecure bool `toml:"insecure"`
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
		Overlay: OverlayConfig{
			Enabled: nil, // nil means enabled (default true)
		},
		Tools: make(map[string]any),
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

# Overlay filesystem settings (global)
[overlay]
# Master switch for overlay filesystem support
# When disabled, all tools use read-only bind mounts regardless of their settings
# enabled = true

# Tool-specific configuration
# Each tool can have its own section under [tools.<name>]

# Mise tool manager settings
[tools.mise]
# Allow mise to install/update tools via overlayfs
# When enabled, mise directories are mounted with a writable overlay layer
writable = false

# Persist mise changes across sandbox sessions
# When false: changes are discarded when sandbox exits (safer)
# When true: changes are stored in ~/.local/share/devsandbox/<project>/overlay/
persistent = false

# Remote logging configuration
# Proxy logs can be forwarded to remote destinations
[logging]

# Custom attributes added to all log entries
# [logging.attributes]
# environment = "development"
# host = "myhost"

# Example: Local syslog
# [[logging.receivers]]
# type = "syslog"
# facility = "local0"
# tag = "devsandbox"

# Example: Remote syslog server
# [[logging.receivers]]
# type = "syslog-remote"
# address = "logs.example.com:514"
# protocol = "udp"  # or "tcp"
# facility = "local0"
# tag = "devsandbox"

# Example: OpenTelemetry collector (HTTP)
# [[logging.receivers]]
# type = "otlp"
# endpoint = "http://localhost:4318/v1/logs"
# protocol = "http"  # default
# headers = { "Authorization" = "Bearer token" }
# batch_size = 100
# flush_interval = "5s"

# Example: OpenTelemetry collector (gRPC)
# [[logging.receivers]]
# type = "otlp"
# endpoint = "localhost:4317"
# protocol = "grpc"
# insecure = true  # disable TLS for local testing
# batch_size = 100
# flush_interval = "5s"
`
}
