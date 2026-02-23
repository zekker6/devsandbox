package proxy

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/bmatcuk/doublestar/v4"
)

func TestFilterEngine_GlobPattern(t *testing.T) {
	cfg := &FilterConfig{
		DefaultAction: FilterActionBlock, // whitelist behavior
		Rules: []FilterRule{
			{Pattern: "*.github.com", Action: FilterActionAllow, Scope: FilterScopeHost},
			{Pattern: "api.anthropic.com", Action: FilterActionAllow, Scope: FilterScopeHost},
		},
	}

	engine, err := NewFilterEngine(cfg)
	if err != nil {
		t.Fatalf("failed to create filter engine: %v", err)
	}

	tests := []struct {
		name     string
		host     string
		expected FilterAction
	}{
		{"exact match", "api.anthropic.com", FilterActionAllow},
		{"glob match", "api.github.com", FilterActionAllow},
		{"glob match subdomain", "raw.github.com", FilterActionAllow},
		{"not matched", "example.com", FilterActionBlock}, // whitelist default
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{
				Host: tt.host,
				URL:  &url.URL{Host: tt.host, Path: "/"},
			}
			decision := engine.Match(req)
			if decision.Action != tt.expected {
				t.Errorf("got action %s, want %s", decision.Action, tt.expected)
			}
		})
	}
}

func TestFilterEngine_RegexPattern(t *testing.T) {
	cfg := &FilterConfig{
		DefaultAction: FilterActionBlock, // whitelist behavior
		Rules: []FilterRule{
			{Pattern: `^api\.(dev|staging)\.example\.com$`, Action: FilterActionAllow, Scope: FilterScopeHost, Type: PatternTypeRegex},
		},
	}

	engine, err := NewFilterEngine(cfg)
	if err != nil {
		t.Fatalf("failed to create filter engine: %v", err)
	}

	tests := []struct {
		name     string
		host     string
		expected FilterAction
	}{
		{"dev match", "api.dev.example.com", FilterActionAllow},
		{"staging match", "api.staging.example.com", FilterActionAllow},
		{"prod no match", "api.prod.example.com", FilterActionBlock},
		{"base no match", "api.example.com", FilterActionBlock},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{
				Host: tt.host,
				URL:  &url.URL{Host: tt.host, Path: "/"},
			}
			decision := engine.Match(req)
			if decision.Action != tt.expected {
				t.Errorf("got action %s, want %s", decision.Action, tt.expected)
			}
		})
	}
}

func TestFilterEngine_BlacklistMode(t *testing.T) {
	cfg := &FilterConfig{
		DefaultAction: FilterActionAllow, // blacklist behavior
		Rules: []FilterRule{
			{Pattern: "*.tracking.io", Action: FilterActionBlock, Scope: FilterScopeHost},
			{Pattern: "ads.example.com", Action: FilterActionBlock, Scope: FilterScopeHost},
		},
	}

	engine, err := NewFilterEngine(cfg)
	if err != nil {
		t.Fatalf("failed to create filter engine: %v", err)
	}

	tests := []struct {
		name     string
		host     string
		expected FilterAction
	}{
		{"blocked glob", "metrics.tracking.io", FilterActionBlock},
		{"blocked exact", "ads.example.com", FilterActionBlock},
		{"allowed", "api.example.com", FilterActionAllow}, // blacklist default
		{"allowed other", "github.com", FilterActionAllow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{
				Host: tt.host,
				URL:  &url.URL{Host: tt.host, Path: "/"},
			}
			decision := engine.Match(req)
			if decision.Action != tt.expected {
				t.Errorf("got action %s, want %s", decision.Action, tt.expected)
			}
		})
	}
}

func TestFilterEngine_PathScope(t *testing.T) {
	cfg := &FilterConfig{
		DefaultAction: FilterActionAllow, // blacklist behavior
		Rules: []FilterRule{
			{Pattern: "/api/admin/*", Action: FilterActionBlock, Scope: FilterScopePath},
			{Pattern: "/debug/*", Action: FilterActionBlock, Scope: FilterScopePath},
		},
	}

	engine, err := NewFilterEngine(cfg)
	if err != nil {
		t.Fatalf("failed to create filter engine: %v", err)
	}

	tests := []struct {
		name     string
		path     string
		expected FilterAction
	}{
		{"blocked admin", "/api/admin/users", FilterActionBlock},
		{"blocked debug", "/debug/pprof", FilterActionBlock},
		{"allowed api", "/api/v1/users", FilterActionAllow},
		{"allowed root", "/", FilterActionAllow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{
				Host: "example.com",
				URL:  &url.URL{Host: "example.com", Path: tt.path},
			}
			decision := engine.Match(req)
			if decision.Action != tt.expected {
				t.Errorf("got action %s, want %s", decision.Action, tt.expected)
			}
		})
	}
}

func TestFilterEngine_DisabledMode(t *testing.T) {
	cfg := &FilterConfig{
		// DefaultAction empty = filtering disabled
		Rules: []FilterRule{
			{Pattern: "blocked.com", Action: FilterActionBlock, Scope: FilterScopeHost},
		},
	}

	engine, err := NewFilterEngine(cfg)
	if err != nil {
		t.Fatalf("failed to create filter engine: %v", err)
	}

	req := &http.Request{
		Host: "blocked.com",
		URL:  &url.URL{Host: "blocked.com", Path: "/"},
	}
	decision := engine.Match(req)

	if decision.Action != FilterActionAllow {
		t.Errorf("disabled mode should allow all, got %s", decision.Action)
	}
	if !decision.IsDefault {
		t.Error("disabled mode should use default action")
	}
}

func TestFilterEngine_DecisionCache(t *testing.T) {
	cfg := &FilterConfig{
		DefaultAction: FilterActionAsk,
	}

	engine, err := NewFilterEngine(cfg)
	if err != nil {
		t.Fatalf("failed to create filter engine: %v", err)
	}

	// Cache a decision
	engine.CacheDecision("cached.example.com", FilterActionAllow)

	req := &http.Request{
		Host: "cached.example.com",
		URL:  &url.URL{Host: "cached.example.com", Path: "/"},
	}
	decision := engine.Match(req)

	if decision.Action != FilterActionAllow {
		t.Errorf("cached decision should be allow, got %s", decision.Action)
	}

	// Clear cache
	engine.ClearCache()

	// Should now return ask (default for ask mode)
	decision = engine.Match(req)
	if decision.Action != FilterActionAsk {
		t.Errorf("after cache clear, should return ask, got %s", decision.Action)
	}
}

func TestFilterConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     FilterConfig
		wantErr bool
	}{
		{
			name: "valid whitelist",
			cfg: FilterConfig{
				DefaultAction: FilterActionBlock,
				Rules: []FilterRule{
					{Pattern: "*.example.com", Action: FilterActionAllow},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid default action",
			cfg: FilterConfig{
				DefaultAction: "invalid",
			},
			wantErr: true,
		},
		{
			name: "invalid rule action",
			cfg: FilterConfig{
				DefaultAction: FilterActionBlock,
				Rules: []FilterRule{
					{Pattern: "example.com", Action: "invalid"},
				},
			},
			wantErr: true,
		},
		{
			name: "missing pattern",
			cfg: FilterConfig{
				DefaultAction: FilterActionBlock,
				Rules: []FilterRule{
					{Pattern: "", Action: FilterActionAllow},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid regex",
			cfg: FilterConfig{
				DefaultAction: FilterActionBlock,
				Rules: []FilterRule{
					{Pattern: "[invalid", Action: FilterActionAllow, Type: PatternTypeRegex},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGlobMatching(t *testing.T) {
	tests := []struct {
		glob    string
		input   string
		matches bool
	}{
		{"*.example.com", "api.example.com", true},
		{"*.example.com", "example.com", false},
		{"api.*.com", "api.example.com", true},
		{"api.?.com", "api.x.com", true},
		{"api.?.com", "api.xx.com", false},
		{"test.com", "test.com", true},
		{"test.com", "other.com", false},
		// Additional tests for doublestar features
		{"**.github.com", "api.github.com", true},
		{"**.github.com", "raw.githubusercontent.github.com", true},
		{"*.tracking.io", "metrics.tracking.io", true},
	}

	for _, tt := range tests {
		t.Run(tt.glob+"_"+tt.input, func(t *testing.T) {
			matched, err := doublestar.Match(tt.glob, tt.input)
			if err != nil {
				t.Fatalf("doublestar.Match(%q, %q) error: %v", tt.glob, tt.input, err)
			}
			if matched != tt.matches {
				t.Errorf("doublestar.Match(%q, %q) = %v, want %v",
					tt.glob, tt.input, matched, tt.matches)
			}
		})
	}
}

func TestFilterRule_DetectPatternType(t *testing.T) {
	tests := []struct {
		pattern  string
		expected PatternType
	}{
		{"example.com", PatternTypeGlob},            // default is glob
		{"*.example.com", PatternTypeGlob},          // glob wildcard
		{"api.?.com", PatternTypeGlob},              // glob single char
		{"^api\\.example\\.com$", PatternTypeRegex}, // regex anchors
		{"api.(dev|prod).com", PatternTypeRegex},    // regex alternation
		{"[a-z]+.example.com", PatternTypeRegex},    // regex character class
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			rule := FilterRule{Pattern: tt.pattern}
			got := rule.DetectPatternType()
			if got != tt.expected {
				t.Errorf("DetectPatternType(%q) = %s, want %s", tt.pattern, got, tt.expected)
			}
		})
	}
}

func TestBlockResponse(t *testing.T) {
	req := &http.Request{
		Host: "blocked.example.com",
		URL:  &url.URL{Host: "blocked.example.com", Path: "/test"},
	}

	resp := BlockResponse(req, "test block reason")

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Blocked-By") != "devsandbox" {
		t.Errorf("expected X-Blocked-By header")
	}
}

func TestNormalizeHost(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Basic cases
		{"example.com", "example.com"},
		{"example.com:8080", "example.com"},
		{"example.com:443", "example.com"},

		// IPv4
		{"127.0.0.1", "127.0.0.1"},
		{"127.0.0.1:8080", "127.0.0.1"},

		// IPv6
		{"[::1]", "::1"},
		{"[::1]:8080", "::1"},
		{"[2001:db8::1]", "2001:db8::1"},
		{"[2001:db8::1]:443", "2001:db8::1"},

		// Edge cases
		{"", ""},
		{"localhost", "localhost"},
		{"localhost:80", "localhost"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizeHost(tt.input)
			if got != tt.expected {
				t.Errorf("NormalizeHost(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestFilterEngine_CacheNormalization(t *testing.T) {
	cfg := &FilterConfig{
		DefaultAction:  FilterActionAsk,
		CacheDecisions: boolPtr(true),
	}

	engine, err := NewFilterEngine(cfg)
	if err != nil {
		t.Fatalf("failed to create filter engine: %v", err)
	}

	// Cache a decision for host without port
	engine.CacheDecision("example.com", FilterActionAllow)

	// Retrieve using host with port - should find the cached decision
	cached := engine.getCachedDecision("example.com:8080")
	if cached != FilterActionAllow {
		t.Errorf("expected cached decision for host:port, got %s", cached)
	}

	// Also verify IPv6 normalization
	engine.CacheDecision("[::1]:8080", FilterActionBlock)
	cached = engine.getCachedDecision("::1")
	if cached != FilterActionBlock {
		t.Errorf("expected cached decision for IPv6, got %s", cached)
	}
}

func boolPtr(b bool) *bool {
	return &b
}
