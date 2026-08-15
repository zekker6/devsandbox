package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/bmatcuk/doublestar/v4"
)

func TestCompilePattern(t *testing.T) {
	t.Run("exact match", func(t *testing.T) {
		m, err := compilePattern("api.example.com", PatternTypeExact)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !m("api.example.com") {
			t.Errorf("exact pattern should match identical string")
		}
		if m("api.example.com.evil.test") {
			t.Errorf("exact pattern should not match suffix")
		}
		if m("API.example.com") {
			t.Errorf("exact pattern is case-sensitive")
		}
	})

	t.Run("glob match", func(t *testing.T) {
		m, err := compilePattern("*.example.com", PatternTypeGlob)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !m("api.example.com") {
			t.Errorf("glob should match subdomain")
		}
		if !m("foo.example.com") {
			t.Errorf("glob should match different subdomain")
		}
		if m("example.com") {
			t.Errorf("glob *.example.com should not match bare apex")
		}
		if m("other.com") {
			t.Errorf("glob should not match unrelated host")
		}
	})

	t.Run("regex match", func(t *testing.T) {
		m, err := compilePattern(`^api\.(staging|prod)\.example\.com$`, PatternTypeRegex)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !m("api.staging.example.com") {
			t.Errorf("regex should match staging")
		}
		if !m("api.prod.example.com") {
			t.Errorf("regex should match prod")
		}
		if m("api.dev.example.com") {
			t.Errorf("regex should not match dev")
		}
	})

	t.Run("invalid regex error", func(t *testing.T) {
		_, err := compilePattern(`(unclosed`, PatternTypeRegex)
		if err == nil {
			t.Fatal("expected error for invalid regex")
		}
		if !strings.Contains(err.Error(), "invalid regex") {
			t.Errorf("expected wrapped 'invalid regex' message, got: %v", err)
		}
	})

	t.Run("invalid glob error", func(t *testing.T) {
		_, err := compilePattern("[unclosed", PatternTypeGlob)
		if err == nil {
			t.Fatal("expected error for invalid glob")
		}
		if !strings.Contains(err.Error(), "invalid glob") {
			t.Errorf("expected 'invalid glob' message, got: %v", err)
		}
	})

	t.Run("unknown pattern type error", func(t *testing.T) {
		_, err := compilePattern("anything", PatternType("bogus"))
		if err == nil {
			t.Fatal("expected error for unknown pattern type")
		}
		if !strings.Contains(err.Error(), "unknown pattern type") {
			t.Errorf("expected 'unknown pattern type' message, got: %v", err)
		}
	})
}

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

		// Case and trailing dot: both spell the same DNS name
		{"EXAMPLE.COM", "example.com"},
		{"Example.Com:8080", "example.com"},
		{"example.com.", "example.com"},
		{"EXAMPLE.COM.:443", "example.com"},
		{"[2001:DB8::1]:443", "2001:db8::1"},
		// A bare root label is left alone rather than normalized to empty
		{".", "."},
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

// TestFilterEngine_HostAliasing_Blacklist covers the DNS spellings that reach
// the same server: hostnames are case-insensitive and a single trailing dot is
// the fully-qualified form of the same name. All of them must hit the block
// rule rather than falling through to the allow default.
func TestFilterEngine_HostAliasing_Blacklist(t *testing.T) {
	cfg := &FilterConfig{
		DefaultAction: FilterActionAllow,
		Rules: []FilterRule{
			{Pattern: "blocked.example.com", Action: FilterActionBlock, Scope: FilterScopeHost},
			{Pattern: "*.tracking.io", Action: FilterActionBlock, Scope: FilterScopeHost},
		},
	}

	engine, err := NewFilterEngine(cfg)
	if err != nil {
		t.Fatalf("failed to create filter engine: %v", err)
	}

	tests := []struct {
		name string
		host string
	}{
		{"uppercase", "BLOCKED.EXAMPLE.COM"},
		{"mixed case", "Blocked.Example.Com"},
		{"trailing dot", "blocked.example.com."},
		{"mixed case, trailing dot and port", "BLOCKED.example.com.:443"},
		{"glob uppercase", "METRICS.TRACKING.IO"},
		{"glob trailing dot", "metrics.tracking.io."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{
				Host: tt.host,
				URL:  &url.URL{Host: tt.host, Path: "/"},
			}
			decision := engine.Match(req)
			if decision.Action != FilterActionBlock {
				t.Errorf("host %q: got action %s, want %s", tt.host, decision.Action, FilterActionBlock)
			}
		})
	}
}

// TestFilterEngine_HostAliasing_NarrowBlockFirst guards the rule-order case:
// under a whitelist default, a narrow block rule precedes a broader allow glob.
// An aliased spelling of the blocked host must not skip the block and land on
// the allow.
func TestFilterEngine_HostAliasing_NarrowBlockFirst(t *testing.T) {
	cfg := &FilterConfig{
		DefaultAction: FilterActionBlock,
		Rules: []FilterRule{
			{Pattern: "secrets.example.com", Action: FilterActionBlock, Scope: FilterScopeHost},
			{Pattern: "*.example.com", Action: FilterActionAllow, Scope: FilterScopeHost},
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
		// wantRule is the pattern of the rule that must have produced the
		// decision, or "" when the default action is expected. Asserting the
		// rule matters here: an aliased spelling of the blocked host that the
		// broad allow glob swallows, and one that falls through to the block
		// default, differ only in this field.
		wantRule string
	}{
		{"canonical blocked", "secrets.example.com", FilterActionBlock, "secrets.example.com"},
		{"uppercase blocked", "SECRETS.EXAMPLE.COM", FilterActionBlock, "secrets.example.com"},
		{"trailing dot blocked", "secrets.example.com.", FilterActionBlock, "secrets.example.com"},
		// Pre-fix this spelling matched the broad allow glob (doublestar's `*`
		// swallows "Secrets") while missing the narrow block, inverting the
		// decision rather than merely losing the rule attribution.
		{"leading-cap blocked", "Secrets.example.com", FilterActionBlock, "secrets.example.com"},
		{"uppercase allowed sibling", "API.EXAMPLE.COM", FilterActionAllow, "*.example.com"},
		{"unlisted host", "other.test", FilterActionBlock, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{
				Host: tt.host,
				URL:  &url.URL{Host: tt.host, Path: "/"},
			}
			decision := engine.Match(req)
			if decision.Action != tt.expected {
				t.Errorf("host %q: got action %s, want %s", tt.host, decision.Action, tt.expected)
			}
			switch {
			case tt.wantRule == "":
				if !decision.IsDefault {
					t.Errorf("host %q: expected the default action, got rule %+v", tt.host, decision.Rule)
				}
			case decision.Rule == nil:
				t.Errorf("host %q: expected rule %q, got the default action", tt.host, tt.wantRule)
			case decision.Rule.Pattern != tt.wantRule:
				t.Errorf("host %q: matched rule %q, want %q", tt.host, decision.Rule.Pattern, tt.wantRule)
			}
		})
	}
}

// TestFilterEngine_CacheHostAliasing asserts aliased spellings share one
// ask-mode cache entry rather than each getting their own.
func TestFilterEngine_CacheHostAliasing(t *testing.T) {
	cfg := &FilterConfig{
		DefaultAction:  FilterActionAsk,
		CacheDecisions: boolPtr(true),
	}

	engine, err := NewFilterEngine(cfg)
	if err != nil {
		t.Fatalf("failed to create filter engine: %v", err)
	}

	engine.CacheDecision("EXAMPLE.com", FilterActionAllow)

	for _, host := range []string{"example.com", "example.com.", "EXAMPLE.COM:443", "Example.Com."} {
		if cached := engine.getCachedDecision(host); cached != FilterActionAllow {
			t.Errorf("getCachedDecision(%q) = %q, want %q", host, cached, FilterActionAllow)
		}
	}

	// The engine's own lookup path must agree with the cache.
	req := &http.Request{
		Host: "Example.COM.:443",
		URL:  &url.URL{Host: "Example.COM.:443", Path: "/"},
	}
	if decision := engine.Match(req); decision.Action != FilterActionAllow {
		t.Errorf("Match on aliased host: got %s, want %s", decision.Action, FilterActionAllow)
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

// TestFilterEngine_ConcurrentReaders exercises every reader of the immutable
// config and rule set alongside the writers of the decision cache, so `-race`
// reports if the two are ever conflated. Match, MatchHost, IsEnabled and Config
// read state written only in NewFilterEngine; CacheDecision and ClearCache
// write the cache, which cacheMu guards.
func TestFilterEngine_ConcurrentReaders(t *testing.T) {
	cfg := &FilterConfig{
		DefaultAction:  FilterActionAllow,
		CacheDecisions: boolPtr(true),
		Rules: []FilterRule{
			{Pattern: "blocked.example.com", Action: FilterActionBlock, Scope: FilterScopeHost},
			{Pattern: "*.internal", Action: FilterActionBlock, Scope: FilterScopeHost},
		},
	}

	engine, err := NewFilterEngine(cfg)
	if err != nil {
		t.Fatalf("failed to create filter engine: %v", err)
	}

	const (
		workers    = 16
		iterations = 200
	)

	var wg sync.WaitGroup
	errs := make(chan string, workers*4)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()

			req := &http.Request{
				Host: "blocked.example.com",
				URL:  &url.URL{Host: "blocked.example.com", Path: "/"},
			}

			for n := 0; n < iterations; n++ {
				switch worker % 4 {
				case 0:
					if decision := engine.Match(req); decision.Action != FilterActionBlock {
						errs <- "Match: got " + string(decision.Action) + ", want " + string(FilterActionBlock)
						return
					}
				case 1:
					if decision := engine.MatchHost("blocked.example.com:443"); decision.Action != FilterActionBlock {
						errs <- "MatchHost: got " + string(decision.Action) + ", want " + string(FilterActionBlock)
						return
					}
				case 2:
					if !engine.IsEnabled() {
						errs <- "IsEnabled: got false, want true"
						return
					}
					if engine.Config() == nil {
						errs <- "Config: got nil"
						return
					}
				case 3:
					engine.CacheDecision("cached.example.com", FilterActionAllow)
					engine.getCachedDecision("cached.example.com")
					if n%50 == 0 {
						engine.ClearCache()
					}
				}
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for msg := range errs {
		t.Error(msg)
	}
}

func boolPtr(b bool) *bool {
	return &b
}

// TestHostRegexIsCaseInsensitive pins the fix for a rule silently retired by
// host canonicalization. NormalizeHost lowercases every match target, so a
// host-scoped regex written with any uppercase - which matched before
// canonicalization landed - could never match again, turning an existing block
// rule into a no-op on upgrade with no error and no log line.
func TestHostRegexIsCaseInsensitive(t *testing.T) {
	engine, err := NewFilterEngine(&FilterConfig{
		DefaultAction: FilterActionAllow,
		Rules: []FilterRule{
			{Pattern: `^API\.Example\.com$`, Type: PatternTypeRegex, Scope: FilterScopeHost, Action: FilterActionBlock},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, host := range []string{"api.example.com", "API.EXAMPLE.COM:443", "Api.Example.Com.", "api.example.com:8080"} {
		if got := engine.MatchHost(host).Action; got != FilterActionBlock {
			t.Errorf("MatchHost(%q) = %q, want block", host, got)
		}
	}
	if got := engine.MatchHost("other.example.com").Action; got != FilterActionAllow {
		t.Errorf("MatchHost(other.example.com) = %q, want allow", got)
	}
}

// TestURLScopeCanonicalizesAuthority pins the other half: url.Parse leaves the
// authority's case alone, so a url-scoped rule matched against the raw string
// was evaded by asking for the same resource in a different host spelling. The
// path stays case-sensitive, which is the half of a URL that actually is.
func TestURLScopeCanonicalizesAuthority(t *testing.T) {
	engine, err := NewFilterEngine(&FilterConfig{
		DefaultAction: FilterActionAllow,
		Rules: []FilterRule{
			{Pattern: "https://api.example.com/**", Type: PatternTypeGlob, Scope: FilterScopeURL, Action: FilterActionBlock},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, raw := range []string{
		"https://api.example.com/secret",
		"https://API.EXAMPLE.COM/secret",
		"https://Api.Example.Com/secret",
		"https://api.example.com./secret",
	} {
		req := httptest.NewRequest(http.MethodGet, raw, nil)
		if got := engine.Match(req).Action; got != FilterActionBlock {
			t.Errorf("Match(%q) = %q, want block", raw, got)
		}
	}

	// A different host is still unaffected, and the path half stays literal.
	req := httptest.NewRequest(http.MethodGet, "https://other.example.com/secret", nil)
	if got := engine.Match(req).Action; got != FilterActionAllow {
		t.Errorf("Match(other host) = %q, want allow", got)
	}
}

// TestURLScopeCanonicalizesPattern is the other side of the same comparison.
// Canonicalizing only the target silently retires every url rule whose pattern
// spells its host with any uppercase - it can no longer match anything, and
// under an allow default a block rule that matches nothing is not an error.
func TestURLScopeCanonicalizesPattern(t *testing.T) {
	engine, err := NewFilterEngine(&FilterConfig{
		DefaultAction: FilterActionAllow,
		Rules: []FilterRule{
			{Pattern: "https://API.Example.com./**", Type: PatternTypeGlob, Scope: FilterScopeURL, Action: FilterActionBlock},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "https://api.example.com/secret", nil)
	if got := engine.Match(req).Action; got != FilterActionBlock {
		t.Errorf("Match = %q, want block", got)
	}
}

// TestURLScopeDropsDefaultPort covers the spelling every intercepted HTTPS
// request actually arrives in. goproxy rebuilds req.URL from the CONNECT
// target, which always carries a port, so req.URL.String() is
// "https://host:443/..." - and no url rule written the way the docs write them
// could ever match one.
func TestURLScopeDropsDefaultPort(t *testing.T) {
	engine, err := NewFilterEngine(&FilterConfig{
		DefaultAction: FilterActionAllow,
		Rules: []FilterRule{
			{Pattern: "https://api.example.com/**", Type: PatternTypeGlob, Scope: FilterScopeURL, Action: FilterActionBlock},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, raw := range []string{
		"https://api.example.com/v1/users",
		"https://api.example.com:443/v1/users",
	} {
		req := httptest.NewRequest(http.MethodGet, raw, nil)
		if got := engine.Match(req).Action; got != FilterActionBlock {
			t.Errorf("Match(%q) = %q, want block", raw, got)
		}
	}

	// A non-default port is a different endpoint and stays outside the rule.
	req := httptest.NewRequest(http.MethodGet, "https://api.example.com:8443/v1/users", nil)
	if got := engine.Match(req).Action; got != FilterActionAllow {
		t.Errorf("Match(explicit non-default port) = %q, want allow", got)
	}
}

// TestHostScopeMatchesConnectTargetNotHostHeader is the bypass this pins shut.
// On the MITM path goproxy rebuilds req.URL from the CONNECT target and leaves
// req.Host as the tunneled request spelled it, while the transport dials
// req.URL.Host. Matching req.Host meant `CONNECT evil:443` carrying
// `Host: allowed` was checked against a name it was not contacting - and the
// same value keyed the ask-mode decision cache, so one approval was reusable
// against any destination.
func TestHostScopeMatchesConnectTargetNotHostHeader(t *testing.T) {
	engine, err := NewFilterEngine(&FilterConfig{
		DefaultAction: FilterActionAllow,
		Rules: []FilterRule{
			{Pattern: "evil.example.com", Type: PatternTypeExact, Scope: FilterScopeHost, Action: FilterActionBlock},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "https://evil.example.com:443/x", nil)
	req.Host = "allowed.example.com"
	if got := engine.Match(req).Action; got != FilterActionBlock {
		t.Errorf("Match = %q, want block (matched the Host header, not the connect target)", got)
	}

	// And the mirror: a forged Host header must not pull an unrelated rule in.
	req = httptest.NewRequest(http.MethodGet, "https://allowed.example.com:443/x", nil)
	req.Host = "evil.example.com"
	if got := engine.Match(req).Action; got != FilterActionAllow {
		t.Errorf("Match = %q, want allow", got)
	}
}

// TestUsesAskAction pins what decides whether the ask queue gets built. Keying
// it on the default action alone left `action = "ask"` on a rule with nothing to
// ask: the request was allowed through unprompted while its log entry recorded
// FilterAction="ask", so the audit trail asserted an approval nobody gave.
func TestUsesAskAction(t *testing.T) {
	tests := []struct {
		name string
		cfg  *FilterConfig
		want bool
	}{
		{"nil config", nil, false},
		{"allow default, no rules", &FilterConfig{DefaultAction: FilterActionAllow}, false},
		{"ask default", &FilterConfig{DefaultAction: FilterActionAsk}, true},
		{"allow default, ask rule", &FilterConfig{
			DefaultAction: FilterActionAllow,
			Rules:         []FilterRule{{Pattern: "*.example.com", Action: FilterActionAsk}},
		}, true},
		{"block default, ask rule", &FilterConfig{
			DefaultAction: FilterActionBlock,
			Rules:         []FilterRule{{Pattern: "*.example.com", Action: FilterActionAsk}},
		}, true},
		{"block default, no ask rule", &FilterConfig{
			DefaultAction: FilterActionBlock,
			Rules:         []FilterRule{{Pattern: "*.example.com", Action: FilterActionAllow}},
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.usesAskAction(); got != tt.want {
				t.Errorf("usesAskAction() = %v, want %v", got, tt.want)
			}
		})
	}
}
