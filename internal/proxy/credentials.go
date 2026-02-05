package proxy

import (
	"net"
	"net/http"
	"os"
)

// CredentialInjector adds authentication to requests for specific domains.
// This allows the proxy to authenticate requests without exposing credentials
// to the sandbox environment.
type CredentialInjector interface {
	// Match returns true if this injector handles the request
	Match(req *http.Request) bool
	// Inject adds credentials to the request (modifies in place)
	Inject(req *http.Request)
}

// GitHubCredentialInjector injects GitHub API tokens for api.github.com requests.
// It reads the token from GITHUB_TOKEN or GH_TOKEN environment variables.
type GitHubCredentialInjector struct {
	token string
}

// NewGitHubCredentialInjector creates a new GitHub credential injector.
// Returns nil if no GitHub token is available in the environment.
func NewGitHubCredentialInjector() *GitHubCredentialInjector {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	if token == "" {
		return nil
	}
	return &GitHubCredentialInjector{token: token}
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

// DefaultCredentialInjectors returns the default set of credential injectors.
// Currently only includes GitHub, but designed for future extensibility.
func DefaultCredentialInjectors() []CredentialInjector {
	var injectors []CredentialInjector

	if gh := NewGitHubCredentialInjector(); gh != nil {
		injectors = append(injectors, gh)
	}

	return injectors
}
