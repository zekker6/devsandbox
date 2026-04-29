package proxy

import (
	"strings"
	"testing"
)

func TestLogSkipRule_Validate(t *testing.T) {
	tests := []struct {
		name    string
		rule    LogSkipRule
		wantErr string // substring; empty means no error expected
	}{
		{
			name: "valid host glob",
			rule: LogSkipRule{Pattern: "*.example.com", Scope: FilterScopeHost, Type: PatternTypeGlob},
		},
		{
			name: "valid defaults (empty scope and type)",
			rule: LogSkipRule{Pattern: "telemetry.example.com"},
		},
		{
			name: "valid path",
			rule: LogSkipRule{Pattern: "/v1/metrics", Scope: FilterScopePath, Type: PatternTypeExact},
		},
		{
			name: "valid url regex",
			rule: LogSkipRule{Pattern: `^https://api\.example\.com/v1/.*$`, Scope: FilterScopeURL, Type: PatternTypeRegex},
		},
		{
			name:    "empty pattern",
			rule:    LogSkipRule{Scope: FilterScopeHost},
			wantErr: "pattern is required",
		},
		{
			name:    "invalid scope",
			rule:    LogSkipRule{Pattern: "x", Scope: "bogus"},
			wantErr: "invalid scope",
		},
		{
			name:    "invalid type",
			rule:    LogSkipRule{Pattern: "x", Type: "bogus"},
			wantErr: "invalid type",
		},
		{
			name:    "invalid regex pattern",
			rule:    LogSkipRule{Pattern: `(unclosed`, Type: PatternTypeRegex},
			wantErr: "invalid regex pattern",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.rule.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestLogSkipConfig_Validate(t *testing.T) {
	t.Run("nil config is valid", func(t *testing.T) {
		var cfg *LogSkipConfig
		if err := cfg.Validate(); err != nil {
			t.Errorf("nil config should be valid, got %v", err)
		}
	})

	t.Run("empty rules is valid", func(t *testing.T) {
		cfg := &LogSkipConfig{}
		if err := cfg.Validate(); err != nil {
			t.Errorf("empty rules should be valid, got %v", err)
		}
	})

	t.Run("valid rules pass", func(t *testing.T) {
		cfg := &LogSkipConfig{Rules: []LogSkipRule{
			{Pattern: "host1.example.com"},
			{Pattern: "/api/v1/metrics", Scope: FilterScopePath},
		}}
		if err := cfg.Validate(); err != nil {
			t.Errorf("valid rules should pass, got %v", err)
		}
	})

	t.Run("error reports rule index (1-based)", func(t *testing.T) {
		cfg := &LogSkipConfig{Rules: []LogSkipRule{
			{Pattern: "ok.example.com"},
			{Pattern: "", Scope: FilterScopeHost},
		}}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error for empty pattern in rule 2")
		}
		if !strings.Contains(err.Error(), "rule 2") {
			t.Errorf("error should reference rule 2, got: %v", err)
		}
	})
}

func TestLogSkipConfig_IsEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  *LogSkipConfig
		want bool
	}{
		{"nil", nil, false},
		{"empty", &LogSkipConfig{}, false},
		{"with rules", &LogSkipConfig{Rules: []LogSkipRule{{Pattern: "x"}}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.IsEnabled(); got != tt.want {
				t.Errorf("IsEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLogSkipRule_GetScope(t *testing.T) {
	tests := []struct {
		name string
		rule LogSkipRule
		want FilterScope
	}{
		{"empty defaults to host", LogSkipRule{Pattern: "x"}, FilterScopeHost},
		{"explicit host", LogSkipRule{Pattern: "x", Scope: FilterScopeHost}, FilterScopeHost},
		{"explicit path", LogSkipRule{Pattern: "x", Scope: FilterScopePath}, FilterScopePath},
		{"explicit url", LogSkipRule{Pattern: "x", Scope: FilterScopeURL}, FilterScopeURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.rule.GetScope(); got != tt.want {
				t.Errorf("GetScope() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewLogSkipEngine(t *testing.T) {
	t.Run("nil config returns empty engine", func(t *testing.T) {
		e, err := NewLogSkipEngine(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if e == nil {
			t.Fatal("expected non-nil engine")
		}
		if e.ShouldSkip(&RequestLog{URL: "https://example.com/"}) {
			t.Error("nil-config engine should never skip")
		}
	})

	t.Run("empty rules returns empty engine", func(t *testing.T) {
		e, err := NewLogSkipEngine(&LogSkipConfig{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if e.ShouldSkip(&RequestLog{URL: "https://example.com/"}) {
			t.Error("empty-rules engine should never skip")
		}
	})

	t.Run("invalid regex returns error with rule index", func(t *testing.T) {
		_, err := NewLogSkipEngine(&LogSkipConfig{Rules: []LogSkipRule{
			{Pattern: "ok.example.com"},
			{Pattern: `(unclosed`, Type: PatternTypeRegex},
		}})
		if err == nil {
			t.Fatal("expected error for invalid regex")
		}
		if !strings.Contains(err.Error(), "rule 2") {
			t.Errorf("error should reference rule 2, got: %v", err)
		}
	})

	t.Run("invalid scope rejected at construction", func(t *testing.T) {
		_, err := NewLogSkipEngine(&LogSkipConfig{Rules: []LogSkipRule{
			{Pattern: "x", Scope: "bogus"},
		}})
		if err == nil {
			t.Fatal("expected error for invalid scope")
		}
	})
}

func TestLogSkipEngine_ShouldSkip(t *testing.T) {
	tests := []struct {
		name string
		cfg  *LogSkipConfig
		url  string
		want bool
	}{
		// host scope × pattern type
		{
			name: "host exact match",
			cfg:  &LogSkipConfig{Rules: []LogSkipRule{{Pattern: "telemetry.example.com", Type: PatternTypeExact}}},
			url:  "https://telemetry.example.com/v1/traces",
			want: true,
		},
		{
			name: "host exact miss",
			cfg:  &LogSkipConfig{Rules: []LogSkipRule{{Pattern: "telemetry.example.com", Type: PatternTypeExact}}},
			url:  "https://api.example.com/",
			want: false,
		},
		{
			name: "host glob match",
			cfg:  &LogSkipConfig{Rules: []LogSkipRule{{Pattern: "*.telemetry.example.com"}}},
			url:  "https://otlp.telemetry.example.com/v1/metrics",
			want: true,
		},
		{
			name: "host glob miss",
			cfg:  &LogSkipConfig{Rules: []LogSkipRule{{Pattern: "*.telemetry.example.com"}}},
			url:  "https://api.example.com/",
			want: false,
		},
		{
			name: "host regex match",
			cfg:  &LogSkipConfig{Rules: []LogSkipRule{{Pattern: `^(otlp|telemetry)\.example\.com$`, Type: PatternTypeRegex}}},
			url:  "https://otlp.example.com/",
			want: true,
		},
		{
			name: "host regex miss",
			cfg:  &LogSkipConfig{Rules: []LogSkipRule{{Pattern: `^(otlp|telemetry)\.example\.com$`, Type: PatternTypeRegex}}},
			url:  "https://api.example.com/",
			want: false,
		},

		// path scope × pattern type
		{
			name: "path exact match",
			cfg:  &LogSkipConfig{Rules: []LogSkipRule{{Pattern: "/v1/traces", Scope: FilterScopePath, Type: PatternTypeExact}}},
			url:  "https://api.example.com/v1/traces",
			want: true,
		},
		{
			name: "path exact miss",
			cfg:  &LogSkipConfig{Rules: []LogSkipRule{{Pattern: "/v1/traces", Scope: FilterScopePath, Type: PatternTypeExact}}},
			url:  "https://api.example.com/v1/metrics",
			want: false,
		},
		{
			name: "path glob match",
			cfg:  &LogSkipConfig{Rules: []LogSkipRule{{Pattern: "/v1/*", Scope: FilterScopePath}}},
			url:  "https://api.example.com/v1/traces",
			want: true,
		},
		{
			name: "path glob miss",
			cfg:  &LogSkipConfig{Rules: []LogSkipRule{{Pattern: "/v1/*", Scope: FilterScopePath}}},
			url:  "https://api.example.com/v2/traces",
			want: false,
		},
		{
			name: "path regex match",
			cfg:  &LogSkipConfig{Rules: []LogSkipRule{{Pattern: `^/v[12]/traces$`, Scope: FilterScopePath, Type: PatternTypeRegex}}},
			url:  "https://api.example.com/v2/traces",
			want: true,
		},
		{
			name: "path regex miss",
			cfg:  &LogSkipConfig{Rules: []LogSkipRule{{Pattern: `^/v[12]/traces$`, Scope: FilterScopePath, Type: PatternTypeRegex}}},
			url:  "https://api.example.com/v3/traces",
			want: false,
		},

		// url scope × pattern type
		{
			name: "url exact match",
			cfg:  &LogSkipConfig{Rules: []LogSkipRule{{Pattern: "https://api.example.com/v1/traces", Scope: FilterScopeURL, Type: PatternTypeExact}}},
			url:  "https://api.example.com/v1/traces",
			want: true,
		},
		{
			name: "url exact miss (different path)",
			cfg:  &LogSkipConfig{Rules: []LogSkipRule{{Pattern: "https://api.example.com/v1/traces", Scope: FilterScopeURL, Type: PatternTypeExact}}},
			url:  "https://api.example.com/v1/metrics",
			want: false,
		},
		{
			name: "url glob match",
			cfg:  &LogSkipConfig{Rules: []LogSkipRule{{Pattern: "https://*.example.com/v1/**", Scope: FilterScopeURL}}},
			url:  "https://api.example.com/v1/traces",
			want: true,
		},
		{
			name: "url glob miss",
			cfg:  &LogSkipConfig{Rules: []LogSkipRule{{Pattern: "https://*.example.com/v1/**", Scope: FilterScopeURL}}},
			url:  "https://api.example.com/v2/traces",
			want: false,
		},
		{
			name: "url regex match",
			cfg:  &LogSkipConfig{Rules: []LogSkipRule{{Pattern: `^https://api\.example\.com/v\d+/.*$`, Scope: FilterScopeURL, Type: PatternTypeRegex}}},
			url:  "https://api.example.com/v3/something",
			want: true,
		},
		{
			name: "url regex miss",
			cfg:  &LogSkipConfig{Rules: []LogSkipRule{{Pattern: `^https://api\.example\.com/v\d+/.*$`, Scope: FilterScopeURL, Type: PatternTypeRegex}}},
			url:  "https://api.example.com/about",
			want: false,
		},

		// edge cases
		{
			name: "host with port: port stripped before match",
			cfg:  &LogSkipConfig{Rules: []LogSkipRule{{Pattern: "telemetry.example.com", Type: PatternTypeExact}}},
			url:  "https://telemetry.example.com:8443/v1/traces",
			want: true,
		},
		{
			name: "first match wins (first rule)",
			cfg: &LogSkipConfig{Rules: []LogSkipRule{
				{Pattern: "telemetry.example.com", Type: PatternTypeExact}, // matches
				{Pattern: "never.example.com", Type: PatternTypeExact},     // no fall-through expected
			}},
			url:  "https://telemetry.example.com/x",
			want: true,
		},
		{
			name: "first match wins (second rule)",
			cfg: &LogSkipConfig{Rules: []LogSkipRule{
				{Pattern: "first.example.com", Type: PatternTypeExact}, // miss
				{Pattern: "second.example.com", Type: PatternTypeExact},
			}},
			url:  "https://second.example.com/",
			want: true,
		},
		{
			name: "no rules match → no skip",
			cfg:  &LogSkipConfig{Rules: []LogSkipRule{{Pattern: "skipthis.example.com"}}},
			url:  "https://api.example.com/",
			want: false,
		},
		{
			name: "malformed URL → no skip (fail open: log it)",
			cfg:  &LogSkipConfig{Rules: []LogSkipRule{{Pattern: "anything"}}},
			url:  "://broken url with spaces",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, err := NewLogSkipEngine(tt.cfg)
			if err != nil {
				t.Fatalf("engine construction failed: %v", err)
			}
			entry := &RequestLog{URL: tt.url}
			if got := e.ShouldSkip(entry); got != tt.want {
				t.Errorf("ShouldSkip(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestLogSkipEngine_ShouldSkip_NilSafety(t *testing.T) {
	t.Run("nil engine", func(t *testing.T) {
		var e *LogSkipEngine
		if e.ShouldSkip(&RequestLog{URL: "https://example.com/"}) {
			t.Error("nil engine should never skip")
		}
	})

	t.Run("nil entry", func(t *testing.T) {
		e, _ := NewLogSkipEngine(&LogSkipConfig{Rules: []LogSkipRule{{Pattern: "x"}}})
		if e.ShouldSkip(nil) {
			t.Error("nil entry should never skip")
		}
	})
}

func TestLogSkipRule_DetectPatternType(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		typ     PatternType
		want    PatternType
	}{
		{"explicit exact wins", "anything", PatternTypeExact, PatternTypeExact},
		{"explicit glob wins", "anything", PatternTypeGlob, PatternTypeGlob},
		{"explicit regex wins", "anything", PatternTypeRegex, PatternTypeRegex},
		{"plain literal -> glob", "example.com", "", PatternTypeGlob},
		{"glob wildcard -> glob", "*.example.com", "", PatternTypeGlob},
		{"regex anchor -> regex", "^api.example.com$", "", PatternTypeRegex},
		{"regex alternation -> regex", "api.(dev|prod).com", "", PatternTypeRegex},
		{"regex char class -> regex", "[a-z]+.example.com", "", PatternTypeRegex},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := LogSkipRule{Pattern: tt.pattern, Type: tt.typ}
			if got := r.DetectPatternType(); got != tt.want {
				t.Errorf("DetectPatternType() = %v, want %v", got, tt.want)
			}
		})
	}
}
