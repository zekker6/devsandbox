package proxy

import (
	"net/http"
	"sort"

	"devsandbox/internal/notice"
	"devsandbox/internal/source"
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
	// ResolvedValue returns the resolved credential value (for conflict detection).
	// Returns empty string if the injector is disabled or has no credential.
	ResolvedValue() string
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
			notice.Warn("unknown credential injector %q, skipping", name)
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

// CredentialSource is an alias for source.Source, preserved for backward compatibility.
type CredentialSource = source.Source

// ParseCredentialSource extracts a [*.source] sub-table from an injector config map.
// Returns nil if no source is configured, signaling the injector should use its defaults.
func ParseCredentialSource(cfg map[string]any) *CredentialSource {
	return source.Parse(cfg)
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
