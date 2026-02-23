package proxy

import (
	"fmt"
	"regexp"
)

// RedactionAction represents the action to take when a secret is detected.
type RedactionAction string

const (
	// RedactionActionBlock blocks the request with HTTP 403.
	RedactionActionBlock RedactionAction = "block"
	// RedactionActionRedact replaces the secret and forwards the request.
	RedactionActionRedact RedactionAction = "redact"
	// RedactionActionLog allows the request but logs a warning.
	RedactionActionLog RedactionAction = "log"
)

// RedactionConfig holds the complete redaction configuration.
type RedactionConfig struct {
	// Enabled enables content redaction scanning. Default: false.
	Enabled *bool `toml:"enabled"`
	// DefaultAction is the action when a secret is detected and the rule has no override.
	DefaultAction RedactionAction `toml:"default_action"`
	// Rules is the list of redaction rules.
	Rules []RedactionRule `toml:"rules"`
}

// IsEnabled returns true if redaction scanning is enabled.
func (c *RedactionConfig) IsEnabled() bool {
	if c == nil || c.Enabled == nil {
		return false
	}
	return *c.Enabled
}

// GetDefaultAction returns the default action, defaulting to block.
func (c *RedactionConfig) GetDefaultAction() RedactionAction {
	if c.DefaultAction == "" {
		return RedactionActionBlock
	}
	return c.DefaultAction
}

// Validate checks the redaction configuration for errors.
func (c *RedactionConfig) Validate() error {
	if c.DefaultAction != "" {
		switch c.DefaultAction {
		case RedactionActionBlock, RedactionActionRedact, RedactionActionLog:
			// valid
		default:
			return fmt.Errorf("invalid default_action: %q (must be block, redact, or log)", c.DefaultAction)
		}
	}

	for i := range c.Rules {
		if err := c.Rules[i].Validate(); err != nil {
			return fmt.Errorf("rule %d: %w", i+1, err)
		}
	}
	return nil
}

// RedactionRule defines a single redaction rule.
type RedactionRule struct {
	// Name is a human-readable identifier for this rule.
	// Optional; auto-generated as "rule-<index>" if omitted.
	Name string `toml:"name"`
	// Action overrides the default action for this rule. Optional.
	Action RedactionAction `toml:"action"`
	// Source resolves the secret value to scan for. Mutually exclusive with Pattern.
	Source *RedactionSource `toml:"source"`
	// Pattern is a regex pattern to scan for. Mutually exclusive with Source.
	Pattern string `toml:"pattern"`
}

// GetName returns the rule name, generating one from the index if not set.
func (r *RedactionRule) GetName(index int) string {
	if r.Name != "" {
		return r.Name
	}
	return fmt.Sprintf("rule-%d", index+1)
}

// GetAction returns the rule action, falling back to the given default.
func (r *RedactionRule) GetAction(defaultAction RedactionAction) RedactionAction {
	if r.Action != "" {
		return r.Action
	}
	return defaultAction
}

// Validate checks a redaction rule for errors.
func (r *RedactionRule) Validate() error {
	hasSource := r.Source != nil
	hasPattern := r.Pattern != ""
	if hasSource && hasPattern {
		return fmt.Errorf("source and pattern are mutually exclusive")
	}
	if !hasSource && !hasPattern {
		return fmt.Errorf("either source or pattern is required")
	}

	if hasSource {
		if r.Source.Value == "" && r.Source.Env == "" && r.Source.File == "" && r.Source.EnvFileKey == "" {
			return fmt.Errorf("source must have at least one field set (value, env, file, or env_file_key)")
		}
	}

	if hasPattern {
		if _, err := regexp.Compile(r.Pattern); err != nil {
			return fmt.Errorf("invalid regex pattern: %w", err)
		}
	}

	if r.Action != "" {
		switch r.Action {
		case RedactionActionBlock, RedactionActionRedact, RedactionActionLog:
			// valid
		default:
			return fmt.Errorf("invalid action: %q (must be block, redact, or log)", r.Action)
		}
	}

	return nil
}

// RedactionSource resolves a secret value from a configured source.
type RedactionSource struct {
	// Value is a static secret value.
	Value string `toml:"value"`
	// Env is the name of an environment variable containing the secret.
	Env string `toml:"env"`
	// File is the path to a file containing the secret. Supports ~ expansion.
	File string `toml:"file"`
	// EnvFileKey is the key name to look up in project .env files.
	EnvFileKey string `toml:"env_file_key"`
}

// RedactionResult holds the outcome of scanning a request.
type RedactionResult struct {
	// Matched is true if any rule matched.
	Matched bool
	// Action is the effective action (highest severity across all matches).
	Action RedactionAction
	// Matches lists all individual rule matches.
	Matches []RedactionMatch
	// Body is the redacted body (set when Action is redact or block).
	Body []byte
	// Headers are the redacted headers (set when Action is redact or block).
	Headers map[string][]string
	// URL is the redacted URL (set when Action is redact or block).
	URL string
}

// RedactionMatch represents a single rule match.
type RedactionMatch struct {
	// RuleName identifies which rule matched.
	RuleName string
	// Location describes where the match was found: "url", "header:<Name>", or "body".
	Location string
	// Action is this specific rule's action.
	Action RedactionAction
}
