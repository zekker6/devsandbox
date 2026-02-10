package proxy

import (
	"net"
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
	token   string
	enabled bool
}

// Name returns "github".
func (g *GitHubCredentialInjector) Name() string {
	return "github"
}

// Configure reads the injector config from the raw TOML map.
// Expects: enabled = true/false (required, defaults to false).
func (g *GitHubCredentialInjector) Configure(cfg map[string]any) {
	g.enabled = false

	if cfg == nil {
		return
	}

	if enabled, ok := cfg["enabled"].(bool); ok && enabled {
		g.token = os.Getenv("GITHUB_TOKEN")
		if g.token == "" {
			g.token = os.Getenv("GH_TOKEN")
		}
		g.enabled = g.token != ""
	}
}

// Enabled returns true if the injector is configured and has a valid token.
func (g *GitHubCredentialInjector) Enabled() bool {
	return g.enabled
}

// Match returns true for requests to api.github.com.
func (g *GitHubCredentialInjector) Match(req *http.Request) bool {
	// Host may include port (e.g., "api.github.com:443")
	host := req.URL.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host == "api.github.com"
}

// Inject adds the Authorization header if not already present.
func (g *GitHubCredentialInjector) Inject(req *http.Request) {
	// Don't override existing authorization
	if req.Header.Get("Authorization") != "" {
		return
	}
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
}
