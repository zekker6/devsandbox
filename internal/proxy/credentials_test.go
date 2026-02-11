package proxy

import (
	"net/http"
	"os"
	"path/filepath"
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

func TestGitHubCredentialInjector_Configure_WithSource(t *testing.T) {
	t.Run("uses custom env var from source", func(t *testing.T) {
		t.Setenv("CUSTOM_GH_TOKEN", "custom-token")
		t.Setenv("GITHUB_TOKEN", "default-token")

		injector := &GitHubCredentialInjector{}
		injector.Configure(map[string]any{
			"enabled": true,
			"source":  map[string]any{"env": "CUSTOM_GH_TOKEN"},
		})

		if !injector.Enabled() {
			t.Error("expected enabled")
		}
		if injector.token != "custom-token" {
			t.Errorf("token = %q, want %q", injector.token, "custom-token")
		}
	})

	t.Run("source overrides defaults even when default vars set", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "default-token")
		t.Setenv("GH_TOKEN", "gh-token")
		t.Setenv("MY_TOKEN", "my-token")

		injector := &GitHubCredentialInjector{}
		injector.Configure(map[string]any{
			"enabled": true,
			"source":  map[string]any{"env": "MY_TOKEN"},
		})

		if injector.token != "my-token" {
			t.Errorf("token = %q, want %q (source should override defaults)", injector.token, "my-token")
		}
	})

	t.Run("disabled when source env var is unset", func(t *testing.T) {
		t.Setenv("MISSING_VAR", "")
		t.Setenv("GITHUB_TOKEN", "default-token")

		injector := &GitHubCredentialInjector{}
		injector.Configure(map[string]any{
			"enabled": true,
			"source":  map[string]any{"env": "MISSING_VAR"},
		})

		if injector.Enabled() {
			t.Error("expected disabled when source env var is empty")
		}
	})

	t.Run("falls back to defaults when source not configured", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "default-token")

		injector := &GitHubCredentialInjector{}
		injector.Configure(map[string]any{"enabled": true})

		if !injector.Enabled() {
			t.Error("expected enabled")
		}
		if injector.token != "default-token" {
			t.Errorf("token = %q, want %q", injector.token, "default-token")
		}
	})

	t.Run("uses static value from source", func(t *testing.T) {
		injector := &GitHubCredentialInjector{}
		injector.Configure(map[string]any{
			"enabled": true,
			"source":  map[string]any{"value": "static-github-token"},
		})

		if !injector.Enabled() {
			t.Error("expected enabled")
		}
		if injector.token != "static-github-token" {
			t.Errorf("token = %q, want %q", injector.token, "static-github-token")
		}
	})

	t.Run("uses file from source", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "gh-token")
		if err := os.WriteFile(path, []byte("file-github-token\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		injector := &GitHubCredentialInjector{}
		injector.Configure(map[string]any{
			"enabled": true,
			"source":  map[string]any{"file": path},
		})

		if !injector.Enabled() {
			t.Error("expected enabled")
		}
		if injector.token != "file-github-token" {
			t.Errorf("token = %q, want %q", injector.token, "file-github-token")
		}
	})
}

func TestParseCredentialSource(t *testing.T) {
	t.Run("nil config returns nil", func(t *testing.T) {
		src := ParseCredentialSource(nil)
		if src != nil {
			t.Errorf("expected nil, got %+v", src)
		}
	})

	t.Run("no source key returns nil", func(t *testing.T) {
		src := ParseCredentialSource(map[string]any{"enabled": true})
		if src != nil {
			t.Errorf("expected nil, got %+v", src)
		}
	})

	t.Run("source with env", func(t *testing.T) {
		src := ParseCredentialSource(map[string]any{
			"source": map[string]any{"env": "MY_TOKEN"},
		})
		if src == nil {
			t.Fatal("expected non-nil source")
		}
		if src.Env != "MY_TOKEN" {
			t.Errorf("Env = %q, want %q", src.Env, "MY_TOKEN")
		}
	})

	t.Run("source with value", func(t *testing.T) {
		src := ParseCredentialSource(map[string]any{
			"source": map[string]any{"value": "static-secret"},
		})
		if src == nil {
			t.Fatal("expected non-nil source")
		}
		if src.Value != "static-secret" {
			t.Errorf("Value = %q, want %q", src.Value, "static-secret")
		}
	})

	t.Run("source with file", func(t *testing.T) {
		src := ParseCredentialSource(map[string]any{
			"source": map[string]any{"file": "~/.config/devsandbox/github-token"},
		})
		if src == nil {
			t.Fatal("expected non-nil source")
		}
		if src.File != "~/.config/devsandbox/github-token" {
			t.Errorf("File = %q, want %q", src.File, "~/.config/devsandbox/github-token")
		}
	})

	t.Run("empty source returns non-nil with empty fields", func(t *testing.T) {
		src := ParseCredentialSource(map[string]any{
			"source": map[string]any{},
		})
		if src == nil {
			t.Fatal("expected non-nil source (explicit empty source)")
		}
		if src.Env != "" {
			t.Errorf("Env = %q, want empty", src.Env)
		}
	})

	t.Run("source with wrong type is ignored", func(t *testing.T) {
		src := ParseCredentialSource(map[string]any{
			"source": "not-a-map",
		})
		if src != nil {
			t.Errorf("expected nil for wrong type, got %+v", src)
		}
	})
}

func TestCredentialSource_Resolve(t *testing.T) {
	t.Run("resolves env var", func(t *testing.T) {
		t.Setenv("TEST_CRED_TOKEN", "resolved-value")

		src := &CredentialSource{Env: "TEST_CRED_TOKEN"}
		if got := src.Resolve(); got != "resolved-value" {
			t.Errorf("Resolve() = %q, want %q", got, "resolved-value")
		}
	})

	t.Run("returns empty when env var missing", func(t *testing.T) {
		t.Setenv("TEST_CRED_MISSING", "")

		src := &CredentialSource{Env: "TEST_CRED_MISSING"}
		if got := src.Resolve(); got != "" {
			t.Errorf("Resolve() = %q, want empty", got)
		}
	})

	t.Run("returns empty when all fields empty", func(t *testing.T) {
		src := &CredentialSource{}
		if got := src.Resolve(); got != "" {
			t.Errorf("Resolve() = %q, want empty", got)
		}
	})

	t.Run("resolves static value", func(t *testing.T) {
		src := &CredentialSource{Value: "static-secret"}
		if got := src.Resolve(); got != "static-secret" {
			t.Errorf("Resolve() = %q, want %q", got, "static-secret")
		}
	})

	t.Run("resolves file contents", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "token")
		if err := os.WriteFile(path, []byte("file-secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		src := &CredentialSource{File: path}
		if got := src.Resolve(); got != "file-secret" {
			t.Errorf("Resolve() = %q, want %q (should trim whitespace)", got, "file-secret")
		}
	})

	t.Run("returns empty when file does not exist", func(t *testing.T) {
		src := &CredentialSource{File: "/nonexistent/path/to/token"}
		if got := src.Resolve(); got != "" {
			t.Errorf("Resolve() = %q, want empty", got)
		}
	})

	t.Run("value takes priority over env", func(t *testing.T) {
		t.Setenv("TEST_CRED_PRIORITY", "from-env")

		src := &CredentialSource{Value: "from-value", Env: "TEST_CRED_PRIORITY"}
		if got := src.Resolve(); got != "from-value" {
			t.Errorf("Resolve() = %q, want %q (value should take priority)", got, "from-value")
		}
	})

	t.Run("env takes priority over file", func(t *testing.T) {
		t.Setenv("TEST_CRED_ENV_PRIO", "from-env")
		dir := t.TempDir()
		path := filepath.Join(dir, "token")
		if err := os.WriteFile(path, []byte("from-file"), 0o600); err != nil {
			t.Fatal(err)
		}

		src := &CredentialSource{Env: "TEST_CRED_ENV_PRIO", File: path}
		if got := src.Resolve(); got != "from-env" {
			t.Errorf("Resolve() = %q, want %q (env should take priority over file)", got, "from-env")
		}
	})

	t.Run("file with tilde expansion", func(t *testing.T) {
		// Write to a temp file under home to test ~ expansion
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("cannot determine home directory")
		}
		dir := t.TempDir()
		// TempDir is not under ~, so test absolute path with expandHome logic
		path := filepath.Join(dir, "token")
		if err := os.WriteFile(path, []byte("  token-with-spaces  \n"), 0o600); err != nil {
			t.Fatal(err)
		}

		// Test absolute path works (no expansion needed)
		src := &CredentialSource{File: path}
		if got := src.Resolve(); got != "token-with-spaces" {
			t.Errorf("Resolve() = %q, want %q", got, "token-with-spaces")
		}

		// Test ~ expansion with a known path under home
		testFile := filepath.Join(home, ".devsandbox-test-cred-resolve")
		if err := os.WriteFile(testFile, []byte("home-token\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(testFile) })

		src = &CredentialSource{File: "~/.devsandbox-test-cred-resolve"}
		if got := src.Resolve(); got != "home-token" {
			t.Errorf("Resolve() = %q, want %q (tilde should expand)", got, "home-token")
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
