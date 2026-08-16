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

// compileScopedPattern compiles a pattern for the given scope. Exact and glob
// patterns are canonicalized on the same axis their match target is - the whole
// pattern for host scope, the authority alone for url scope - so both sides of
// the comparison agree and a rule written "BLOCKED.Example.com." or
// "https://API.Example.com/**" still matches.
//
// A regex cannot be rewritten that way - lowercasing a pattern would corrupt its
// metacharacters. A host-scoped one is compiled case-insensitively instead:
// leaving it verbatim is what a rule author would expect only if the match
// target were verbatim too, and it is not, so `^API\.example\.com$` could never
// match again once canonicalization landed, silently retiring an existing block
// rule on upgrade. Case-insensitive is also what DNS means, and an author who
// genuinely wants case sensitivity can still say so with an inner (?-i).
//
// A url-scoped regex is left verbatim, because the path half of a URL *is*
// case-sensitive and folding the whole pattern would widen it there. Such a
// pattern has to spell its host in lower case with no default port; that is
// documented rather than rewritten.
func compileScopedPattern(pattern string, t PatternType, scope FilterScope) (func(string) bool, error) {
	switch {
	case t == PatternTypeRegex:
		if scope == FilterScopeHost {
			pattern = "(?i)" + pattern
		}
	case scope == FilterScopeHost:
		pattern = canonicalizeHost(pattern)
	case scope == FilterScopeURL:
		pattern = canonicalizeURLPattern(pattern)
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

	// Evaluate rules in order, then let a remembered answer stand in only where
	// a prompt is what would otherwise happen.
	for _, compiled := range e.compiledRules {
		target := e.getMatchTarget(req, compiled.rule.GetScope())
		if compiled.matcher(target) {
			return e.decisionFor(&compiled.rule, RequestHost(req))
		}
	}

	return e.decisionFor(nil, RequestHost(req))
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

	for _, compiled := range e.compiledRules {
		if compiled.rule.GetScope() != FilterScopeHost {
			continue
		}
		if compiled.matcher(host) {
			return e.decisionFor(&compiled.rule, host)
		}
	}

	return e.decisionFor(nil, host)
}

// decisionFor turns the matched rule - or its absence - into a decision,
// substituting a remembered ask-mode answer only where the outcome would
// otherwise be a prompt.
//
// The cache used to be read *before* any rule was evaluated, which made it a
// blanket host-wide override: it is keyed on the host alone and records nothing
// about which rule asked, so approving and remembering one request that matched
// a path-scoped `ask` rule installed an `allow` that short-circuited a later
// host-scoped `block` for the rest of the session, logged only as "cached
// decision". Nothing was wrong with the cache's key until rule-level `ask`
// became reachable - the ask queue is now gated on usesAskAction() rather than
// on the default action alone, which is what puts a narrow rule's answer in
// front of every other rule.
//
// An explicit allow or block therefore wins outright, and "remember" keeps
// meaning what the prompt offered: do not ask me again about this host.
func (e *FilterEngine) decisionFor(rule *FilterRule, host string) FilterDecision {
	if rule != nil && rule.Action != FilterActionAsk {
		return matchedDecision(*rule)
	}
	if e.config.IsCacheEnabled() {
		if decision := e.getCachedDecision(host); decision != "" {
			return FilterDecision{
				Action:    decision,
				IsDefault: false,
				Reason:    "cached decision",
			}
		}
	}
	if rule != nil {
		return matchedDecision(*rule)
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
	case FilterScopePath:
		return req.URL.Path

	case FilterScopeURL:
		return canonicalizeURL(req.URL)

	default:
		return NormalizeHost(RequestHost(req))
	}
}

// RequestHost returns the authority the request will actually be sent to.
//
// req.URL.Host is that authority; req.Host is only the client's Host header. On
// the MITM path goproxy rebuilds req.URL from the CONNECT target and leaves
// req.Host as the tunneled request spelled it, while the transport dials
// req.URL.Host - so a sandbox sending `CONNECT evil.example.com:443` with
// `Host: allowed.example.com` matched every host-scoped rule against the name it
// was not contacting. Everything that decides or records where a request went
// has to read the same field, or the decision describes a different destination
// than the connection.
func RequestHost(req *http.Request) string {
	if req == nil {
		return ""
	}
	if req.URL != nil && req.URL.Host != "" {
		return req.URL.Host
	}
	return req.Host
}

// defaultSchemePorts are the ports a scheme already implies. A URL spelling one
// explicitly names the same resource as one omitting it.
var defaultSchemePorts = map[string]string{"http": "80", "https": "443"}

// canonicalizeURL renders a URL with its authority canonicalized the way
// NormalizeHost canonicalizes a host, and its path left exactly as sent.
//
// url.Parse does not touch the authority's case, so a url-scoped rule matching
// the raw string was evaded by asking for the same resource as
// "https://API.EXAMPLE.COM/x" or "https://api.example.com./x". The path is the
// case-sensitive half of a URL; the host half is not, and treating the whole
// string as case-sensitive let the host half inherit the wrong rule.
//
// The scheme's default port is dropped for the same reason. On the MITM path
// req.URL is always rebuilt from the CONNECT target, which carries a port, so
// every HTTPS request arrived here spelled "https://host:443/..." and no rule
// written the way the docs write them could ever match one.
func canonicalizeURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	c := *u
	c.Scheme = strings.ToLower(u.Scheme)
	c.Host = canonicalizeAuthority(c.Scheme, u.Host)
	return c.String()
}

// canonicalizeAuthority lowercases a "host[:port]" authority, strips a single
// trailing dot from the host, and drops a port the scheme already implies.
func canonicalizeAuthority(scheme, authority string) string {
	host, port, err := net.SplitHostPort(authority)
	if err != nil {
		// No port: strip IPv6 brackets, canonicalize, put them back.
		host = authority
		bracketed := strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]")
		if bracketed {
			host = host[1 : len(host)-1]
		}
		host = canonicalizeHost(host)
		if bracketed || strings.Contains(host, ":") {
			return "[" + host + "]"
		}
		return host
	}

	host = canonicalizeHost(host)
	if port == defaultSchemePorts[scheme] {
		port = ""
	}
	if port == "" {
		if strings.Contains(host, ":") {
			return "[" + host + "]"
		}
		return host
	}
	return net.JoinHostPort(host, port)
}

// canonicalizeURLPattern applies canonicalizeAuthority to a url-scoped pattern's
// authority and leaves the rest of it byte for byte.
//
// It is done lexically rather than with url.Parse because a pattern is not a
// URL: a glob's `?`, `[` and `]` are metacharacters url.Parse would read as a
// query string or a malformed IPv6 literal. Without this the two sides of the
// comparison disagree - the target is canonicalized and the pattern is not - so
// a rule naming "https://API.Example.com/**" silently matched nothing.
func canonicalizeURLPattern(pattern string) string {
	rawScheme, rest, ok := strings.Cut(pattern, "://")
	if !ok {
		return pattern
	}
	scheme := strings.ToLower(rawScheme)
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	return scheme + "://" + canonicalizeAuthority(scheme, rest[:end]) + rest[end:]
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
