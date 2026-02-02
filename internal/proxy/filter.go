package proxy

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/bmatcuk/doublestar/v4"
)

// FilterEngine evaluates HTTP requests against filter rules.
type FilterEngine struct {
	config        *FilterConfig
	compiledRules []compiledRule
	mu            sync.RWMutex

	// Decision cache for ask mode (host -> action)
	decisionCache map[string]FilterAction
	cacheMu       sync.RWMutex
}

// compiledRule is a filter rule with a pre-compiled matcher.
type compiledRule struct {
	rule    FilterRule
	matcher func(string) bool
}

// NewFilterEngine creates a new filter engine with the given configuration.
func NewFilterEngine(cfg *FilterConfig) (*FilterEngine, error) {
	if cfg == nil {
		cfg = DefaultFilterConfig()
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid filter config: %w", err)
	}

	engine := &FilterEngine{
		config:        cfg,
		decisionCache: make(map[string]FilterAction),
	}

	// Compile all rules
	for _, rule := range cfg.Rules {
		compiled, err := compileRule(rule)
		if err != nil {
			return nil, fmt.Errorf("failed to compile rule %q: %w", rule.Pattern, err)
		}
		engine.compiledRules = append(engine.compiledRules, compiled)
	}

	return engine, nil
}

// compileRule creates a compiled rule with a pre-built matcher function.
func compileRule(rule FilterRule) (compiledRule, error) {
	patternType := rule.DetectPatternType()

	var matcher func(string) bool

	switch patternType {
	case PatternTypeExact:
		pattern := rule.Pattern
		matcher = func(s string) bool {
			return s == pattern
		}

	case PatternTypeGlob:
		// Use doublestar for glob matching (supports *, **, ?)
		pattern := rule.Pattern
		// Validate pattern at compile time
		if !doublestar.ValidatePattern(pattern) {
			return compiledRule{}, fmt.Errorf("invalid glob pattern: %s", pattern)
		}
		matcher = func(s string) bool {
			matched, _ := doublestar.Match(pattern, s)
			return matched
		}

	case PatternTypeRegex:
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return compiledRule{}, fmt.Errorf("invalid regex pattern: %w", err)
		}
		matcher = func(s string) bool {
			return re.MatchString(s)
		}

	default:
		return compiledRule{}, fmt.Errorf("unknown pattern type: %s", patternType)
	}

	return compiledRule{
		rule:    rule,
		matcher: matcher,
	}, nil
}

// Match evaluates the request against filter rules and returns a decision.
func (e *FilterEngine) Match(req *http.Request) FilterDecision {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// If filtering is disabled, always allow
	if !e.config.IsEnabled() {
		return FilterDecision{
			Action:    FilterActionAllow,
			IsDefault: true,
			Reason:    "filtering disabled",
		}
	}

	// Check decision cache
	if e.config.IsCacheEnabled() {
		if decision := e.getCachedDecision(req.Host); decision != "" {
			return FilterDecision{
				Action:    decision,
				IsDefault: false,
				Reason:    "cached decision",
			}
		}
	}

	// Evaluate rules in order
	for _, compiled := range e.compiledRules {
		target := e.getMatchTarget(req, compiled.rule.GetScope())
		if compiled.matcher(target) {
			reason := compiled.rule.Reason
			if reason == "" {
				reason = fmt.Sprintf("matched rule: %s", compiled.rule.Pattern)
			}
			return FilterDecision{
				Action:    compiled.rule.Action,
				Rule:      &compiled.rule,
				Reason:    reason,
				IsDefault: false,
			}
		}
	}

	// No rule matched, use default action
	defaultAction := e.config.GetDefaultAction()
	return FilterDecision{
		Action:    defaultAction,
		IsDefault: true,
		Reason:    fmt.Sprintf("no rule matched, using default action: %s", defaultAction),
	}
}

// getMatchTarget extracts the appropriate string to match based on scope.
func (e *FilterEngine) getMatchTarget(req *http.Request, scope FilterScope) string {
	switch scope {
	case FilterScopeHost:
		return normalizeHost(req.Host)

	case FilterScopePath:
		return req.URL.Path

	case FilterScopeURL:
		return req.URL.String()

	default:
		return normalizeHost(req.Host)
	}
}

// normalizeHost extracts the hostname without port, handling IPv6 addresses correctly.
func normalizeHost(hostport string) string {
	// Use net.SplitHostPort for robust parsing
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		// No port present, return as-is (but strip brackets from IPv6 if present)
		if strings.HasPrefix(hostport, "[") && strings.HasSuffix(hostport, "]") {
			return hostport[1 : len(hostport)-1]
		}
		return hostport
	}
	return host
}

// CacheDecision stores a decision for future requests to the same host.
// The host is normalized (port removed) to ensure consistent cache keys.
func (e *FilterEngine) CacheDecision(host string, action FilterAction) {
	if !e.config.IsCacheEnabled() {
		return
	}

	e.cacheMu.Lock()
	defer e.cacheMu.Unlock()
	e.decisionCache[normalizeHost(host)] = action
}

// getCachedDecision retrieves a cached decision for a host.
// The host is normalized (port removed) to ensure consistent cache keys.
func (e *FilterEngine) getCachedDecision(host string) FilterAction {
	e.cacheMu.RLock()
	defer e.cacheMu.RUnlock()
	return e.decisionCache[normalizeHost(host)]
}

// ClearCache clears all cached decisions.
func (e *FilterEngine) ClearCache() {
	e.cacheMu.Lock()
	defer e.cacheMu.Unlock()
	e.decisionCache = make(map[string]FilterAction)
}

// IsEnabled returns true if filtering is active.
func (e *FilterEngine) IsEnabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.config.IsEnabled()
}

// Config returns the filter configuration.
func (e *FilterEngine) Config() *FilterConfig {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.config
}

// BlockResponse creates an HTTP 403 response for blocked requests.
func BlockResponse(req *http.Request, reason string) *http.Response {
	body := fmt.Sprintf("Request blocked by devsandbox filter: %s\n", reason)

	return &http.Response{
		StatusCode: http.StatusForbidden,
		Status:     "403 Forbidden",
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header: http.Header{
			"Content-Type":   []string{"text/plain; charset=utf-8"},
			"Content-Length": []string{fmt.Sprintf("%d", len(body))},
			"X-Blocked-By":   []string{"devsandbox-filter"},
		},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}
