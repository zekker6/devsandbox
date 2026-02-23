package proxy

import (
	"testing"
)

func TestRedactionConfig_IsEnabled(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *RedactionConfig
		expected bool
	}{
		{"nil config", nil, false},
		{"nil enabled defaults to false", &RedactionConfig{}, false},
		{"explicit true", &RedactionConfig{Enabled: boolPtr(true)}, true},
		{"explicit false", &RedactionConfig{Enabled: boolPtr(false)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.IsEnabled(); got != tt.expected {
				t.Errorf("IsEnabled() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestRedactionConfig_GetDefaultAction(t *testing.T) {
	tests := []struct {
		name     string
		action   RedactionAction
		expected RedactionAction
	}{
		{"empty defaults to block", "", RedactionActionBlock},
		{"explicit block", RedactionActionBlock, RedactionActionBlock},
		{"explicit redact", RedactionActionRedact, RedactionActionRedact},
		{"explicit log", RedactionActionLog, RedactionActionLog},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &RedactionConfig{DefaultAction: tt.action}
			if got := cfg.GetDefaultAction(); got != tt.expected {
				t.Errorf("GetDefaultAction() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestRedactionConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *RedactionConfig
		wantErr bool
	}{
		{
			"valid source rule",
			&RedactionConfig{
				Enabled:       boolPtr(true),
				DefaultAction: RedactionActionBlock,
				Rules: []RedactionRule{
					{Source: &RedactionSource{Env: "SECRET"}},
				},
			},
			false,
		},
		{
			"valid pattern rule",
			&RedactionConfig{
				Enabled:       boolPtr(true),
				DefaultAction: RedactionActionBlock,
				Rules: []RedactionRule{
					{Pattern: "sk-[a-zA-Z0-9]+"},
				},
			},
			false,
		},
		{
			"invalid: both source and pattern",
			&RedactionConfig{
				Enabled:       boolPtr(true),
				DefaultAction: RedactionActionBlock,
				Rules: []RedactionRule{
					{Source: &RedactionSource{Env: "SECRET"}, Pattern: "sk-.*"},
				},
			},
			true,
		},
		{
			"invalid: neither source nor pattern",
			&RedactionConfig{
				Enabled:       boolPtr(true),
				DefaultAction: RedactionActionBlock,
				Rules: []RedactionRule{
					{Name: "empty-rule"},
				},
			},
			true,
		},
		{
			"invalid: source with no fields set",
			&RedactionConfig{
				Enabled:       boolPtr(true),
				DefaultAction: RedactionActionBlock,
				Rules: []RedactionRule{
					{Source: &RedactionSource{}},
				},
			},
			true,
		},
		{
			"invalid action",
			&RedactionConfig{
				Enabled:       boolPtr(true),
				DefaultAction: "explode",
			},
			true,
		},
		{
			"invalid rule action",
			&RedactionConfig{
				Enabled:       boolPtr(true),
				DefaultAction: RedactionActionBlock,
				Rules: []RedactionRule{
					{Source: &RedactionSource{Env: "SECRET"}, Action: "explode"},
				},
			},
			true,
		},
		{
			"invalid regex pattern",
			&RedactionConfig{
				Enabled:       boolPtr(true),
				DefaultAction: RedactionActionBlock,
				Rules: []RedactionRule{
					{Pattern: "[invalid"},
				},
			},
			true,
		},
		{
			"auto-generated name",
			&RedactionConfig{
				Enabled:       boolPtr(true),
				DefaultAction: RedactionActionBlock,
				Rules: []RedactionRule{
					{Source: &RedactionSource{Env: "SECRET"}},
				},
			},
			false,
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

func TestRedactionRule_GetName(t *testing.T) {
	tests := []struct {
		name     string
		rule     RedactionRule
		index    int
		expected string
	}{
		{"explicit name", RedactionRule{Name: "my-secret"}, 0, "my-secret"},
		{"auto-generated index 0", RedactionRule{}, 0, "rule-1"},
		{"auto-generated index 4", RedactionRule{}, 4, "rule-5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.rule.GetName(tt.index); got != tt.expected {
				t.Errorf("GetName(%d) = %v, want %v", tt.index, got, tt.expected)
			}
		})
	}
}

func TestRedactionRule_GetAction(t *testing.T) {
	defaultAction := RedactionActionBlock
	tests := []struct {
		name     string
		rule     RedactionRule
		expected RedactionAction
	}{
		{"explicit action", RedactionRule{Action: RedactionActionLog}, RedactionActionLog},
		{"falls back to default", RedactionRule{}, defaultAction},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.rule.GetAction(defaultAction); got != tt.expected {
				t.Errorf("GetAction() = %v, want %v", got, tt.expected)
			}
		})
	}
}
