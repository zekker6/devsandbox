// Package config provides configuration file support for devsandbox.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	// MinPort is the minimum valid port number.
	MinPort = 1
	// MaxPort is the maximum valid port number.
	MaxPort = 65535
	// MaxAskTimeout is the maximum ask timeout in seconds (10 minutes).
	MaxAskTimeout = 600
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

	// Include contains conditional config includes.
	Include []Include `toml:"include"`
}

// ProxyConfig contains proxy-related configuration.
type ProxyConfig struct {
	// Enabled sets whether proxy mode is enabled by default.
	// Uses pointer to distinguish between unset (nil) and explicit false.
	Enabled *bool `toml:"enabled"`

	// Port is the default proxy server port.
	Port int `toml:"port"`

	// Filter contains HTTP request filtering configuration.
	Filter ProxyFilterConfig `toml:"filter"`
}

// IsEnabled returns whether proxy is enabled (defaults to false).
func (p ProxyConfig) IsEnabled() bool {
	if p.Enabled == nil {
		return false
	}
	return *p.Enabled
}

// ProxyFilterConfig contains HTTP filtering settings.
// Filtering is enabled when DefaultAction is set.
type ProxyFilterConfig struct {
	// DefaultAction is the action when no rule matches.
	// Setting this enables filtering:
	// - "block": block unmatched requests (whitelist behavior)
	// - "allow": allow unmatched requests (blacklist behavior)
	// - "ask": prompt user for unmatched requests
	DefaultAction string `toml:"default_action"`

	// AskTimeout is the timeout in seconds for ask mode decisions.
	// Default: 30
	AskTimeout int `toml:"ask_timeout"`

	// CacheDecisions enables caching of ask mode decisions for the session.
	// Default: true
	CacheDecisions *bool `toml:"cache_decisions"`

	// Rules is the list of filter rules.
	Rules []ProxyFilterRule `toml:"rules"`
}

// ProxyFilterRule defines a single filtering rule.
type ProxyFilterRule struct {
	// Pattern is the pattern to match (exact, glob, or regex).
	Pattern string `toml:"pattern"`

	// Action specifies what to do: "allow", "block", or "ask".
	Action string `toml:"action"`

	// Scope defines what to match: "host", "path", or "url".
	// Default: "host"
	Scope string `toml:"scope"`

	// Type specifies pattern type: "exact", "glob", or "regex".
	// Default: "glob"
	Type string `toml:"type"`

	// Reason is shown when blocking a request.
	Reason string `toml:"reason"`
}

// ConfigVisibility defines how .devsandbox.toml is exposed to the sandbox.
type ConfigVisibility string

const (
	// ConfigVisibilityHidden hides the config file from the sandbox (default).
	ConfigVisibilityHidden ConfigVisibility = "hidden"
	// ConfigVisibilityReadOnly exposes the config file as read-only.
	ConfigVisibilityReadOnly ConfigVisibility = "readonly"
	// ConfigVisibilityReadWrite exposes the config file as read-write.
	ConfigVisibilityReadWrite ConfigVisibility = "readwrite"
)

// SandboxConfig contains sandbox-related configuration.
type SandboxConfig struct {
	// BasePath is the directory where sandbox homes are stored.
	// Defaults to ~/.local/share/devsandbox if not set.
	BasePath string `toml:"base_path"`

	// Mounts contains custom mount rules.
	Mounts MountsConfig `toml:"mounts"`

	// ConfigVisibility controls how .devsandbox.toml is exposed to the sandbox.
	// Values: "hidden" (default), "readonly", "readwrite"
	ConfigVisibility ConfigVisibility `toml:"config_visibility"`
}

// GetConfigVisibility returns the config visibility (defaults to hidden).
func (s SandboxConfig) GetConfigVisibility() ConfigVisibility {
	if s.ConfigVisibility == "" {
		return ConfigVisibilityHidden
	}
	return s.ConfigVisibility
}

// MountsConfig defines custom mount rules for the sandbox.
type MountsConfig struct {
	// Rules is the list of mount rules.
	Rules []MountRule `toml:"rules"`
}

// MountRule defines a single mount rule.
type MountRule struct {
	// Pattern is the glob pattern to match paths.
	// Supports ~ expansion and ** for recursive matching.
	// Examples: "~/.config/myapp", "**/secrets/**", "/opt/tools"
	Pattern string `toml:"pattern"`

	// Mode specifies how to handle matching paths:
	// - "hidden": overlay with /dev/null (hide the file/directory)
	// - "readonly": mount as read-only bind mount
	// - "readwrite": mount as read-write bind mount
	// - "overlay": mount with persistent overlayfs (writes saved to sandbox)
	// - "tmpoverlay": mount with tmpfs overlay (writes discarded on exit)
	// Default: "readonly"
	Mode string `toml:"mode"`
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
	toolCfg, ok := c.Tools[toolName]
	if !ok {
		return nil
	}
	m, ok := toolCfg.(map[string]any)
	if !ok {
		return nil
	}
	return m
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
			Enabled: nil, // nil means disabled (default false)
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

// configDir returns the devsandbox config directory path.
// Uses XDG_CONFIG_HOME/devsandbox or ~/.config/devsandbox
func configDir() string {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "devsandbox")
}

// ConfigPath returns the path to the config file.
func ConfigPath() string {
	return filepath.Join(configDir(), "config.toml")
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

	// Validate configuration values
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}

// Validate checks configuration values for security and correctness.
func (c *Config) Validate() error {
	// Validate proxy port
	if c.Proxy.Port != 0 {
		if c.Proxy.Port < MinPort || c.Proxy.Port > MaxPort {
			return fmt.Errorf("proxy.port must be between %d and %d, got %d", MinPort, MaxPort, c.Proxy.Port)
		}
	}

	// Validate ask timeout (must be positive if set)
	if c.Proxy.Filter.AskTimeout < 0 {
		return fmt.Errorf("proxy.filter.ask_timeout cannot be negative, got %d", c.Proxy.Filter.AskTimeout)
	}
	if c.Proxy.Filter.AskTimeout > MaxAskTimeout {
		return fmt.Errorf("proxy.filter.ask_timeout cannot exceed %d seconds, got %d", MaxAskTimeout, c.Proxy.Filter.AskTimeout)
	}

	// Validate base path (no path traversal)
	if c.Sandbox.BasePath != "" {
		if err := validatePath(c.Sandbox.BasePath); err != nil {
			return fmt.Errorf("sandbox.base_path: %w", err)
		}
	}

	// Validate config visibility
	validVisibilities := map[ConfigVisibility]bool{
		"": true, ConfigVisibilityHidden: true, ConfigVisibilityReadOnly: true, ConfigVisibilityReadWrite: true,
	}
	if !validVisibilities[c.Sandbox.ConfigVisibility] {
		return fmt.Errorf("sandbox.config_visibility must be 'hidden', 'readonly', or 'readwrite', got %q", c.Sandbox.ConfigVisibility)
	}

	// Validate filter rules
	validActions := map[string]bool{"allow": true, "block": true, "ask": true, "": true}
	if c.Proxy.Filter.DefaultAction != "" && !validActions[c.Proxy.Filter.DefaultAction] {
		return fmt.Errorf("proxy.filter.default_action must be 'allow', 'block', or 'ask', got %q", c.Proxy.Filter.DefaultAction)
	}

	for i, rule := range c.Proxy.Filter.Rules {
		if rule.Pattern == "" {
			return fmt.Errorf("proxy.filter.rules[%d].pattern cannot be empty", i)
		}
		if rule.Action != "" && !validActions[rule.Action] {
			return fmt.Errorf("proxy.filter.rules[%d].action must be 'allow', 'block', or 'ask', got %q", i, rule.Action)
		}
	}

	// Validate mount rules
	validMountModes := map[string]bool{
		"hidden": true, "readonly": true, "readwrite": true,
		"overlay": true, "tmpoverlay": true, "": true,
	}
	for i, rule := range c.Sandbox.Mounts.Rules {
		if rule.Pattern == "" {
			return fmt.Errorf("sandbox.mounts.rules[%d].pattern cannot be empty", i)
		}
		if !validMountModes[rule.Mode] {
			return fmt.Errorf("sandbox.mounts.rules[%d].mode must be 'hidden', 'readonly', 'readwrite', 'overlay', or 'tmpoverlay', got %q", i, rule.Mode)
		}
	}

	return nil
}

// validatePath checks a path for security issues like path traversal.
func validatePath(path string) error {
	// Check for path traversal attempts in original path
	// We check before cleaning because Clean() resolves ".." which hides the attempt
	if strings.Contains(path, "..") {
		return fmt.Errorf("path contains traversal sequence: %q", path)
	}

	// Clean the path
	cleaned := filepath.Clean(path)

	// Path must be absolute
	if !filepath.IsAbs(cleaned) {
		return fmt.Errorf("path must be absolute: %q", path)
	}

	return nil
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
	if path[1] != '/' {
		return path
	}
	return filepath.Join(home, path[2:])
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

# HTTP request filtering (requires proxy mode)
# Filtering is enabled when default_action is set.
# [proxy.filter]
# Default action when no rule matches (enables filtering):
# - "block": block unmatched requests (whitelist behavior)
# - "allow": allow unmatched requests (blacklist behavior)
# - "ask": prompt user for unmatched requests
# default_action = "block"

# Timeout in seconds for ask mode (default: 30)
# ask_timeout = 30

# Cache ask mode decisions for session (default: true)
# cache_decisions = true

# Filter rules (evaluated in order, first match wins)
# Defaults: type = "glob", scope = "host"
# [[proxy.filter.rules]]
# pattern = "*.github.com"
# action = "allow"

# [[proxy.filter.rules]]
# pattern = "api.anthropic.com"
# action = "allow"

# [[proxy.filter.rules]]
# pattern = "*.tracking.io"
# action = "block"
# reason = "Tracking domain blocked"

# Sandbox settings
[sandbox]
# Base directory for sandbox homes
# Defaults to ~/.local/share/devsandbox if not set
# base_path = "~/.local/share/devsandbox"

# Control visibility of .devsandbox.toml inside the sandbox
# - "hidden" (default): config file is not visible to sandboxed processes
# - "readonly": config file is visible but read-only
# - "readwrite": config file is visible and writable
# config_visibility = "hidden"

# Custom mount rules - control how paths are mounted in the sandbox
# Note: Home directory paths (~/.ssh, ~/.aws, etc.) are NOT mounted by default.
# .env files in the project are hidden by default (hardcoded).
#
# Use these rules to:
# - Mount additional paths from the host filesystem
# - Hide sensitive files within the project
# - Control read/write access to specific paths
#
# Modes:
# - "hidden": overlay with /dev/null (hide the file/directory)
# - "readonly": mount as read-only bind mount
# - "readwrite": mount as read-write bind mount
# - "overlay": mount with persistent overlayfs (writes saved to sandbox)
# - "tmpoverlay": mount with tmpfs overlay (writes discarded on exit)
#
# Patterns support glob syntax with ** for recursive matching and ~ for home.

# Example: Mount app config directory as read-only
# [[sandbox.mounts.rules]]
# pattern = "~/.config/myapp"
# mode = "readonly"

# Example: Hide secrets directory within the project
# [[sandbox.mounts.rules]]
# pattern = "**/secrets/**"
# mode = "hidden"

# Example: Mount cache with overlay (persistent writes)
# [[sandbox.mounts.rules]]
# pattern = "~/.cache/myapp"
# mode = "overlay"

# Overlay filesystem settings (global)
[overlay]
# Master switch for overlay filesystem support
# When disabled, all tools use read-only bind mounts regardless of their settings
# enabled = true

# Tool-specific configuration
# Each tool can have its own section under [tools.<name>]

# Git access settings
[tools.git]
# Git access mode:
# - "readonly" (default): .git mounted read-only (no commits), no credentials
# - "readwrite": full git access with credentials, SSH keys, GPG keys
# - "disabled": no git config, but .git writable (commits need --author)
mode = "readonly"

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

// LoadOptions configures the config loading behavior.
type LoadOptions struct {
	// TrustStore is used for local config trust verification.
	// If nil, local configs are skipped.
	TrustStore *TrustStore

	// SkipLocalConfig disables loading .devsandbox.toml even if trusted.
	SkipLocalConfig bool

	// OnLocalConfigPrompt is called when a local config needs trust approval.
	// If nil, PromptTrustStdio is used.
	// Return true to trust, false to skip.
	OnLocalConfigPrompt func(projectDir, content string, changed bool) (bool, error)
}

// LocalConfigFile is the name of the local config file.
const LocalConfigFile = ".devsandbox.toml"

// LoadConfig loads the full configuration for the current working directory.
// It loads the trust store and config together, reducing boilerplate.
// Returns the config, trust store, and the resolved project directory.
func LoadConfig() (*Config, *TrustStore, string, error) {
	projectDir, err := os.Getwd()
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to get current directory: %w", err)
	}

	trustStore, err := LoadTrustStore(TrustStorePath())
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to load trust store: %w", err)
	}

	cfg, err := LoadWithProjectDir(ConfigPath(), projectDir, &LoadOptions{
		TrustStore: trustStore,
	})
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to load config: %w", err)
	}

	return cfg, trustStore, projectDir, nil
}

// LoadWithProjectDir loads configuration with project-specific overrides.
// It loads: global config -> matching includes -> local .devsandbox.toml (if trusted)
func LoadWithProjectDir(globalPath, projectDir string, opts *LoadOptions) (*Config, error) {
	if opts == nil {
		opts = &LoadOptions{}
	}

	cfg, err := LoadFrom(globalPath)
	if err != nil {
		return nil, err
	}

	if len(cfg.Include) > 0 {
		cfg, err = applyIncludes(cfg, projectDir)
		if err != nil {
			return nil, err
		}
	}

	if opts.SkipLocalConfig {
		return cfg, nil
	}

	localCfg, err := loadLocalConfig(projectDir, opts)
	if err != nil {
		return nil, err
	}
	if localCfg != nil {
		cfg = mergeConfigs(cfg, localCfg)
	}
	return cfg, nil
}

// applyIncludes processes matching include files and merges them into cfg.
func applyIncludes(cfg *Config, projectDir string) (*Config, error) {
	matching, err := getMatchingIncludes(cfg.Include, projectDir)
	if err != nil {
		return nil, fmt.Errorf("invalid include configuration: %w", err)
	}
	for _, inc := range matching {
		includePath := expandHome(inc.Path)
		includeCfg, err := loadIncludeFile(includePath)
		if err != nil {
			return nil, fmt.Errorf("failed to load include %s: %w", includePath, err)
		}
		cfg = mergeConfigs(cfg, includeCfg)
	}
	return cfg, nil
}

// loadIncludeFile loads a config file for inclusion.
// Returns error if file doesn't exist or has parse errors.
func loadIncludeFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("file not found")
	}
	if err != nil {
		return nil, err
	}

	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse error: %w", err)
	}

	// Validate included config
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validation error: %w", err)
	}

	// Warn if include file has nested includes
	if len(cfg.Include) > 0 {
		fmt.Fprintf(os.Stderr, "Warning: nested includes in %s are ignored\n", path)
		cfg.Include = nil
	}

	return cfg, nil
}

// loadLocalConfig loads and validates the local .devsandbox.toml file.
func loadLocalConfig(projectDir string, opts *LoadOptions) (*Config, error) {
	localPath := filepath.Join(projectDir, LocalConfigFile)

	data, err := os.ReadFile(localPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read local config: %w", err)
	}

	if opts.TrustStore == nil {
		return nil, fmt.Errorf("TrustStore is required to load local config")
	}

	hash := hashBytes(data)
	if err := ensureTrusted(projectDir, hash, data, opts); err != nil {
		if errors.Is(err, errConfigNotTrusted) {
			return nil, nil // Skip untrusted config
		}
		return nil, err
	}

	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse local config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid local config: %w", err)
	}

	if len(cfg.Include) > 0 {
		fmt.Fprintf(os.Stderr, "Warning: includes in local config are ignored\n")
		cfg.Include = nil
	}

	return cfg, nil
}

// ensureTrusted verifies trust for a local config, prompting if needed.
// Returns nil if trusted/approved, error if denied or prompt failed.
func ensureTrusted(projectDir, hash string, data []byte, opts *LoadOptions) error {
	existing := opts.TrustStore.GetTrusted(projectDir)
	if existing != nil && existing.Hash == hash {
		return nil // Already trusted
	}

	promptFn := opts.OnLocalConfigPrompt
	if promptFn == nil {
		promptFn = PromptTrustStdio
	}

	changed := existing != nil // Has entry but hash differs
	approved, err := promptFn(projectDir, string(data), changed)
	if err != nil {
		return fmt.Errorf("trust prompt failed: %w", err)
	}
	if !approved {
		fmt.Fprintf(os.Stderr, "Skipping local config (not trusted)\n")
		return errConfigNotTrusted
	}

	opts.TrustStore.AddTrust(projectDir, hash)
	if err := opts.TrustStore.Save(); err != nil {
		return fmt.Errorf("failed to save trust approval: %w (trust not persisted, you will be prompted again)", err)
	}
	return nil
}

// errConfigNotTrusted is a sentinel error for untrusted configs that should be skipped.
var errConfigNotTrusted = fmt.Errorf("config not trusted")

// hashBytes computes SHA256 hash of data.
func hashBytes(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
