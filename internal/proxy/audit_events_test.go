package proxy

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"devsandbox/internal/logging"
)

// auditMemWriter captures dispatched entries for audit-event assertions.
type auditMemWriter struct {
	mu      sync.Mutex
	entries []logging.Entry
}

func (m *auditMemWriter) Write(entry *logging.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *entry
	if entry.Fields != nil {
		cp.Fields = make(map[string]any, len(entry.Fields))
		for k, v := range entry.Fields {
			cp.Fields[k] = v
		}
	}
	m.entries = append(m.entries, cp)
	return nil
}

func (m *auditMemWriter) Close() error { return nil }

func (m *auditMemWriter) snapshot() []logging.Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]logging.Entry, len(m.entries))
	copy(out, m.entries)
	return out
}

func newServerWithAuditWriter(t *testing.T, logFilterDecisions bool) (*Server, *auditMemWriter) {
	t.Helper()
	d := logging.NewDispatcher()
	mw := &auditMemWriter{}
	d.AddWriter(mw)

	s := &Server{
		config:     &Config{LogFilterDecisions: logFilterDecisions},
		dispatcher: d,
	}
	return s, mw
}

func reqFor(t *testing.T, method, rawURL string) *http.Request {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return &http.Request{
		Method: method,
		URL:    u,
		Host:   u.Host,
	}
}

func TestEmitFilterDecision_DenyAlwaysEmits(t *testing.T) {
	s, mw := newServerWithAuditWriter(t, false)
	req := reqFor(t, "GET", "https://api.example.com/v1/data?token=secret")

	rule := &FilterRule{Pattern: "api.example.com", Action: FilterActionBlock}
	dec := FilterDecision{Action: FilterActionBlock, Rule: rule, Reason: "block rule"}
	s.emitFilterDecision(req, dec)

	got := mw.snapshot()
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	e := got[0]
	if e.Fields["event"] != "proxy.filter.decision" {
		t.Errorf("event = %v, want proxy.filter.decision", e.Fields["event"])
	}
	if e.Level != logging.LevelWarn {
		t.Errorf("level = %v, want warn for deny", e.Level)
	}
	if e.Fields["host"] != "api.example.com" {
		t.Errorf("host = %v, want api.example.com", e.Fields["host"])
	}
	if e.Fields["method"] != "GET" {
		t.Errorf("method = %v, want GET", e.Fields["method"])
	}
	if e.Fields["rule_action"] != "block" {
		t.Errorf("rule_action = %v, want block", e.Fields["rule_action"])
	}
	if e.Fields["rule_id"] != "api.example.com" {
		t.Errorf("rule_id = %v, want api.example.com", e.Fields["rule_id"])
	}
}

func TestEmitFilterDecision_AllowGatedByFlag(t *testing.T) {
	// Flag off → no event for allow.
	s, mw := newServerWithAuditWriter(t, false)
	req := reqFor(t, "GET", "https://api.example.com/v1/data")
	dec := FilterDecision{Action: FilterActionAllow, Reason: "matched allow rule",
		Rule: &FilterRule{Pattern: "api.example.com", Action: FilterActionAllow}}

	s.emitFilterDecision(req, dec)
	if got := mw.snapshot(); len(got) != 0 {
		t.Errorf("flag off allow → %d entries, want 0", len(got))
	}

	// Flag on → event emitted at info level.
	s2, mw2 := newServerWithAuditWriter(t, true)
	s2.emitFilterDecision(req, dec)
	got := mw2.snapshot()
	if len(got) != 1 {
		t.Fatalf("flag on allow → %d entries, want 1", len(got))
	}
	if got[0].Level != logging.LevelInfo {
		t.Errorf("level = %v, want info for allow", got[0].Level)
	}
}

func TestEmitFilterDecision_PathHasNoQueryString(t *testing.T) {
	s, mw := newServerWithAuditWriter(t, false)
	req := reqFor(t, "GET", "https://api.example.com/v1/data?token=sk-test-leakcanary&user=bob")
	dec := FilterDecision{Action: FilterActionBlock, Rule: &FilterRule{Pattern: "x"}}

	s.emitFilterDecision(req, dec)

	got := mw.snapshot()
	path, _ := got[0].Fields["path"].(string)
	if path != "/v1/data" {
		t.Errorf("path = %q, want /v1/data (no query)", path)
	}
	if strings.Contains(path, "?") || strings.Contains(path, "token") || strings.Contains(path, "leakcanary") {
		t.Errorf("path leaked query: %q", path)
	}
	// Negative assertion: no field across the whole entry should leak the secret.
	for k, v := range got[0].Fields {
		if s, ok := v.(string); ok && strings.Contains(s, "leakcanary") {
			t.Errorf("field %q leaked secret token: %q", k, s)
		}
	}
}

func TestEmitFilterDecision_DefaultActionFlagged(t *testing.T) {
	s, mw := newServerWithAuditWriter(t, false)
	req := reqFor(t, "GET", "https://x.example/")
	dec := FilterDecision{
		Action:    FilterActionBlock,
		IsDefault: true,
		Reason:    "no rule matched",
	}

	s.emitFilterDecision(req, dec)

	got := mw.snapshot()
	if got[0].Fields["default_action_used"] != true {
		t.Errorf("default_action_used = %v, want true", got[0].Fields["default_action_used"])
	}
	if _, ok := got[0].Fields["rule_id"]; ok {
		t.Errorf("rule_id should be omitted when no rule matched: %v", got[0].Fields)
	}
}

func TestEmitFilterDecision_NilDispatcherIsNoop(t *testing.T) {
	s := &Server{config: &Config{}}
	req := reqFor(t, "GET", "https://x/")
	// Must not panic.
	s.emitFilterDecision(req, FilterDecision{Action: FilterActionBlock})
}

func TestEmitRedactionApplied_OneEventPerMatch(t *testing.T) {
	s, mw := newServerWithAuditWriter(t, false)
	req := reqFor(t, "POST", "https://api.example.com/secrets")

	const secret = "sk-test-leakcanary"
	result := &RedactionResult{
		Matched: true,
		Matches: []RedactionMatch{
			{RuleName: "openai_key", Location: "body", Action: RedactionActionRedact},
			{RuleName: "openai_key", Location: "header:Authorization", Action: RedactionActionRedact},
		},
	}

	s.emitRedactionApplied(req, result)

	got := mw.snapshot()
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2 (one per match)", len(got))
	}
	for _, e := range got {
		if e.Fields["event"] != "proxy.redaction.applied" {
			t.Errorf("event = %v, want proxy.redaction.applied", e.Fields["event"])
		}
		if e.Fields["secret_kind"] != "openai_key" {
			t.Errorf("secret_kind = %v, want openai_key", e.Fields["secret_kind"])
		}
		if e.Fields["host"] != "api.example.com" {
			t.Errorf("host = %v, want api.example.com", e.Fields["host"])
		}
		// Negative: no field anywhere should contain the secret string.
		for k, v := range e.Fields {
			if str, ok := v.(string); ok && strings.Contains(str, secret) {
				t.Errorf("field %q leaked secret: %q", k, str)
			}
		}
	}
}

func TestEmitRedactionApplied_NoMatchedNoEvent(t *testing.T) {
	s, mw := newServerWithAuditWriter(t, false)
	req := reqFor(t, "GET", "https://x/")
	s.emitRedactionApplied(req, &RedactionResult{Matched: false})
	if got := mw.snapshot(); len(got) != 0 {
		t.Errorf("got %d entries, want 0 (Matched=false)", len(got))
	}
}

func TestEmitRedactionApplied_NilDispatcherIsNoop(t *testing.T) {
	s := &Server{config: &Config{}}
	s.emitRedactionApplied(reqFor(t, "GET", "https://x/"), &RedactionResult{Matched: true})
}

func TestEmitCredentialInjected_PayloadShape(t *testing.T) {
	s, mw := newServerWithAuditWriter(t, false)
	const secret = "ghp_test_leakcanary_token"

	s.emitCredentialInjected("api.github.com", "github", "Authorization")

	got := mw.snapshot()
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	e := got[0]
	if e.Fields["event"] != "proxy.credential.injected" {
		t.Errorf("event = %v, want proxy.credential.injected", e.Fields["event"])
	}
	if e.Fields["host"] != "api.github.com" {
		t.Errorf("host = %v, want api.github.com", e.Fields["host"])
	}
	if e.Fields["injector"] != "github" {
		t.Errorf("injector = %v, want github", e.Fields["injector"])
	}
	if e.Fields["header_name"] != "Authorization" {
		t.Errorf("header_name = %v, want Authorization", e.Fields["header_name"])
	}
	// Negative: even though we never pass the secret to emitCredentialInjected,
	// pin the contract via search.
	for k, v := range e.Fields {
		if str, ok := v.(string); ok && strings.Contains(str, secret) {
			t.Errorf("field %q leaked secret: %q", k, str)
		}
	}
}

func TestEmitCredentialInjected_NilDispatcherIsNoop(t *testing.T) {
	s := &Server{config: &Config{}}
	s.emitCredentialInjected("h", "i", "H")
}

func TestEmitMITMBypass_PayloadShape(t *testing.T) {
	s, mw := newServerWithAuditWriter(t, false)
	s.emitMITMBypass("api.example.com")

	got := mw.snapshot()
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	e := got[0]
	if e.Fields["event"] != "proxy.mitm.bypass" {
		t.Errorf("event = %v, want proxy.mitm.bypass", e.Fields["event"])
	}
	if e.Fields["host"] != "api.example.com" {
		t.Errorf("host = %v, want api.example.com", e.Fields["host"])
	}
	if e.Fields["reason"] != "global" {
		t.Errorf("reason = %v, want global", e.Fields["reason"])
	}
}

func TestMITMBypass_DedupePerHost(t *testing.T) {
	s, mw := newServerWithAuditWriter(t, false)

	// Simulate the dedupe-and-emit pattern from setupMITM's no-MITM branch.
	tryEmit := func(host string) {
		if _, loaded := s.bypassedHosts.LoadOrStore(host, struct{}{}); !loaded {
			s.emitMITMBypass(host)
		}
	}

	tryEmit("api.github.com")
	tryEmit("api.github.com") // duplicate — should not emit
	tryEmit("api.openai.com") // distinct — should emit
	tryEmit("api.github.com") // duplicate again — still no emit

	got := mw.snapshot()
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2 (one per distinct host)", len(got))
	}
	hosts := []string{
		got[0].Fields["host"].(string),
		got[1].Fields["host"].(string),
	}
	want := map[string]bool{"api.github.com": true, "api.openai.com": true}
	for _, h := range hosts {
		if !want[h] {
			t.Errorf("unexpected host in events: %q", h)
		}
	}
}

func TestRequestLogger_RequestCount(t *testing.T) {
	dir := t.TempDir()
	rl, err := NewRequestLogger(dir, nil, false, nil)
	if err != nil {
		t.Fatalf("NewRequestLogger: %v", err)
	}
	defer func() { _ = rl.Close() }()

	if got := rl.RequestCount(); got != 0 {
		t.Errorf("initial count = %d, want 0", got)
	}

	for range 5 {
		_ = rl.Log(&RequestLog{Method: "GET", URL: "http://x"})
	}

	if got := rl.RequestCount(); got != 5 {
		t.Errorf("count after 5 Log calls = %d, want 5", got)
	}
}
