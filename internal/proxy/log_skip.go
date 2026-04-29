package proxy

import (
	"fmt"
	"net/url"
	"regexp"
)

// LogSkipRule defines a single log-skip rule. Matching requests are dropped
// from the request log entirely (neither written to the local jsonl file nor
// forwarded to remote dispatchers). Unlike FilterRule, there is no Action —
// matching is the action.
type LogSkipRule struct {
	// Pattern is the pattern to match against (exact, glob, or regex).
	Pattern string `toml:"pattern"`

	// Scope defines what part of the request to match.
	// Default: host
	Scope FilterScope `toml:"scope"`

	// Type specifies the pattern matching type.
	// Default: glob. Auto-detected as regex if pattern contains ^$|()[]{}\+
	Type PatternType `toml:"type"`
}

// LogSkipConfig holds the complete log-skip configuration.
// Skipping is active when Rules is non-empty.
type LogSkipConfig struct {
	// Rules is the list of skip rules; first match wins.
	Rules []LogSkipRule `toml:"rules"`
}

// IsEnabled returns true if any skip rules are configured.
func (c *LogSkipConfig) IsEnabled() bool {
	return c != nil && len(c.Rules) > 0
}

// Validate checks the log-skip configuration for errors.
func (c *LogSkipConfig) Validate() error {
	if c == nil {
		return nil
	}
	for i, rule := range c.Rules {
		if err := rule.Validate(); err != nil {
			return fmt.Errorf("rule %d: %w", i+1, err)
		}
	}
	return nil
}

// Validate checks a log-skip rule for errors.
func (r *LogSkipRule) Validate() error {
	if r.Pattern == "" {
		return fmt.Errorf("pattern is required")
	}

	switch r.Scope {
	case FilterScopeHost, FilterScopePath, FilterScopeURL, "":
		// Valid
	default:
		return fmt.Errorf("invalid scope: %q (must be host, path, or url)", r.Scope)
	}

	switch r.Type {
	case PatternTypeExact, PatternTypeGlob, PatternTypeRegex, "":
		// Valid
	default:
		return fmt.Errorf("invalid type: %q (must be exact, glob, or regex)", r.Type)
	}

	if r.DetectPatternType() == PatternTypeRegex {
		if _, err := regexp.Compile(r.Pattern); err != nil {
			return fmt.Errorf("invalid regex pattern: %w", err)
		}
	}

	return nil
}

// GetScope returns the scope with default of host.
func (r *LogSkipRule) GetScope() FilterScope {
	if r.Scope == "" {
		return FilterScopeHost
	}
	return r.Scope
}

// DetectPatternType returns the pattern type, defaulting to glob if not
// specified, and auto-detecting regex when shell metacharacters are present.
func (r *LogSkipRule) DetectPatternType() PatternType {
	if r.Type != "" {
		return r.Type
	}
	// Reuse the same heuristic as FilterRule: presence of regex metacharacters
	// promotes the pattern to regex; otherwise glob.
	tmp := FilterRule{Pattern: r.Pattern}
	return tmp.DetectPatternType()
}

// LogSkipEngine evaluates RequestLog entries against skip rules.
type LogSkipEngine struct {
	rules []compiledLogSkipRule
}

// compiledLogSkipRule pairs a rule with its pre-compiled matcher.
type compiledLogSkipRule struct {
	rule    LogSkipRule
	matcher func(string) bool
}

// NewLogSkipEngine creates a new engine with rules pre-compiled. A nil or
// empty config returns a valid engine that skips nothing.
func NewLogSkipEngine(cfg *LogSkipConfig) (*LogSkipEngine, error) {
	if cfg == nil || len(cfg.Rules) == 0 {
		return &LogSkipEngine{}, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid log_skip config: %w", err)
	}
	e := &LogSkipEngine{}
	for i, r := range cfg.Rules {
		matcher, err := compilePattern(r.Pattern, r.DetectPatternType())
		if err != nil {
			return nil, fmt.Errorf("log_skip rule %d (%q): %w", i+1, r.Pattern, err)
		}
		e.rules = append(e.rules, compiledLogSkipRule{rule: r, matcher: matcher})
	}
	return e, nil
}

// ShouldSkip returns true if any rule matches the entry. Empty engine and
// malformed entry URLs both return false (matched-nothing → log normally).
func (e *LogSkipEngine) ShouldSkip(entry *RequestLog) bool {
	if e == nil || len(e.rules) == 0 || entry == nil {
		return false
	}
	u, err := url.Parse(entry.URL)
	if err != nil {
		return false
	}
	host := NormalizeHost(u.Host)
	path := u.Path
	full := entry.URL
	for _, r := range e.rules {
		var target string
		switch r.rule.GetScope() {
		case FilterScopePath:
			target = path
		case FilterScopeURL:
			target = full
		default:
			target = host
		}
		if r.matcher(target) {
			return true
		}
	}
	return false
}
