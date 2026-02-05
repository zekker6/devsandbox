package proxy

import (
	"net/http"
	"testing"
)

func TestGitHubCredentialInjector_Match(t *testing.T) {
	injector := &GitHubCredentialInjector{token: "test-token"}

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

func TestGitHubCredentialInjector_Inject(t *testing.T) {
	t.Run("injects token when not present", func(t *testing.T) {
		injector := &GitHubCredentialInjector{token: "my-secret-token"}
		req, _ := http.NewRequest("GET", "https://api.github.com/repos/foo/bar", nil)

		injector.Inject(req)

		auth := req.Header.Get("Authorization")
		if auth != "Bearer my-secret-token" {
			t.Errorf("Authorization = %q, want %q", auth, "Bearer my-secret-token")
		}
	})

	t.Run("does not override existing auth", func(t *testing.T) {
		injector := &GitHubCredentialInjector{token: "my-secret-token"}
		req, _ := http.NewRequest("GET", "https://api.github.com/repos/foo/bar", nil)
		req.Header.Set("Authorization", "Bearer existing-token")

		injector.Inject(req)

		auth := req.Header.Get("Authorization")
		if auth != "Bearer existing-token" {
			t.Errorf("Authorization = %q, want %q (should not override)", auth, "Bearer existing-token")
		}
	})

	t.Run("no-op when token is empty", func(t *testing.T) {
		injector := &GitHubCredentialInjector{token: ""}
		req, _ := http.NewRequest("GET", "https://api.github.com/repos/foo/bar", nil)

		injector.Inject(req)

		auth := req.Header.Get("Authorization")
		if auth != "" {
			t.Errorf("Authorization = %q, want empty", auth)
		}
	})
}

func TestNewGitHubCredentialInjector(t *testing.T) {
	t.Run("uses GITHUB_TOKEN", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "token-from-github-token")
		t.Setenv("GH_TOKEN", "token-from-gh-token")

		injector := NewGitHubCredentialInjector()

		if injector == nil {
			t.Fatal("expected injector, got nil")
		}
		if injector.token != "token-from-github-token" {
			t.Errorf("token = %q, want %q", injector.token, "token-from-github-token")
		}
	})

	t.Run("falls back to GH_TOKEN", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "token-from-gh-token")

		injector := NewGitHubCredentialInjector()

		if injector == nil {
			t.Fatal("expected injector, got nil")
		}
		if injector.token != "token-from-gh-token" {
			t.Errorf("token = %q, want %q", injector.token, "token-from-gh-token")
		}
	})

	t.Run("returns nil when no token", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "")

		injector := NewGitHubCredentialInjector()

		if injector != nil {
			t.Errorf("expected nil, got %+v", injector)
		}
	})
}

func TestDefaultCredentialInjectors(t *testing.T) {
	t.Run("includes GitHub injector when token set", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "test-token")

		injectors := DefaultCredentialInjectors()

		if len(injectors) != 1 {
			t.Errorf("len(injectors) = %d, want 1", len(injectors))
		}
	})

	t.Run("empty when no tokens", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "")

		injectors := DefaultCredentialInjectors()

		if len(injectors) != 0 {
			t.Errorf("len(injectors) = %d, want 0", len(injectors))
		}
	})
}
