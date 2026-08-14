package proxy

import (
	"fmt"
	"regexp"
	"strings"
)

// FilterAction represents the action to take for a request.
type FilterAction string

const (
	// FilterActionAllow allows the request to proceed.
	FilterActionAllow FilterAction = "allow"
	// FilterActionBlock blocks the request with HTTP 403.
	FilterActionBlock FilterAction = "block"
	// FilterActionAsk prompts the user for a decision.
	FilterActionAsk FilterAction = "ask"
)

// FilterScope defines what part of the request to match against.
type FilterScope string

const (
	// FilterScopeHost matches against the request host only.
	FilterScopeHost FilterScope = "host"
	// FilterScopePath matches against the request path only.
	FilterScopePath FilterScope = "path"
	// FilterScopeURL matches against the full URL.
	FilterScopeURL FilterScope = "url"
)

// PatternType indicates how the pattern should be matched.
type PatternType string

const (
	// PatternTypeExact matches the exact string.
	PatternTypeExact PatternType = "exact"
	// PatternTypeGlob matches using glob patterns (*, ?).
	PatternTypeGlob PatternType = "glob"
	// PatternTypeRegex matches using regular expressions.
	PatternTypeRegex PatternType = "regex"
)

// FilterRule defines a single filtering rule.
type FilterRule struct {
	// Pattern is the pattern to match against (exact, glob, or regex).
	Pattern string `toml:"pattern"`

	// Action specifies what to do when the rule matches.
	Action FilterAction `toml:"action"`

	// Scope defines what part of the request to match.
	// Default: host
	Scope FilterScope `toml:"scope"`

	// Type specifies the pattern matching type.
	// Default: glob. Auto-detected as regex if pattern contains ^$|()[]{}\+
	Type PatternType `toml:"type"`

	// Reason is an optional human-readable explanation shown when blocking.
	Reason string `toml:"reason"`
}

// FilterConfig holds the complete filter configuration.
// Filtering is enabled when DefaultAction is set.
type FilterConfig struct {
	// DefaultAction is the action when no rule matches.
	// Setting this enables filtering.
	// - "block": block unmatched requests (whitelist behavior)
	// - "allow": allow unmatched requests (blacklist behavior)
	// - "ask": prompt user for unmatched requests
	DefaultAction FilterAction `toml:"default_action"`

	// AskTimeout is the timeout in seconds for ask mode decisions.
	// Default: 30
	AskTimeout int `toml:"ask_timeout"`

	// CacheDecisions enables caching of ask mode decisions for the session.
	// Default: true
	CacheDecisions *bool `toml:"cache_decisions"`

	// Rules is the list of filter rules, evaluated in order.
	Rules []FilterRule `toml:"rules"`
}

// IsEnabled returns true if filtering is enabled.
func (c *FilterConfig) IsEnabled() bool {
	return c != nil && c.DefaultAction != ""
}

// IsCacheEnabled returns whether decision caching is enabled (default: true).
func (c *FilterConfig) IsCacheEnabled() bool {
	if c.CacheDecisions == nil {
		return true
	}
	return *c.CacheDecisions
}

// GetDefaultAction returns the default action.
func (c *FilterConfig) GetDefaultAction() FilterAction {
	if c.DefaultAction == "" {
		return FilterActionAllow
	}
	return c.DefaultAction
}

// GetAskTimeout returns the ask timeout with a default of 30 seconds.
func (c *FilterConfig) GetAskTimeout() int {
	if c.AskTimeout <= 0 {
		return 30
	}
	return c.AskTimeout
}

// usesAskAction reports whether any decision this config can produce is "ask" -
// the default action, or one carried by a single rule. Both need the ask
// queue, and a rule-level ask without it is worse than no ask at all: the
// request is allowed silently and logged as though it had been approved.
func (c *FilterConfig) usesAskAction() bool {
	if c == nil {
		return false
	}
	if c.DefaultAction == FilterActionAsk {
		return true
	}
	for _, rule := range c.Rules {
		if rule.Action == FilterActionAsk {
			return true
		}
	}
	return false
}

// Validate checks the filter configuration for errors.
func (c *FilterConfig) Validate() error {
	// Validate default action
	if c.DefaultAction != "" {
		switch c.DefaultAction {
		case FilterActionAllow, FilterActionBlock, FilterActionAsk:
			// Valid
		default:
			return fmt.Errorf("invalid default_action: %q (must be allow, block, or ask)", c.DefaultAction)
		}
	}

	// Validate rules
	for i, rule := range c.Rules {
		if err := rule.Validate(); err != nil {
			return fmt.Errorf("rule %d: %w", i+1, err)
		}
	}

	return nil
}

// Validate checks a filter rule for errors.
func (r *FilterRule) Validate() error {
	if r.Pattern == "" {
		return fmt.Errorf("pattern is required")
	}

	// Validate action
	switch r.Action {
	case FilterActionAllow, FilterActionBlock, FilterActionAsk:
		// Valid
	case "":
		return fmt.Errorf("action is required")
	default:
		return fmt.Errorf("invalid action: %q (must be allow, block, or ask)", r.Action)
	}

	// Validate scope (default to host if empty)
	switch r.Scope {
	case FilterScopeHost, FilterScopePath, FilterScopeURL, "":
		// Valid
	default:
		return fmt.Errorf("invalid scope: %q (must be host, path, or url)", r.Scope)
	}

	// Validate pattern type
	switch r.Type {
	case PatternTypeExact, PatternTypeGlob, PatternTypeRegex, "":
		// Valid
	default:
		return fmt.Errorf("invalid type: %q (must be exact, glob, or regex)", r.Type)
	}

	// Try to compile the pattern
	patternType := r.DetectPatternType()
	if patternType == PatternTypeRegex {
		if _, err := regexp.Compile(r.Pattern); err != nil {
			return fmt.Errorf("invalid regex pattern: %w", err)
		}
	}

	return nil
}

// DetectPatternType returns the pattern type, defaulting to glob if not specified.
func (r *FilterRule) DetectPatternType() PatternType {
	if r.Type != "" {
		return r.Type
	}

	// Check for regex indicators - if found, use regex
	regexChars := []string{"^", "$", "|", "(", ")", "[", "]", "{", "}", "+", "\\"}
	for _, ch := range regexChars {
		if strings.Contains(r.Pattern, ch) {
			return PatternTypeRegex
		}
	}

	// Default to glob (supports * and ? wildcards, treats plain strings as literals)
	return PatternTypeGlob
}

// GetScope returns the scope with default of host.
func (r *FilterRule) GetScope() FilterScope {
	if r.Scope == "" {
		return FilterScopeHost
	}
	return r.Scope
}

// DefaultFilterConfig returns a disabled filter configuration.
func DefaultFilterConfig() *FilterConfig {
	return &FilterConfig{}
}

// FilterDecision represents the result of evaluating a request against filter rules.
type FilterDecision struct {
	// Action is the determined action (allow, block, ask).
	Action FilterAction

	// Rule is the matched rule, or nil if default action was used.
	Rule *FilterRule

	// Reason is a human-readable explanation of the decision.
	Reason string

	// IsDefault indicates whether the default action was used.
	IsDefault bool
}
