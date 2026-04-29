package proxy

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bmatcuk/doublestar/v4"
)

func TestPresetRegistry_RegisterAndLookup(t *testing.T) {
	RegisterPreset("test-preset", Preset{
		Host:        "example.com",
		Header:      "X-Test",
		ValueFormat: "tok {token}",
	})
	p, ok := lookupPreset("test-preset")
	if !ok {
		t.Fatalf("expected preset to be registered")
	}
	if p.Host != "example.com" || p.Header != "X-Test" || p.ValueFormat != "tok {token}" {
		t.Errorf("preset fields not stored correctly: %+v", p)
	}
	if _, ok := lookupPreset("nonexistent-preset"); ok {
		t.Errorf("expected lookup of unknown preset to return false")
	}
}

// newGenericInjectorForTest builds a GenericInjector with a host matcher
// compiled the same way BuildCredentialInjectors will (exact == when no
// glob metachars; doublestar.Match otherwise). Test-internal helper.
func newGenericInjectorForTest(t *testing.T, name, host, header, valueFormat, token string, overwrite, enabled bool) *GenericInjector {
	t.Helper()
	g := &GenericInjector{
		name:        name,
		host:        host,
		header:      http.CanonicalHeaderKey(header),
		valueFormat: valueFormat,
		token:       token,
		overwrite:   overwrite,
		enabled:     enabled,
	}
	if strings.ContainsAny(host, "*?[") {
		if !doublestar.ValidatePattern(host) {
			t.Fatalf("invalid glob pattern in test setup: %q", host)
		}
		pattern := host
		g.matcher = func(s string) bool {
			matched, _ := doublestar.Match(pattern, s)
			return matched
		}
		g.isExact = false
	} else {
		exact := host
		g.matcher = func(s string) bool { return s == exact }
		g.isExact = true
	}
	return g
}

func TestGenericInjector_Match(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		reqHost string
		want    bool
	}{
		{"exact match", "api.github.com", "api.github.com", true},
		{"exact mismatch", "api.github.com", "raw.github.com", false},
		{"glob non-match: raw.githubusercontent.com is not a *.github.com subdomain",
			"*.github.com", "raw.githubusercontent.com", false},
		{"glob match subdomain", "*.github.com", "api.github.com", true},
		{"port stripped from req host", "api.github.com", "api.github.com:443", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newGenericInjectorForTest(t, "test", tt.host, "X-Test", "{token}", "tok", false, true)
			req, err := http.NewRequest("GET", "https://"+tt.reqHost+"/", nil)
			if err != nil {
				t.Fatalf("NewRequest error: %v", err)
			}
			if got := g.Match(req); got != tt.want {
				t.Errorf("Match(%q vs %q) = %v, want %v", tt.host, tt.reqHost, got, tt.want)
			}
		})
	}
}

func TestGenericInjector_Inject(t *testing.T) {
	t.Run("empty token is no-op", func(t *testing.T) {
		g := newGenericInjectorForTest(t, "test", "api.example.com", "Authorization", "Bearer {token}", "", false, true)
		req, _ := http.NewRequest("GET", "https://api.example.com/", nil)
		g.Inject(req)
		if got := req.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty", got)
		}
	})

	t.Run("overwrite=false preserves existing header", func(t *testing.T) {
		g := newGenericInjectorForTest(t, "test", "api.example.com", "Authorization", "Bearer {token}", "real-token", false, true)
		req, _ := http.NewRequest("GET", "https://api.example.com/", nil)
		req.Header.Set("Authorization", "Bearer placeholder")
		g.Inject(req)
		if got := req.Header.Get("Authorization"); got != "Bearer placeholder" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer placeholder")
		}
	})

	t.Run("overwrite=true replaces existing header", func(t *testing.T) {
		g := newGenericInjectorForTest(t, "test", "api.example.com", "Authorization", "Bearer {token}", "real-token", true, true)
		req, _ := http.NewRequest("GET", "https://api.example.com/", nil)
		req.Header.Set("Authorization", "Bearer placeholder")
		g.Inject(req)
		if got := req.Header.Get("Authorization"); got != "Bearer real-token" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer real-token")
		}
	})

	t.Run("value_format with {token} is rendered", func(t *testing.T) {
		g := newGenericInjectorForTest(t, "test", "api.example.com", "Authorization", "Bearer {token}", "abc", false, true)
		req, _ := http.NewRequest("GET", "https://api.example.com/", nil)
		g.Inject(req)
		if got := req.Header.Get("Authorization"); got != "Bearer abc" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer abc")
		}
	})

	t.Run("value_format raw {token} (no prefix)", func(t *testing.T) {
		g := newGenericInjectorForTest(t, "test", "api.example.com", "X-API-Key", "{token}", "abc", false, true)
		req, _ := http.NewRequest("GET", "https://api.example.com/", nil)
		g.Inject(req)
		if got := req.Header.Get("X-Api-Key"); got != "abc" {
			t.Errorf("X-Api-Key = %q, want %q", got, "abc")
		}
	})

	t.Run("header is canonicalized at construction", func(t *testing.T) {
		g := newGenericInjectorForTest(t, "test", "api.example.com", "authorization", "Bearer {token}", "abc", false, true)
		if g.header != "Authorization" {
			t.Errorf("header field = %q, want %q (canonicalized)", g.header, "Authorization")
		}
		req, _ := http.NewRequest("GET", "https://api.example.com/", nil)
		g.Inject(req)
		if got := req.Header.Get("Authorization"); got != "Bearer abc" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer abc")
		}
	})

	t.Run("value_format without {token} is literal", func(t *testing.T) {
		g := newGenericInjectorForTest(t, "test", "api.example.com", "X-Static", "literal-value", "ignored", false, true)
		req, _ := http.NewRequest("GET", "https://api.example.com/", nil)
		g.Inject(req)
		if got := req.Header.Get("X-Static"); got != "literal-value" {
			t.Errorf("X-Static = %q, want %q", got, "literal-value")
		}
	})
}

func TestGenericInjector_Specificity(t *testing.T) {
	cases := []struct {
		name string
		host string
		want int
	}{
		{"exact host", "api.github.com", 1000},
		{"glob *.github.com", "*.github.com", len("*.github.com") - 1},
		{"glob *", "*", 0},
		{"glob *.com", "*.com", len("*.com") - 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newGenericInjectorForTest(t, "test", c.host, "X-Test", "{token}", "tok", false, true)
			if got := g.Specificity(); got != c.want {
				t.Errorf("Specificity(%q) = %d, want %d", c.host, got, c.want)
			}
		})
	}
	// Sanity: exact > any glob; longer-literal-glob > shorter-literal-glob.
	exact := newGenericInjectorForTest(t, "exact", "api.github.com", "X", "{token}", "x", false, true)
	wide := newGenericInjectorForTest(t, "wide", "*.github.com", "X", "{token}", "x", false, true)
	star := newGenericInjectorForTest(t, "star", "*", "X", "{token}", "x", false, true)
	if exact.Specificity() <= wide.Specificity() {
		t.Errorf("expected exact > wide, got %d vs %d", exact.Specificity(), wide.Specificity())
	}
	if wide.Specificity() <= star.Specificity() {
		t.Errorf("expected *.github.com > *, got %d vs %d", wide.Specificity(), star.Specificity())
	}
}

func TestGenericInjector_ResolvedValue(t *testing.T) {
	t.Run("enabled returns token", func(t *testing.T) {
		g := newGenericInjectorForTest(t, "test", "api.example.com", "X", "{token}", "abc", false, true)
		if got := g.ResolvedValue(); got != "abc" {
			t.Errorf("ResolvedValue() = %q, want %q", got, "abc")
		}
	})
	t.Run("disabled returns empty", func(t *testing.T) {
		g := newGenericInjectorForTest(t, "test", "api.example.com", "X", "{token}", "abc", false, false)
		if got := g.ResolvedValue(); got != "" {
			t.Errorf("ResolvedValue() = %q, want empty", got)
		}
	})
}

func TestGenericInjector_Name(t *testing.T) {
	g := newGenericInjectorForTest(t, "my-injector", "api.example.com", "X", "{token}", "abc", false, true)
	if got := g.Name(); got != "my-injector" {
		t.Errorf("Name() = %q, want %q", got, "my-injector")
	}
}

func TestBuildCredentialInjectors_NilAndEmpty(t *testing.T) {
	t.Run("nil map returns nil slice and nil error", func(t *testing.T) {
		injs, err := BuildCredentialInjectors(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if injs != nil {
			t.Errorf("expected nil slice, got %v", injs)
		}
	})

	t.Run("empty map returns nil slice and nil error", func(t *testing.T) {
		injs, err := BuildCredentialInjectors(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if injs != nil {
			t.Errorf("expected nil slice, got %v", injs)
		}
	})
}

func TestBuildCredentialInjectors_CustomAndCanonicalization(t *testing.T) {
	t.Run("custom injector with static source builds and canonicalizes header", func(t *testing.T) {
		injs, err := BuildCredentialInjectors(map[string]any{
			"my-api": map[string]any{
				"enabled":      true,
				"host":         "api.example.com",
				"header":       "x-api-key",
				"value_format": "{token}",
				"source":       map[string]any{"value": "abc"},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(injs) != 1 {
			t.Fatalf("len(injs) = %d, want 1", len(injs))
		}
		if injs[0].Name() != "my-api" {
			t.Errorf("Name() = %q, want %q", injs[0].Name(), "my-api")
		}
		req, _ := http.NewRequest("GET", "https://api.example.com/", nil)
		injs[0].Inject(req)
		// Canonicalized: x-api-key -> X-Api-Key.
		if got := req.Header.Get("X-Api-Key"); got != "abc" {
			t.Errorf("X-Api-Key = %q, want %q", got, "abc")
		}
	})

	t.Run("authorization header is canonicalized at load time", func(t *testing.T) {
		injs, err := BuildCredentialInjectors(map[string]any{
			"basic-bearer": map[string]any{
				"enabled":      true,
				"host":         "api.example.com",
				"header":       "authorization",
				"value_format": "Bearer {token}",
				"source":       map[string]any{"value": "abc"},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(injs) != 1 {
			t.Fatalf("len(injs) = %d, want 1", len(injs))
		}
		req, _ := http.NewRequest("GET", "https://api.example.com/", nil)
		injs[0].Inject(req)
		if got := req.Header.Get("Authorization"); got != "Bearer abc" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer abc")
		}
	})

	t.Run("missing value_format defaults to {token}", func(t *testing.T) {
		injs, err := BuildCredentialInjectors(map[string]any{
			"raw": map[string]any{
				"enabled": true,
				"host":    "api.example.com",
				"header":  "X-Token",
				"source":  map[string]any{"value": "raw-value"},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(injs) != 1 {
			t.Fatalf("len(injs) = %d, want 1", len(injs))
		}
		req, _ := http.NewRequest("GET", "https://api.example.com/", nil)
		injs[0].Inject(req)
		if got := req.Header.Get("X-Token"); got != "raw-value" {
			t.Errorf("X-Token = %q, want %q", got, "raw-value")
		}
	})
}

func TestBuildCredentialInjectors_ValidationErrors(t *testing.T) {
	cases := []struct {
		name    string
		cfg     map[string]any
		wantSub []string // error message must contain all of these substrings
	}{
		{
			name: "missing host when enabled",
			cfg: map[string]any{
				"x": map[string]any{
					"enabled": true,
					"header":  "X-Test",
					"source":  map[string]any{"value": "tok"},
				},
			},
			wantSub: []string{"host", "x"},
		},
		{
			name: "missing header when enabled",
			cfg: map[string]any{
				"y": map[string]any{
					"enabled": true,
					"host":    "api.example.com",
					"source":  map[string]any{"value": "tok"},
				},
			},
			wantSub: []string{"header", "y"},
		},
		{
			name: "unknown explicit preset",
			cfg: map[string]any{
				"weird": map[string]any{
					"enabled": true,
					"preset":  "doesnotexist",
				},
			},
			wantSub: []string{"preset", "doesnotexist"},
		},
		{
			name: "invalid glob pattern",
			cfg: map[string]any{
				"bad-glob": map[string]any{
					"enabled":      true,
					"host":         "*[bad",
					"header":       "X-Test",
					"value_format": "{token}",
					"source":       map[string]any{"value": "tok"},
				},
			},
			wantSub: []string{"glob", "bad-glob"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildCredentialInjectors(tc.cfg)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			for _, s := range tc.wantSub {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q missing substring %q", err.Error(), s)
				}
			}
		})
	}
}

func TestBuildCredentialInjectors_PresetSelfReference(t *testing.T) {
	// preset = "<section>" with no preset registered under that name is a
	// no-op when inferred; an explicit unknown preset is an error. Here
	// the section name "self-ref-test" has no preset and the user did
	// not set `preset`, so the inferred path is taken (no preset).
	injs, err := BuildCredentialInjectors(map[string]any{
		"self-ref-test": map[string]any{
			"enabled":      true,
			"host":         "api.example.com",
			"header":       "X-Test",
			"value_format": "{token}",
			"source":       map[string]any{"value": "tok"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(injs) != 1 {
		t.Fatalf("len(injs) = %d, want 1", len(injs))
	}
}

func TestBuildCredentialInjectors_EmptySourceDisables(t *testing.T) {
	t.Setenv("DEVSANDBOX_TEST_UNSET", "")

	injs, err := BuildCredentialInjectors(map[string]any{
		"unset-env": map[string]any{
			"enabled":      true,
			"host":         "api.example.com",
			"header":       "X-Test",
			"value_format": "{token}",
			"source":       map[string]any{"env": "DEVSANDBOX_TEST_UNSET"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(injs) != 0 {
		t.Errorf("expected 0 injectors (silently disabled), got %d", len(injs))
	}
}

func TestBuildCredentialInjectors_SpecificityOrder(t *testing.T) {
	t.Run("exact wins over glob", func(t *testing.T) {
		injs, err := BuildCredentialInjectors(map[string]any{
			"wide": map[string]any{
				"enabled":      true,
				"host":         "*.github.com",
				"header":       "X-Wide",
				"value_format": "{token}",
				"source":       map[string]any{"value": "w"},
			},
			"specific": map[string]any{
				"enabled":      true,
				"host":         "api.github.com",
				"header":       "X-Specific",
				"value_format": "{token}",
				"source":       map[string]any{"value": "s"},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(injs) != 2 {
			t.Fatalf("len(injs) = %d, want 2", len(injs))
		}
		if injs[0].Name() != "specific" {
			t.Errorf("expected specific (exact) first, got %q", injs[0].Name())
		}
		if injs[1].Name() != "wide" {
			t.Errorf("expected wide (glob) second, got %q", injs[1].Name())
		}
	})

	t.Run("equal-specificity tiebreak by name ascending", func(t *testing.T) {
		// Two distinct exact hosts of equal "specificity" (both 1000).
		// Tie should be broken by name ascending: "alpha" before "beta".
		injs, err := BuildCredentialInjectors(map[string]any{
			"beta": map[string]any{
				"enabled":      true,
				"host":         "two.example.com",
				"header":       "X-Beta",
				"value_format": "{token}",
				"source":       map[string]any{"value": "b"},
			},
			"alpha": map[string]any{
				"enabled":      true,
				"host":         "one.example.com",
				"header":       "X-Alpha",
				"value_format": "{token}",
				"source":       map[string]any{"value": "a"},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(injs) != 2 {
			t.Fatalf("len(injs) = %d, want 2", len(injs))
		}
		if injs[0].Name() != "alpha" || injs[1].Name() != "beta" {
			t.Errorf("expected alpha,beta got %q,%q", injs[0].Name(), injs[1].Name())
		}
	})
}

func TestBuildCredentialInjectors_DisabledExcluded(t *testing.T) {
	injs, err := BuildCredentialInjectors(map[string]any{
		"off": map[string]any{
			"enabled": false,
			"host":    "api.example.com",
			"header":  "X-Off",
			"source":  map[string]any{"value": "tok"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(injs) != 0 {
		t.Errorf("expected 0 injectors (enabled=false), got %d", len(injs))
	}
}

func TestGitHubPreset_Compat(t *testing.T) {
	cases := []struct {
		name        string
		githubToken string
		ghToken     string
		wantEnabled bool
		wantHeader  string
	}{
		{"GITHUB_TOKEN set", "primary", "", true, "Bearer primary"},
		{"GH_TOKEN only", "", "fallback", true, "Bearer fallback"},
		{"both set, GITHUB_TOKEN wins", "primary", "fallback", true, "Bearer primary"},
		{"neither set, silently disabled", "", "", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("GITHUB_TOKEN", c.githubToken)
			t.Setenv("GH_TOKEN", c.ghToken)
			cfg := map[string]any{"github": map[string]any{"enabled": true}}
			injs, err := BuildCredentialInjectors(cfg)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !c.wantEnabled {
				if len(injs) != 0 {
					t.Fatalf("expected disabled, got %d injectors", len(injs))
				}
				return
			}
			if len(injs) != 1 {
				t.Fatalf("expected 1 injector, got %d", len(injs))
			}
			req := httptest.NewRequest("GET", "https://api.github.com/user", nil)
			if !injs[0].Match(req) {
				t.Fatalf("github preset must match api.github.com")
			}
			injs[0].Inject(req)
			if got := req.Header.Get("Authorization"); got != c.wantHeader {
				t.Errorf("got %q, want %q", got, c.wantHeader)
			}
			// Verify it does NOT match raw.githubusercontent.com (preset is exact host).
			req2 := httptest.NewRequest("GET", "https://raw.githubusercontent.com/x", nil)
			if injs[0].Match(req2) {
				t.Errorf("github preset must not match raw.githubusercontent.com")
			}
		})
	}
}

func TestGitHubPreset_OverwriteWithCustomSource(t *testing.T) {
	t.Setenv("GH_RO_TOKEN", "real-token")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	cfg := map[string]any{
		"github": map[string]any{
			"enabled":   true,
			"overwrite": true,
			"source":    map[string]any{"env": "GH_RO_TOKEN"},
		},
	}
	injs, err := BuildCredentialInjectors(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(injs) != 1 {
		t.Fatalf("expected 1 injector, got %d", len(injs))
	}
	req := httptest.NewRequest("GET", "https://api.github.com/user", nil)
	req.Header.Set("Authorization", "Bearer placeholder")
	injs[0].Inject(req)
	if got := req.Header.Get("Authorization"); got != "Bearer real-token" {
		t.Errorf("expected Bearer real-token (overwrite), got %q", got)
	}
}

func TestGitHubPreset_UserHostOverride(t *testing.T) {
	// User overlay wins over preset defaults — if user sets host="example.com",
	// the injector matches example.com, not api.github.com.
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("GH_TOKEN", "")
	cfg := map[string]any{
		"github": map[string]any{
			"enabled": true,
			"host":    "example.com",
		},
	}
	injs, err := BuildCredentialInjectors(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(injs) != 1 {
		t.Fatalf("expected 1 injector, got %d", len(injs))
	}
	req := httptest.NewRequest("GET", "https://example.com/", nil)
	if !injs[0].Match(req) {
		t.Errorf("user-overridden host must match")
	}
	req2 := httptest.NewRequest("GET", "https://api.github.com/", nil)
	if injs[0].Match(req2) {
		t.Errorf("preset default host must not match when user overrode")
	}
}

func TestBuildCredentialInjectors_SourceFileReadError(t *testing.T) {
	cfg := map[string]any{
		"x": map[string]any{
			"enabled": true,
			"host":    "api.example.com",
			"header":  "X-Test",
			"source":  map[string]any{"file": "/no/such/devsandbox-test-file"},
		},
	}
	injs, err := BuildCredentialInjectors(cfg)
	if err == nil {
		t.Fatalf("expected error from unreadable source file, got injectors=%v", injs)
	}
	if !strings.Contains(err.Error(), "x") {
		t.Errorf("error should mention injector name; got: %v", err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error should wrap fs.ErrNotExist; got: %v", err)
	}
}

func TestGitHubPreset_ExplicitSelfReference(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("GH_TOKEN", "")
	cfg := map[string]any{
		"github": map[string]any{
			"enabled": true,
			"preset":  "github", // explicit, matches section name — should be a no-op
		},
	}
	injs, err := BuildCredentialInjectors(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(injs) != 1 {
		t.Fatalf("expected 1 injector, got %d", len(injs))
	}
	req := httptest.NewRequest("GET", "https://api.github.com/user", nil)
	injs[0].Inject(req)
	if got := req.Header.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer tok")
	}
}
