package proxy

import (
	"net/http"
	"testing"
)

func TestGitHubCredentialInjector_Match(t *testing.T) {
	injector := &GitHubCredentialInjector{token: "test-token", enabled: true}

	tests := []struct {
		name     string
		url      string
		expected bool
	}{
		{"matches api.github.com", "https://api.github.com/repos/foo/bar", true},
		{"matches api.github.com with path", "https://api.github.com/rate_limit", true},
		{"no match github.com", "https://github.com/foo/bar", false},
		{"no match raw.githubusercontent.com", "https://raw.githubusercontent.com/foo/bar/main/file", false},
		{"no match other domain", "https://example.com/api", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", tt.url, nil)
			if got := injector.Match(req); got != tt.expected {
				t.Errorf("Match() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestGitHubCredentialInjector_Match_WithPort(t *testing.T) {
	injector := &GitHubCredentialInjector{token: "test-token", enabled: true}

	tests := []struct {
		name     string
		host     string
		expected bool
	}{
		{"api.github.com no port", "api.github.com", true},
		{"api.github.com with 443", "api.github.com:443", true},
		{"api.github.com with 8080", "api.github.com:8080", true},
		{"github.com no port", "github.com", false},
		{"github.com with 443", "github.com:443", false},
		{"other host", "example.com", false},
		{"other host with port", "example.com:443", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", "https://placeholder/path", nil)
			req.URL.Host = tt.host
			if got := injector.Match(req); got != tt.expected {
				t.Errorf("Match() = %v, want %v for host %q", got, tt.expected, tt.host)
			}
		})
	}
}

func TestGitHubCredentialInjector_Inject(t *testing.T) {
	t.Run("injects token when not present", func(t *testing.T) {
		injector := &GitHubCredentialInjector{token: "my-secret-token", enabled: true}
		req, _ := http.NewRequest("GET", "https://api.github.com/repos/foo/bar", nil)

		injector.Inject(req)

		auth := req.Header.Get("Authorization")
		if auth != "Bearer my-secret-token" {
			t.Errorf("Authorization = %q, want %q", auth, "Bearer my-secret-token")
		}
	})

	t.Run("does not override existing auth", func(t *testing.T) {
		injector := &GitHubCredentialInjector{token: "my-secret-token", enabled: true}
		req, _ := http.NewRequest("GET", "https://api.github.com/repos/foo/bar", nil)
		req.Header.Set("Authorization", "Bearer existing-token")

		injector.Inject(req)

		auth := req.Header.Get("Authorization")
		if auth != "Bearer existing-token" {
			t.Errorf("Authorization = %q, want %q (should not override)", auth, "Bearer existing-token")
		}
	})

	t.Run("no-op when token is empty", func(t *testing.T) {
		injector := &GitHubCredentialInjector{token: "", enabled: true}
		req, _ := http.NewRequest("GET", "https://api.github.com/repos/foo/bar", nil)

		injector.Inject(req)

		auth := req.Header.Get("Authorization")
		if auth != "" {
			t.Errorf("Authorization = %q, want empty", auth)
		}
	})
}

func TestGitHubCredentialInjector_Configure(t *testing.T) {
	t.Run("enabled with GITHUB_TOKEN", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "token-from-github-token")
		t.Setenv("GH_TOKEN", "token-from-gh-token")

		injector := &GitHubCredentialInjector{}
		injector.Configure(map[string]any{"enabled": true})

		if !injector.Enabled() {
			t.Error("expected enabled")
		}
		if injector.token != "token-from-github-token" {
			t.Errorf("token = %q, want %q", injector.token, "token-from-github-token")
		}
	})

	t.Run("falls back to GH_TOKEN", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "token-from-gh-token")

		injector := &GitHubCredentialInjector{}
		injector.Configure(map[string]any{"enabled": true})

		if !injector.Enabled() {
			t.Error("expected enabled")
		}
		if injector.token != "token-from-gh-token" {
			t.Errorf("token = %q, want %q", injector.token, "token-from-gh-token")
		}
	})

	t.Run("disabled when enabled=false", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "some-token")

		injector := &GitHubCredentialInjector{}
		injector.Configure(map[string]any{"enabled": false})

		if injector.Enabled() {
			t.Error("expected disabled")
		}
	})

	t.Run("disabled when no token available", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "")

		injector := &GitHubCredentialInjector{}
		injector.Configure(map[string]any{"enabled": true})

		if injector.Enabled() {
			t.Error("expected disabled when no token")
		}
	})

	t.Run("disabled with nil config", func(t *testing.T) {
		injector := &GitHubCredentialInjector{}
		injector.Configure(nil)

		if injector.Enabled() {
			t.Error("expected disabled with nil config")
		}
	})

	t.Run("disabled when enabled not set", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "some-token")

		injector := &GitHubCredentialInjector{}
		injector.Configure(map[string]any{})

		if injector.Enabled() {
			t.Error("expected disabled when enabled not set")
		}
	})
}

func TestBuildCredentialInjectors(t *testing.T) {
	t.Run("returns github injector when enabled with token", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "test-token")

		injectors := BuildCredentialInjectors(map[string]any{
			"github": map[string]any{"enabled": true},
		})

		if len(injectors) != 1 {
			t.Fatalf("len(injectors) = %d, want 1", len(injectors))
		}
	})

	t.Run("empty when no credentials configured", func(t *testing.T) {
		injectors := BuildCredentialInjectors(nil)

		if len(injectors) != 0 {
			t.Errorf("len(injectors) = %d, want 0", len(injectors))
		}
	})

	t.Run("empty when github disabled", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "test-token")

		injectors := BuildCredentialInjectors(map[string]any{
			"github": map[string]any{"enabled": false},
		})

		if len(injectors) != 0 {
			t.Errorf("len(injectors) = %d, want 0", len(injectors))
		}
	})

	t.Run("skips unknown injectors", func(t *testing.T) {
		injectors := BuildCredentialInjectors(map[string]any{
			"nonexistent": map[string]any{"enabled": true},
		})

		if len(injectors) != 0 {
			t.Errorf("len(injectors) = %d, want 0", len(injectors))
		}
	})

	t.Run("empty when no tokens available", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "")

		injectors := BuildCredentialInjectors(map[string]any{
			"github": map[string]any{"enabled": true},
		})

		if len(injectors) != 0 {
			t.Errorf("len(injectors) = %d, want 0 (no token available)", len(injectors))
		}
	})
}

func TestRegisteredCredentialInjectors(t *testing.T) {
	names := RegisteredCredentialInjectors()

	found := false
	for _, name := range names {
		if name == "github" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'github' in registered injectors, got %v", names)
	}
}
