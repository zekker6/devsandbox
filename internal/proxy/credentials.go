package proxy

import (
	"fmt"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"

	"devsandbox/internal/config"
	"devsandbox/internal/notice"
	"devsandbox/internal/source"

	"github.com/bmatcuk/doublestar/v4"
)

// Preset holds default values for a named credential injector preset.
// User TOML at [proxy.credentials.<name>] is overlaid on the preset
// when <name> matches a registered preset (or via explicit `preset = "..."`).
type Preset struct {
	Host          string
	Header        string
	ValueFormat   string
	DefaultSource *source.Source
}

// presetRegistry stores built-in presets. Populated via RegisterPreset
// (typically from init() functions).
var presetRegistry = map[string]Preset{}

// init registers built-in credential injector presets, and what a
// [proxy.credentials.<name>] section holds, so a typo there is reported and a
// value of the wrong type is rejected instead of both being ignored.
func init() {
	config.RegisterFreeFormSchema("proxy.credentials", credentialSchema)

	RegisterPreset("github", Preset{
		Host:          "api.github.com",
		Header:        "Authorization",
		ValueFormat:   "Bearer {token}",
		DefaultSource: &source.Source{Env: "GITHUB_TOKEN"},
		// GH_TOKEN fallback handled by applyGitHubFallback in
		// BuildCredentialInjectors, gated on preset name == "github" and
		// the user not setting an explicit [...source] sub-table.
	})
}

// credentialConfig is a [proxy.credentials.<name>] section. Every field is
// optional: what a preset supplies, the user need not repeat.
type credentialConfig struct {
	// Preset names the preset to build on. Defaults to the section name when
	// that names a registered preset.
	Preset string `toml:"preset"`
	// Host is the request host to match, exact or a doublestar glob.
	Host string `toml:"host"`
	// Header is the header to write on a match.
	Header string `toml:"header"`
	// ValueFormat renders the header value, with `{token}` standing for the
	// resolved credential. Defaults to the token alone.
	ValueFormat string `toml:"value_format"`
	// Overwrite replaces a header the request already carries.
	Overwrite bool `toml:"overwrite"`
	// Enabled turns the injector on. Injectors are off by default.
	Enabled bool `toml:"enabled"`
	// Source resolves the credential. Left unset, a preset's default applies.
	Source *source.Source `toml:"source"`
}

// credentialSchema reports what a [proxy.credentials.<name>] section holds. The
// section name is the injector's name and is the user's to choose, so every
// name is accepted.
func credentialSchema(string) (reflect.Type, bool) {
	return reflect.TypeFor[credentialConfig](), true
}

// RegisterPreset registers a credential injector preset under the given name.
// Built-in presets call this from init(). Last writer wins for a given name.
func RegisterPreset(name string, p Preset) { presetRegistry[name] = p }

// lookupPreset returns the registered preset for name and whether it exists.
func lookupPreset(name string) (Preset, bool) {
	p, ok := presetRegistry[name]
	return p, ok
}

// GenericInjector is the universal credential injector. It matches a request
// against a host pattern (exact or doublestar glob), and on match writes a
// configured header whose value is rendered from a `value_format` template
// with the literal placeholder `{token}` replaced by the resolved credential.
//
// Fields are unexported because the canonical construction path is
// BuildCredentialInjectors; tests construct instances directly via the
// struct literal in the same package.
type GenericInjector struct {
	name        string
	host        string
	matcher     func(string) bool // compiled from host (exact == or doublestar.Match)
	isExact     bool              // true when host has no glob metachars
	header      string            // already canonicalized via http.CanonicalHeaderKey
	valueFormat string            // contains literal `{token}` placeholder
	overwrite   bool
	token       string
	enabled     bool
}

// Match reports whether this injector should run for req. The request host's
// port is stripped before matching.
func (g *GenericInjector) Match(req *http.Request) bool {
	return g.matcher(NormalizeHost(req.URL.Host))
}

// Inject writes the configured header on req and returns true. It returns
// false (no-op) when the resolved token is empty or when overwrite=false and
// the header is already set.
func (g *GenericInjector) Inject(req *http.Request) bool {
	if g.token == "" {
		return false
	}
	if !g.overwrite && req.Header.Get(g.header) != "" {
		return false
	}
	req.Header.Set(g.header, strings.ReplaceAll(g.valueFormat, "{token}", g.token))
	return true
}

// Header returns the configured header name (e.g., "Authorization"). Used by
// audit events.
func (g *GenericInjector) Header() string { return g.header }

// exactHostSpecificity ranks exact-host injectors above any glob.
// Safe upper bound: DNS hostnames are limited to 253 chars, so any glob's
// literal-character score (len(host) - count('*')) is at most 253.
const exactHostSpecificity = 1000

// Specificity returns a numeric ranking used to order injectors when several
// could match the same request. Exact hosts score exactHostSpecificity; globs
// score by the number of literal (non-`*`) characters in the pattern. Higher
// wins.
func (g *GenericInjector) Specificity() int {
	if g.isExact {
		return exactHostSpecificity
	}
	return len(g.host) - strings.Count(g.host, "*")
}

// ResolvedValue returns the resolved credential when the injector is enabled,
// or an empty string when disabled. Used by redaction conflict detection.
func (g *GenericInjector) ResolvedValue() string {
	if !g.enabled {
		return ""
	}
	return g.token
}

// Name returns the injector's configured name (the TOML section key).
func (g *GenericInjector) Name() string { return g.name }

// CredentialInjector adds authentication to requests for specific domains.
// This allows the proxy to authenticate requests without exposing credentials
// to the sandbox environment.
type CredentialInjector interface {
	// Match returns true if this injector handles the request.
	Match(req *http.Request) bool
	// Inject adds credentials to the request (modifies in place). Returns
	// true when a header was actually written, false when it was a no-op
	// (token empty or header already set with overwrite=false).
	Inject(req *http.Request) bool
	// Name returns the injector's configured name (typically the TOML
	// section key, e.g., "github").
	Name() string
	// Header returns the HTTP header name the injector writes (e.g.,
	// "Authorization"). Used by audit events.
	Header() string
	// ResolvedValue returns the resolved credential value when the injector
	// is enabled, or "" when disabled. Used by redaction conflict detection.
	ResolvedValue() string
}

// applyGitHubFallback returns the GH_TOKEN environment value when the
// resolved token is empty AND the preset is "github" AND the user did not
// explicitly configure a [...source] sub-table. It is a small,
// scope-limited fallback documented in the github preset.
func applyGitHubFallback(presetName string, userSetSource bool, token string) string {
	if token != "" || presetName != "github" || userSetSource {
		return token
	}
	return os.Getenv("GH_TOKEN")
}

// BuildCredentialInjectors creates injectors from the raw TOML map at
// [proxy.credentials]. Each entry name becomes an injector; if the entry
// name (or an explicit `preset` field) matches a registered Preset, the
// preset's defaults are overlaid by user-provided fields.
//
// Returns an error for invalid configs: unknown preset, missing host or
// header after preset expansion when enabled, source-file read errors,
// invalid glob pattern.
//
// Source resolving to empty (env unset, file empty) silently disables the
// injector with a notice.Warn — that is the documented "credential not set
// on host" path.
//
// The returned slice is sorted by descending specificity (exact > longer
// glob > shorter glob), with name-ascending tiebreak. Disabled injectors
// (token empty after resolution) are excluded.
func BuildCredentialInjectors(credentials map[string]any) ([]CredentialInjector, error) {
	if len(credentials) == 0 {
		return nil, nil
	}

	// Iterate in deterministic order to make warnings reproducible. Final
	// slice is sorted at the end, so iteration order doesn't affect output.
	names := make([]string, 0, len(credentials))
	for name := range credentials {
		names = append(names, name)
	}
	sort.Strings(names)

	built := make([]*GenericInjector, 0, len(names))
	for _, name := range names {
		cfg, _ := credentials[name].(map[string]any)
		injector, err := buildOne(name, cfg)
		if err != nil {
			return nil, err
		}
		if injector == nil {
			continue
		}
		built = append(built, injector)
	}

	sort.SliceStable(built, func(i, j int) bool {
		specI := built[i].Specificity()
		specJ := built[j].Specificity()
		if specI != specJ {
			return specI > specJ
		}
		return built[i].name < built[j].name
	})

	out := make([]CredentialInjector, len(built))
	for i, g := range built {
		out[i] = g
	}
	return out, nil
}

// buildOne resolves preset + user overlay for a single injector entry and
// returns either the constructed injector, nil (disabled silently), or an
// error.
func buildOne(name string, raw map[string]any) (*GenericInjector, error) {
	var cfg credentialConfig
	if err := config.DecodeSection(raw, &cfg); err != nil {
		return nil, fmt.Errorf("credential injector %q: %w", name, err)
	}

	// Step 1: resolve preset name. Explicit `preset = "X"` wins; otherwise
	// the section name itself may match a registered preset.
	presetName := cfg.Preset
	if presetName == "" {
		// Inferred from section name — only if a preset with that name exists.
		if _, ok := lookupPreset(name); ok {
			presetName = name
		}
	}

	var preset Preset
	if presetName != "" {
		// presetName here is either explicit (user-provided) or inferred from
		// the section name only after a successful lookup. The inferred path
		// can never produce an unknown name, so a missing preset always means
		// the user typed an unknown explicit preset.
		p, ok := lookupPreset(presetName)
		if !ok {
			return nil, fmt.Errorf("credential injector %q: unknown preset %q", name, presetName)
		}
		preset = p
	}

	// Step 2: overlay user fields on preset defaults. User-provided
	// non-empty values win.
	host := preset.Host
	header := preset.Header
	valueFormat := preset.ValueFormat
	overwrite := cfg.Overwrite

	if cfg.Host != "" {
		host = cfg.Host
	}
	if cfg.Header != "" {
		header = cfg.Header
	}
	if cfg.ValueFormat != "" {
		valueFormat = cfg.ValueFormat
	}
	if valueFormat == "" {
		valueFormat = "{token}"
	}

	// Step 3: enabled flag. enabled=false is a silent skip (excluded slice).
	if !cfg.Enabled {
		return nil, nil
	}

	// Step 4: validate required fields (only when enabled).
	if host == "" {
		return nil, fmt.Errorf("credential injector %q: host is required when enabled = true", name)
	}
	if header == "" {
		return nil, fmt.Errorf("credential injector %q: header is required when enabled = true", name)
	}

	// Step 5: resolve [...source]. User source wins; otherwise preset
	// default; otherwise nil. An empty [...source] sub-table decodes to a
	// non-nil but zero-valued *Source — treat that as "no source set" so it
	// doesn't shadow a preset's DefaultSource.
	userSrc := cfg.Source
	userSetSource := !userSrc.IsZero()
	var src *source.Source
	if userSetSource {
		src = userSrc
	} else {
		src = preset.DefaultSource
	}

	// Step 6: resolve to a token string.
	var token string
	if src != nil {
		val, err := src.Resolve()
		if err != nil {
			return nil, fmt.Errorf("credential injector %q: %w", name, err)
		}
		token = val
	}

	// Step 7: github preset GH_TOKEN fallback.
	token = applyGitHubFallback(presetName, userSetSource, token)

	// Step 8: empty token -> silently disabled (with warning), no error.
	if token == "" {
		notice.Warn("credential injector %q: no token resolved, skipping", name)
		return nil, nil
	}

	// Step 9: compile matcher (see compileHostMatcher).
	g := &GenericInjector{
		name:        name,
		host:        host,
		header:      http.CanonicalHeaderKey(header),
		valueFormat: valueFormat,
		overwrite:   overwrite,
		token:       token,
		enabled:     true,
	}
	matcher, isExact, err := compileHostMatcher(host)
	if err != nil {
		return nil, fmt.Errorf("credential injector %q: %w", name, err)
	}
	g.matcher = matcher
	g.isExact = isExact

	return g, nil
}

// compileHostMatcher compiles a credential injector's host pattern. The pattern
// is canonicalized like the request host Match feeds it (NormalizeHost), so a
// host written with uppercase or a trailing dot still matches. Glob (pattern
// contains *, ?, or [) goes through doublestar; otherwise exact ==.
func compileHostMatcher(host string) (matcher func(string) bool, isExact bool, err error) {
	pattern := canonicalizeHost(host)
	if strings.ContainsAny(pattern, "*?[") {
		if !doublestar.ValidatePattern(pattern) {
			return nil, false, fmt.Errorf("invalid glob pattern %q", host)
		}
		return func(s string) bool {
			matched, _ := doublestar.Match(pattern, s)
			return matched
		}, false, nil
	}
	return func(s string) bool { return s == pattern }, true, nil
}
