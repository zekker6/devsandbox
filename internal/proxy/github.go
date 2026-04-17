package proxy

import (
	"net/http"
	"os"
)

func init() {
	RegisterCredentialInjector("github", func() ConfigurableInjector {
		return &GitHubCredentialInjector{}
	})
}

// GitHubCredentialInjector injects GitHub API tokens for api.github.com requests.
// It reads the token from GITHUB_TOKEN or GH_TOKEN environment variables.
type GitHubCredentialInjector struct {
	token     string
	enabled   bool
	overwrite bool
}

// Name returns "github".
func (g *GitHubCredentialInjector) Name() string {
	return "github"
}

// Configure reads the injector config from the raw TOML map.
// Expects: enabled = true/false (required, defaults to false).
// Optional: overwrite = true/false (defaults to false). When true, the
// injector replaces any existing Authorization header on matching requests —
// useful for pairing a real host-side token with a placeholder env var in
// the sandbox (e.g., to satisfy CLIs like `gh` that refuse to start without
// a token set).
// Optional: [proxy.credentials.github.source] with env = "VAR_NAME".
// When source is configured, it takes precedence over default env vars.
func (g *GitHubCredentialInjector) Configure(cfg map[string]any) {
	g.enabled = false
	g.overwrite = false

	if cfg == nil {
		return
	}

	if enabled, ok := cfg["enabled"].(bool); ok && enabled {
		if src := ParseCredentialSource(cfg); src != nil {
			g.token = src.Resolve()
		} else {
			g.token = os.Getenv("GITHUB_TOKEN")
			if g.token == "" {
				g.token = os.Getenv("GH_TOKEN")
			}
		}
		g.enabled = g.token != ""
	}

	if overwrite, ok := cfg["overwrite"].(bool); ok {
		g.overwrite = overwrite
	}
}

// Enabled returns true if the injector is configured and has a valid token.
func (g *GitHubCredentialInjector) Enabled() bool {
	return g.enabled
}

// Match returns true for requests to api.github.com.
func (g *GitHubCredentialInjector) Match(req *http.Request) bool {
	return NormalizeHost(req.URL.Host) == "api.github.com"
}

// ResolvedValue returns the resolved token value.
func (g *GitHubCredentialInjector) ResolvedValue() string {
	if !g.enabled {
		return ""
	}
	return g.token
}

// Inject adds the Authorization header. By default, existing headers are
// preserved. When overwrite is enabled, any existing Authorization header is
// replaced with the injector's token (if non-empty).
func (g *GitHubCredentialInjector) Inject(req *http.Request) {
	if g.token == "" {
		return
	}
	if !g.overwrite && req.Header.Get("Authorization") != "" {
		return
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
}
