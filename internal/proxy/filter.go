package proxy

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/bmatcuk/doublestar/v4"
)

// FilterEngine evaluates HTTP requests against filter rules.
//
// config and compiledRules are written only by NewFilterEngine, before the
// engine is published, and are immutable afterwards - so the readers need no
// lock. A future config-reload path would have to introduce one and cover
// every reader of both fields; there is deliberately no mutex here implying
// that work is already done. cacheMu guards decisionCache, which is mutable.
type FilterEngine struct {
	config        *FilterConfig
	compiledRules []compiledRule

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
	matcher, err := compileScopedPattern(rule.Pattern, rule.DetectPatternType(), rule.GetScope())
	if err != nil {
		return compiledRule{}, err
	}
	return compiledRule{rule: rule, matcher: matcher}, nil
}

// compileScopedPattern compiles a pattern for the given scope. Host-scoped
// exact and glob patterns are canonicalized the same way NormalizeHost
// canonicalizes the request host, so both sides of the comparison agree and a
// rule written "BLOCKED.Example.com." still matches.
//
// A host-scoped regex cannot be rewritten that way - lowercasing a pattern would
// corrupt its metacharacters - so it is compiled case-insensitively instead.
// Leaving it verbatim is what a rule author would expect only if the match
// target were verbatim too, and it is not: NormalizeHost lowercases every host
// before matching, so `^API\.example\.com$` could never match again once
// canonicalization landed, silently retiring an existing block rule on upgrade.
// Case-insensitive is also what DNS means, and an author who genuinely wants
// case sensitivity can still say so with an inner (?-i).
func compileScopedPattern(pattern string, t PatternType, scope FilterScope) (func(string) bool, error) {
	if scope == FilterScopeHost {
		if t == PatternTypeRegex {
			pattern = "(?i)" + pattern
		} else {
			pattern = canonicalizeHost(pattern)
		}
	}
	return compilePattern(pattern, t)
}

// compilePattern returns a matcher function for the given pattern and type.
// Shared by FilterEngine and LogSkipEngine to avoid duplicating the
// exact/glob/regex compilation switch.
func compilePattern(pattern string, t PatternType) (func(string) bool, error) {
	switch t {
	case PatternTypeExact:
		return func(s string) bool {
			return s == pattern
		}, nil

	case PatternTypeGlob:
		// Use doublestar for glob matching (supports *, **, ?)
		if !doublestar.ValidatePattern(pattern) {
			return nil, fmt.Errorf("invalid glob pattern: %s", pattern)
		}
		return func(s string) bool {
			matched, _ := doublestar.Match(pattern, s)
			return matched
		}, nil

	case PatternTypeRegex:
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid regex pattern: %w", err)
		}
		return func(s string) bool {
			return re.MatchString(s)
		}, nil

	default:
		return nil, fmt.Errorf("unknown pattern type: %s", t)
	}
}

// Match evaluates the request against filter rules and returns a decision.
func (e *FilterEngine) Match(req *http.Request) FilterDecision {
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
			return matchedDecision(compiled.rule)
		}
	}

	return e.defaultDecision()
}

// MatchHost evaluates a CONNECT target ("example.com:443") against the
// host-scoped rules and returns a decision. It exists because a CONNECT
// carries no path or URL, so Match cannot be used: there is no request to
// build a path or url match target from.
//
// Rules of any other scope are skipped rather than guessed at. That cannot
// silently enforce less than the configuration promises, because NewServer
// refuses to start with a path- or url-scoped rule while MITM is off - the
// only configuration in which this method is reached.
func (e *FilterEngine) MatchHost(hostport string) FilterDecision {
	if !e.config.IsEnabled() {
		return FilterDecision{
			Action:    FilterActionAllow,
			IsDefault: true,
			Reason:    "filtering disabled",
		}
	}

	host := NormalizeHost(hostport)

	if e.config.IsCacheEnabled() {
		if decision := e.getCachedDecision(host); decision != "" {
			return FilterDecision{
				Action:    decision,
				IsDefault: false,
				Reason:    "cached decision",
			}
		}
	}

	for _, compiled := range e.compiledRules {
		if compiled.rule.GetScope() != FilterScopeHost {
			continue
		}
		if compiled.matcher(host) {
			return matchedDecision(compiled.rule)
		}
	}

	return e.defaultDecision()
}

// matchedDecision builds the decision for a rule that matched.
func matchedDecision(rule FilterRule) FilterDecision {
	reason := rule.Reason
	if reason == "" {
		reason = fmt.Sprintf("matched rule: %s", rule.Pattern)
	}
	return FilterDecision{
		Action:    rule.Action,
		Rule:      &rule,
		Reason:    reason,
		IsDefault: false,
	}
}

// defaultDecision builds the decision used when no rule matched.
func (e *FilterEngine) defaultDecision() FilterDecision {
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
		return NormalizeHost(req.Host)

	case FilterScopePath:
		return req.URL.Path

	case FilterScopeURL:
		return canonicalizeURL(req.URL)

	default:
		return NormalizeHost(req.Host)
	}
}

// canonicalizeURL renders a URL with its authority canonicalized the way
// NormalizeHost canonicalizes a host, and its path left exactly as sent.
//
// url.Parse does not touch the authority's case, so a url-scoped rule matching
// the raw string was evaded by asking for the same resource as
// "https://API.EXAMPLE.COM/x" or "https://api.example.com./x". The path is the
// case-sensitive half of a URL; the host half is not, and treating the whole
// string as case-sensitive let the host half inherit the wrong rule.
func canonicalizeURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	host := u.Hostname()
	canon := canonicalizeHost(host)
	if canon == host {
		return u.String()
	}

	c := *u
	switch port := u.Port(); {
	case port != "":
		c.Host = net.JoinHostPort(canon, port)
	case strings.Contains(canon, ":"):
		// Bare IPv6 literal: Hostname() dropped the brackets the authority needs.
		c.Host = "[" + canon + "]"
	default:
		c.Host = canon
	}
	return c.String()
}

// NormalizeHost extracts the hostname without port, handling IPv6 addresses
// correctly, and canonicalizes it: DNS names are case-insensitive and a single
// trailing dot is the fully-qualified spelling of the same name, so
// "BLOCKED.Example.com.:443" and "blocked.example.com" both reduce to
// "blocked.example.com". Without this, three spellings that reach the same
// server would each be matched and cached separately.
func NormalizeHost(hostport string) string {
	// Use net.SplitHostPort for robust parsing
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		// No port present, use as-is (but strip brackets from IPv6 if present)
		host = hostport
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = host[1 : len(host)-1]
		}
	}
	return canonicalizeHost(host)
}

// canonicalizeHost lowercases a hostname and strips a single trailing dot. The
// bare root label "." is left alone: reducing it to the empty string would make
// it collide with a missing host.
func canonicalizeHost(host string) string {
	host = strings.ToLower(host)
	if len(host) > 1 {
		host = strings.TrimSuffix(host, ".")
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
	e.decisionCache[NormalizeHost(host)] = action
}

// getCachedDecision retrieves a cached decision for a host.
// The host is normalized (port removed) to ensure consistent cache keys.
func (e *FilterEngine) getCachedDecision(host string) FilterAction {
	e.cacheMu.RLock()
	defer e.cacheMu.RUnlock()
	return e.decisionCache[NormalizeHost(host)]
}

// ClearCache clears all cached decisions.
func (e *FilterEngine) ClearCache() {
	e.cacheMu.Lock()
	defer e.cacheMu.Unlock()
	e.decisionCache = make(map[string]FilterAction)
}

// IsEnabled returns true if filtering is active.
func (e *FilterEngine) IsEnabled() bool {
	return e.config.IsEnabled()
}

// Config returns the filter configuration.
func (e *FilterEngine) Config() *FilterConfig {
	return e.config
}

// BlockResponse creates an HTTP 403 response for blocked requests.
func BlockResponse(req *http.Request, reason string) *http.Response {
	body := fmt.Sprintf("Request blocked by devsandbox: %s\n", reason)

	return &http.Response{
		StatusCode: http.StatusForbidden,
		Status:     "403 Forbidden",
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header: http.Header{
			"Content-Type":   []string{"text/plain; charset=utf-8"},
			"Content-Length": []string{fmt.Sprintf("%d", len(body))},
			"X-Blocked-By":   []string{"devsandbox"},
		},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}
