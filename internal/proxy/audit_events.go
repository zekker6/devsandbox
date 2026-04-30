package proxy

import (
	"net/http"
	"net/url"

	"devsandbox/internal/logging"
)

// emitFilterDecision sends a proxy.filter.decision audit event when filter
// rules evaluate a request. Allow decisions are gated behind
// Config.LogFilterDecisions so that the default-on behaviour (only deny/ask
// events) doesn't generate per-request volume in audit logs.
//
// The path field carries only the URL path component — the query string is
// deliberately stripped because it can carry tokens. Use the host + method +
// path as the queryable key.
func (s *Server) emitFilterDecision(req *http.Request, decision FilterDecision) {
	if s == nil || s.dispatcher == nil {
		return
	}

	level := logging.LevelInfo
	switch decision.Action {
	case FilterActionBlock, FilterActionAsk:
		level = logging.LevelWarn
	default:
		// Allow: only emit when the operator explicitly opts in to high-volume
		// audit traces. Skip silently otherwise.
		if !s.config.LogFilterDecisions {
			return
		}
	}

	host := req.URL.Host
	if host == "" {
		host = req.Host
	}

	fields := map[string]any{
		"host":                host,
		"method":              req.Method,
		"path":                pathOnly(req.URL),
		"rule_action":         string(decision.Action),
		"default_action_used": decision.IsDefault,
	}
	if decision.Rule != nil {
		fields["rule_id"] = decision.Rule.Pattern
	}

	_ = s.dispatcher.Event(level, "proxy.filter.decision", fields)
}

// pathOnly returns the URL path component without the query string. URLs with
// a nil receiver return the empty string. Used to ensure audit events never
// carry token-bearing query strings.
func pathOnly(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Path
}

// emitRedactionApplied sends a proxy.redaction.applied event for each match
// in a successful Scan. Only the rule name (`secret_kind`) and location are
// logged — the secret value itself is never included.
func (s *Server) emitRedactionApplied(req *http.Request, result *RedactionResult) {
	if s == nil || s.dispatcher == nil || result == nil || !result.Matched {
		return
	}
	host := req.URL.Host
	if host == "" {
		host = req.Host
	}
	for _, m := range result.Matches {
		_ = s.dispatcher.Event(logging.LevelInfo, "proxy.redaction.applied", map[string]any{
			"host":        host,
			"secret_kind": m.RuleName,
			"location":    m.Location,
			"rule_id":     m.RuleName,
		})
	}
}

// emitCredentialInjected sends a proxy.credential.injected event when a
// credential injector successfully adds an auth header. Includes only the
// injector name and the header name — never the credential value.
func (s *Server) emitCredentialInjected(host, injector, headerName string) {
	if s == nil || s.dispatcher == nil {
		return
	}
	_ = s.dispatcher.Event(logging.LevelInfo, "proxy.credential.injected", map[string]any{
		"host":        host,
		"injector":    injector,
		"header_name": headerName,
	})
}

// emitMITMBypass sends a proxy.mitm.bypass event the first time a CONNECT
// request bypasses MITM in no-MITM mode. Per-host dedupe is the caller's
// responsibility (see Server.bypassedHosts).
func (s *Server) emitMITMBypass(host string) {
	if s == nil || s.dispatcher == nil {
		return
	}
	_ = s.dispatcher.Event(logging.LevelInfo, "proxy.mitm.bypass", map[string]any{
		"host":   host,
		"reason": "global",
	})
}
