package proxy

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	"devsandbox/internal/sandbox"
)

// RedactionEngine scans outgoing requests for secrets.
type RedactionEngine struct {
	config        *RedactionConfig
	compiledRules []compiledRedactionRule
}

type compiledRedactionRule struct {
	rule          RedactionRule
	name          string
	action        RedactionAction
	resolvedValue string         // for source-based rules
	compiledRegex *regexp.Regexp // for pattern-based rules
}

// NewRedactionEngine creates a new redaction engine.
// projectDir is used to resolve env_file_key sources from .env files.
// Rules with unresolvable sources cause a startup error (fail closed).
func NewRedactionEngine(cfg *RedactionConfig, projectDir string) (*RedactionEngine, error) {
	if cfg == nil {
		cfg = &RedactionConfig{}
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid redaction config: %w", err)
	}

	engine := &RedactionEngine{config: cfg}

	// Pre-resolve env file values if any rule uses env_file_key
	envFileValues := resolveEnvFileValues(cfg.Rules, projectDir)

	for i, rule := range cfg.Rules {
		name := rule.GetName(i)
		action := rule.GetAction(cfg.GetDefaultAction())

		if rule.Pattern != "" {
			re, err := regexp.Compile(rule.Pattern)
			if err != nil {
				return nil, fmt.Errorf("rule %q: invalid regex: %w", name, err)
			}
			engine.compiledRules = append(engine.compiledRules, compiledRedactionRule{
				rule:          rule,
				name:          name,
				action:        action,
				compiledRegex: re,
			})
			continue
		}

		// Source-based rule: resolve value
		resolved := resolveSource(rule.Source, envFileValues)
		if resolved == "" {
			return nil, fmt.Errorf("redaction rule %q: could not resolve secret value from source", name)
		}

		engine.compiledRules = append(engine.compiledRules, compiledRedactionRule{
			rule:          rule,
			name:          name,
			action:        action,
			resolvedValue: resolved,
		})
	}

	return engine, nil
}

// IsEnabled returns true if the redaction engine is active.
func (e *RedactionEngine) IsEnabled() bool {
	return e.config.IsEnabled() && len(e.compiledRules) > 0
}

// MatchesValue checks if any compiled redaction rule would match the given value.
// For source-based rules: checks if the value contains the rule's resolved secret,
// which means the redaction rule's matchTarget would fire on this value in a request.
// For pattern-based rules: tests the compiled regex against the value.
// Returns a list of matching rule names. Empty slice means no conflict.
func (e *RedactionEngine) MatchesValue(value string) []string {
	if value == "" {
		return nil
	}

	var names []string
	for _, cr := range e.compiledRules {
		if cr.compiledRegex != nil {
			if cr.compiledRegex.MatchString(value) {
				names = append(names, cr.name)
			}
		} else if cr.resolvedValue != "" {
			if strings.Contains(value, cr.resolvedValue) {
				names = append(names, cr.name)
			}
		}
	}
	return names
}

// Scan checks a request for secrets and returns the result.
func (e *RedactionEngine) Scan(req *http.Request, body []byte) *RedactionResult {
	result := &RedactionResult{}
	urlStr := req.URL.String()

	// Defer string(body) until needed — avoids allocation when body is empty
	var bodyStr string
	if len(body) > 0 {
		bodyStr = string(body)
	}

	for _, cr := range e.compiledRules {
		// Scan URL
		if e.matchTarget(cr, urlStr) {
			result.Matches = append(result.Matches, RedactionMatch{
				RuleName: cr.name,
				Location: "url",
				Action:   cr.action,
			})
		}

		// Scan headers
		for headerName, values := range req.Header {
			for _, v := range values {
				if e.matchTarget(cr, v) {
					result.Matches = append(result.Matches, RedactionMatch{
						RuleName: cr.name,
						Location: fmt.Sprintf("header:%s", headerName),
						Action:   cr.action,
					})
					break // one match per header name is enough
				}
			}
		}

		// Scan body
		if bodyStr != "" && e.matchTarget(cr, bodyStr) {
			result.Matches = append(result.Matches, RedactionMatch{
				RuleName: cr.name,
				Location: "body",
				Action:   cr.action,
			})
		}
	}

	if len(result.Matches) == 0 {
		return result
	}

	result.Matched = true
	result.Action = highestSeverityAction(result.Matches)

	// Build redacted content for redact and block actions.
	// Block also needs redacted values to prevent secret leakage in logs.
	// When the overall action is block or redact, ALL matched secrets are redacted —
	// including those from log-only rules — because the log entry must not contain
	// any detected secret regardless of per-rule action.
	if result.Action == RedactionActionRedact || result.Action == RedactionActionBlock {
		var redactIndices []int
		for _, m := range result.Matches {
			for i, cr := range e.compiledRules {
				if cr.name == m.RuleName {
					redactIndices = append(redactIndices, i)
					break
				}
			}
		}
		result.URL = e.redactStringFiltered(urlStr, redactIndices)
		result.Headers = e.redactHeadersFiltered(req.Header, redactIndices)
		result.Body = []byte(e.redactStringFiltered(bodyStr, redactIndices))
	}

	return result
}

// matchTarget checks if a compiled rule matches the given string.
func (e *RedactionEngine) matchTarget(cr compiledRedactionRule, target string) bool {
	if cr.compiledRegex != nil {
		return cr.compiledRegex.MatchString(target)
	}
	return strings.Contains(target, cr.resolvedValue)
}

// redactStringFiltered replaces secret values only for rules at the given indices.
func (e *RedactionEngine) redactStringFiltered(s string, indices []int) string {
	for _, i := range indices {
		cr := e.compiledRules[i]
		placeholder := fmt.Sprintf("[REDACTED:%s]", cr.name)
		if cr.compiledRegex != nil {
			s = cr.compiledRegex.ReplaceAllString(s, placeholder)
		} else if cr.resolvedValue != "" {
			s = strings.ReplaceAll(s, cr.resolvedValue, placeholder)
		}
	}
	return s
}

// redactHeadersFiltered creates a copy of headers with secret values replaced for given rule indices.
func (e *RedactionEngine) redactHeadersFiltered(headers http.Header, indices []int) map[string][]string {
	if headers == nil {
		return nil
	}
	redacted := make(map[string][]string, len(headers))
	for k, vals := range headers {
		newVals := make([]string, len(vals))
		for i, v := range vals {
			newVals[i] = e.redactStringFiltered(v, indices)
		}
		redacted[k] = newVals
	}
	return redacted
}

// actionSeverity maps redaction actions to their severity level.
// Package-level to avoid per-call allocation.
var actionSeverity = map[RedactionAction]int{
	RedactionActionLog:    0,
	RedactionActionRedact: 1,
	RedactionActionBlock:  2,
}

// highestSeverityAction returns the most severe action from matches.
// Precedence: block > redact > log.
func highestSeverityAction(matches []RedactionMatch) RedactionAction {
	highest := RedactionActionLog
	for _, m := range matches {
		if actionSeverity[m.Action] > actionSeverity[highest] {
			highest = m.Action
		}
	}
	return highest
}

// resolveSource resolves a RedactionSource to a string value.
// Priority: value > env > file > env_file_key.
func resolveSource(src *RedactionSource, envFileValues map[string]string) string {
	if src == nil {
		return ""
	}
	if src.Value != "" {
		return src.Value
	}
	if src.Env != "" {
		return os.Getenv(src.Env)
	}
	if src.File != "" {
		path := expandHome(src.File)
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to read redaction secret file %q: %v\n", src.File, err)
			return ""
		}
		return strings.TrimSpace(string(data))
	}
	if src.EnvFileKey != "" {
		return envFileValues[src.EnvFileKey]
	}
	return ""
}

// resolveEnvFileValues finds .env files in projectDir and extracts all key=value pairs.
func resolveEnvFileValues(rules []RedactionRule, projectDir string) map[string]string {
	values := make(map[string]string)
	if projectDir == "" {
		return values
	}

	// Check if any rule uses env_file_key
	needsEnvFile := false
	for _, r := range rules {
		if r.Source != nil && r.Source.EnvFileKey != "" {
			needsEnvFile = true
			break
		}
	}
	if !needsEnvFile {
		return values
	}

	envFiles := sandbox.FindEnvFiles(projectDir, 3)
	for _, path := range envFiles {
		parseEnvFile(path, values)
	}
	return values
}

// parseEnvFile reads a .env file and adds key=value pairs to the map.
func parseEnvFile(path string, values map[string]string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		// Strip surrounding quotes if present
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		values[key] = value
	}
}
