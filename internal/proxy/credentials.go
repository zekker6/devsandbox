package proxy

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CredentialInjector adds authentication to requests for specific domains.
// This allows the proxy to authenticate requests without exposing credentials
// to the sandbox environment.
type CredentialInjector interface {
	// Match returns true if this injector handles the request.
	Match(req *http.Request) bool
	// Inject adds credentials to the request (modifies in place).
	Inject(req *http.Request)
}

// ConfigurableInjector extends CredentialInjector with self-registration
// and configuration support. Each injector knows how to parse its own config
// from a map[string]any (the raw TOML section).
type ConfigurableInjector interface {
	CredentialInjector
	// Name returns the injector's registry name (e.g., "github").
	Name() string
	// Configure applies injector-specific configuration.
	// cfg is the raw TOML map from [proxy.credentials.<name>].
	Configure(cfg map[string]any)
	// Enabled returns whether this injector is active after configuration.
	Enabled() bool
}

// credentialRegistry maps injector names to factory functions.
var credentialRegistry = make(map[string]func() ConfigurableInjector)

// RegisterCredentialInjector registers a credential injector factory.
// Injectors should call this in their init() function.
func RegisterCredentialInjector(name string, factory func() ConfigurableInjector) {
	credentialRegistry[name] = factory
}

// BuildCredentialInjectors creates injectors from config.
// Only injectors that are explicitly enabled and have valid credentials are returned.
// Unknown injector names are logged to stderr and skipped.
func BuildCredentialInjectors(credentials map[string]any) []CredentialInjector {
	var injectors []CredentialInjector

	// Sort keys for deterministic ordering
	names := make([]string, 0, len(credentials))
	for name := range credentials {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		rawCfg := credentials[name]
		factory, ok := credentialRegistry[name]
		if !ok {
			fmt.Fprintf(os.Stderr, "Warning: unknown credential injector %q, skipping\n", name)
			continue
		}

		cfg, _ := rawCfg.(map[string]any)
		injector := factory()
		injector.Configure(cfg)

		if injector.Enabled() {
			injectors = append(injectors, injector)
		}
	}

	return injectors
}

// CredentialSource resolves a credential value from a configured source.
// Supports three source types: static value, environment variable, and file.
// When multiple fields are set, priority is: value > env > file.
type CredentialSource struct {
	Value string // static credential value
	Env   string // environment variable name
	File  string // path to file containing credential (supports ~ expansion)
}

// Resolve returns the credential value from the configured source.
// Checks fields in priority order: value, env, file.
// Returns empty string if nothing is configured or the value is missing.
func (s *CredentialSource) Resolve() string {
	if s.Value != "" {
		return s.Value
	}
	if s.Env != "" {
		return os.Getenv(s.Env)
	}
	if s.File != "" {
		path := expandHome(s.File)
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to read credential file %q: %v\n", s.File, err)
			return ""
		}
		return strings.TrimSpace(string(data))
	}
	return ""
}

// ParseCredentialSource extracts a [*.source] sub-table from an injector config map.
// Returns nil if no source is configured, signaling the injector should use its defaults.
func ParseCredentialSource(cfg map[string]any) *CredentialSource {
	raw, ok := cfg["source"].(map[string]any)
	if !ok {
		return nil
	}
	src := &CredentialSource{}
	if v, ok := raw["value"].(string); ok {
		src.Value = v
	}
	if v, ok := raw["env"].(string); ok {
		src.Env = v
	}
	if v, ok := raw["file"].(string); ok {
		src.File = v
	}
	return src
}

// expandHome expands a leading ~ to the user's home directory.
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

// RegisteredCredentialInjectors returns the names of all registered injectors.
func RegisteredCredentialInjectors() []string {
	names := make([]string, 0, len(credentialRegistry))
	for name := range credentialRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
