package proxy

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactionEngine_SourceResolution_Value(t *testing.T) {
	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "literal", Source: &RedactionSource{Value: "super-secret-123"}},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}
	if len(engine.compiledRules) != 1 {
		t.Fatalf("expected 1 compiled rule, got %d", len(engine.compiledRules))
	}
	if engine.compiledRules[0].resolvedValue != "super-secret-123" {
		t.Errorf("resolved value = %q, want %q", engine.compiledRules[0].resolvedValue, "super-secret-123")
	}
}

func TestRedactionEngine_SourceResolution_Env(t *testing.T) {
	t.Setenv("TEST_REDACT_SECRET", "env-secret-456")
	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "from-env", Source: &RedactionSource{Env: "TEST_REDACT_SECRET"}},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}
	if engine.compiledRules[0].resolvedValue != "env-secret-456" {
		t.Errorf("resolved value = %q, want %q", engine.compiledRules[0].resolvedValue, "env-secret-456")
	}
}

func TestRedactionEngine_SourceResolution_File(t *testing.T) {
	tmp := t.TempDir()
	secretFile := filepath.Join(tmp, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("file-secret-789\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "from-file", Source: &RedactionSource{File: secretFile}},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}
	if engine.compiledRules[0].resolvedValue != "file-secret-789" {
		t.Errorf("resolved value = %q, want %q", engine.compiledRules[0].resolvedValue, "file-secret-789")
	}
}

func TestRedactionEngine_SourceResolution_EnvFileKey(t *testing.T) {
	tmp := t.TempDir()
	envFile := filepath.Join(tmp, ".env")
	if err := os.WriteFile(envFile, []byte("DB_PASSWORD=hunter2\nAPP_KEY=not-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "db-pass", Source: &RedactionSource{EnvFileKey: "DB_PASSWORD"}},
		},
	}
	engine, err := NewRedactionEngine(cfg, tmp)
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}
	if engine.compiledRules[0].resolvedValue != "hunter2" {
		t.Errorf("resolved value = %q, want %q", engine.compiledRules[0].resolvedValue, "hunter2")
	}
}

func TestRedactionEngine_SourceResolution_Unresolvable(t *testing.T) {
	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "missing", Source: &RedactionSource{Env: "NONEXISTENT_VAR_XYZ"}},
		},
	}
	_, err := NewRedactionEngine(cfg, "")
	if err == nil {
		t.Error("expected error for unresolvable source, got nil")
	}
}

func TestRedactionEngine_Scan_BodyMatch(t *testing.T) {
	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "api-key", Source: &RedactionSource{Value: "sk-secret123"}},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}

	req := &http.Request{
		URL:    &url.URL{Scheme: "https", Host: "api.example.com", Path: "/v1/chat"},
		Header: http.Header{"Content-Type": {"application/json"}},
	}
	body := []byte(`{"prompt": "my key is sk-secret123 please help"}`)

	result := engine.Scan(req, body)
	if !result.Matched {
		t.Fatal("expected match")
	}
	if result.Action != RedactionActionBlock {
		t.Errorf("action = %v, want %v", result.Action, RedactionActionBlock)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(result.Matches))
	}
	if result.Matches[0].Location != "body" {
		t.Errorf("location = %v, want body", result.Matches[0].Location)
	}
}

func TestRedactionEngine_Scan_HeaderMatch(t *testing.T) {
	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "token", Source: &RedactionSource{Value: "Bearer my-leaked-token"}},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}

	req := &http.Request{
		URL:    &url.URL{Scheme: "https", Host: "api.example.com", Path: "/v1/chat"},
		Header: http.Header{"X-Custom": {"Bearer my-leaked-token"}},
	}

	result := engine.Scan(req, nil)
	if !result.Matched {
		t.Fatal("expected match in header")
	}
	if result.Matches[0].Location != "header:X-Custom" {
		t.Errorf("location = %v, want header:X-Custom", result.Matches[0].Location)
	}
}

func TestRedactionEngine_Scan_URLMatch(t *testing.T) {
	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "url-secret", Source: &RedactionSource{Value: "api_key=secret999"}},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}

	req := &http.Request{
		URL:    &url.URL{Scheme: "https", Host: "api.example.com", Path: "/v1/data", RawQuery: "api_key=secret999"},
		Header: http.Header{},
	}

	result := engine.Scan(req, nil)
	if !result.Matched {
		t.Fatal("expected match in URL")
	}
	if result.Matches[0].Location != "url" {
		t.Errorf("location = %v, want url", result.Matches[0].Location)
	}
}

func TestRedactionEngine_Scan_RegexMatch(t *testing.T) {
	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "openai-key", Pattern: `sk-[a-zA-Z0-9]{8,}`},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}

	req := &http.Request{
		URL:    &url.URL{Scheme: "https", Host: "api.example.com", Path: "/v1/chat"},
		Header: http.Header{},
	}
	body := []byte(`{"key": "sk-abcdefgh12345678"}`)

	result := engine.Scan(req, body)
	if !result.Matched {
		t.Fatal("expected regex match")
	}
}

func TestRedactionEngine_Scan_NoMatch(t *testing.T) {
	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "secret", Source: &RedactionSource{Value: "super-secret-value"}},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}

	req := &http.Request{
		URL:    &url.URL{Scheme: "https", Host: "api.example.com", Path: "/v1/chat"},
		Header: http.Header{},
	}
	body := []byte(`{"prompt": "hello world"}`)

	result := engine.Scan(req, body)
	if result.Matched {
		t.Error("expected no match")
	}
}

func TestRedactionEngine_Scan_ActionPrecedence(t *testing.T) {
	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionLog,
		Rules: []RedactionRule{
			{Name: "log-rule", Action: RedactionActionLog, Source: &RedactionSource{Value: "secret-a"}},
			{Name: "block-rule", Action: RedactionActionBlock, Source: &RedactionSource{Value: "secret-b"}},
			{Name: "redact-rule", Action: RedactionActionRedact, Source: &RedactionSource{Value: "secret-c"}},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}

	req := &http.Request{
		URL:    &url.URL{Scheme: "https", Host: "api.example.com", Path: "/"},
		Header: http.Header{},
	}
	// Body contains all three secrets
	body := []byte("secret-a secret-b secret-c")

	result := engine.Scan(req, body)
	if !result.Matched {
		t.Fatal("expected match")
	}
	// Block should win over redact and log
	if result.Action != RedactionActionBlock {
		t.Errorf("action = %v, want %v (block wins)", result.Action, RedactionActionBlock)
	}
	if len(result.Matches) != 3 {
		t.Errorf("expected 3 matches, got %d", len(result.Matches))
	}
}

func TestRedactionEngine_Scan_Redact(t *testing.T) {
	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionRedact,
		Rules: []RedactionRule{
			{Name: "api-key", Source: &RedactionSource{Value: "sk-secret123"}},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}

	req := &http.Request{
		URL:    &url.URL{Scheme: "https", Host: "api.example.com", Path: "/v1/chat", RawQuery: "key=sk-secret123"},
		Header: http.Header{"X-Key": {"sk-secret123"}},
	}
	body := []byte(`{"key": "sk-secret123"}`)

	result := engine.Scan(req, body)
	if !result.Matched {
		t.Fatal("expected match")
	}
	if result.Action != RedactionActionRedact {
		t.Fatalf("action = %v, want redact", result.Action)
	}

	// Verify body redaction
	if strings.Contains(string(result.Body), "sk-secret123") {
		t.Error("body still contains secret")
	}
	if !strings.Contains(string(result.Body), "[REDACTED:api-key]") {
		t.Errorf("body missing redaction placeholder, got: %s", result.Body)
	}

	// Verify URL redaction
	if strings.Contains(result.URL, "sk-secret123") {
		t.Error("URL still contains secret")
	}
	if !strings.Contains(result.URL, "[REDACTED:api-key]") {
		t.Errorf("URL missing redaction placeholder, got: %s", result.URL)
	}

	// Verify header redaction
	if val := result.Headers["X-Key"]; len(val) > 0 && strings.Contains(val[0], "sk-secret123") {
		t.Error("header still contains secret")
	}
}

func TestRedactionEngine_Disabled(t *testing.T) {
	cfg := &RedactionConfig{
		Enabled:       boolPtr(false),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "secret", Source: &RedactionSource{Value: "should-not-match"}},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}
	if engine.IsEnabled() {
		t.Error("expected engine to be disabled")
	}
}

func TestRedactionEngine_EmptyBody(t *testing.T) {
	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "secret", Source: &RedactionSource{Value: "some-secret"}},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}

	req := &http.Request{
		URL:    &url.URL{Scheme: "https", Host: "api.example.com", Path: "/"},
		Header: http.Header{},
	}

	result := engine.Scan(req, nil)
	if result.Matched {
		t.Error("expected no match on empty body")
	}
}

func TestRedactionEngine_Scan_MixedActions_AllSecretsRedacted(t *testing.T) {
	logSecret := "log-only-secret-456"
	redactSecret := "redact-this-secret-789"
	cfg := &RedactionConfig{
		Enabled: boolPtr(true),
		Rules: []RedactionRule{
			{
				Name:   "log-rule",
				Action: RedactionActionLog,
				Source: &RedactionSource{Value: logSecret},
			},
			{
				Name:   "redact-rule",
				Action: RedactionActionRedact,
				Source: &RedactionSource{Value: redactSecret},
			},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatal(err)
	}

	body := []byte("log=" + logSecret + "&key=" + redactSecret)
	req, _ := http.NewRequest("POST", "https://example.com", nil)
	result := engine.Scan(req, body)

	if result.Action != RedactionActionRedact {
		t.Fatalf("expected redact (highest severity), got %s", result.Action)
	}
	// Both secrets must be redacted when overall action is redact/block,
	// even if a specific rule's action is log. This prevents secret leakage
	// in log entries when the request is being blocked or modified.
	if strings.Contains(string(result.Body), redactSecret) {
		t.Error("redact-rule secret should be replaced in body")
	}
	if strings.Contains(string(result.Body), logSecret) {
		t.Error("log-rule secret must also be redacted from body when overall action is redact")
	}
	if !strings.Contains(string(result.Body), "[REDACTED:log-rule]") {
		t.Error("body should contain redaction placeholder for log-rule")
	}
	if !strings.Contains(string(result.Body), "[REDACTED:redact-rule]") {
		t.Error("body should contain redaction placeholder for redact-rule")
	}
}

func TestRedactionEngine_Scan_LogOnlyActionDoesNotRedactBody(t *testing.T) {
	secret := "super-secret-value-123"
	cfg := &RedactionConfig{
		Enabled: boolPtr(true),
		Rules: []RedactionRule{
			{
				Name:   "log-only-rule",
				Action: RedactionActionLog,
				Source: &RedactionSource{Value: secret},
			},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"token": "` + secret + `"}`)
	req, _ := http.NewRequest("POST", "https://api.example.com/v1", nil)
	result := engine.Scan(req, body)

	if !result.Matched {
		t.Fatal("expected match")
	}
	if result.Action != RedactionActionLog {
		t.Fatalf("expected log action, got %s", result.Action)
	}
	// Body should be UNCHANGED for log action
	if result.Body != nil {
		t.Errorf("log action should not produce redacted body, got: %s", result.Body)
	}
}

func TestRedactionEngine_Scan_MixedActions_BlockEscalation_AllFieldsSanitized(t *testing.T) {
	logSecret := "log-secret-AAA"
	blockSecret := "block-secret-BBB"
	cfg := &RedactionConfig{
		Enabled: boolPtr(true),
		Rules: []RedactionRule{
			{
				Name:   "log-rule",
				Action: RedactionActionLog,
				Source: &RedactionSource{Value: logSecret},
			},
			{
				Name:   "block-rule",
				Action: RedactionActionBlock,
				Source: &RedactionSource{Value: blockSecret},
			},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatal(err)
	}

	body := []byte("data=" + logSecret + "&key=" + blockSecret)
	req := &http.Request{
		URL:    &url.URL{Scheme: "https", Host: "api.example.com", Path: "/v1", RawQuery: "a=" + logSecret + "&b=" + blockSecret},
		Header: http.Header{"X-Log": {logSecret}, "X-Block": {blockSecret}},
	}
	result := engine.Scan(req, body)

	if result.Action != RedactionActionBlock {
		t.Fatalf("expected block, got %s", result.Action)
	}

	// ALL secrets must be absent from result fields — including the log-rule's secret
	for _, field := range []struct {
		name string
		val  string
	}{
		{"body", string(result.Body)},
		{"url", result.URL},
		{"header X-Log", strings.Join(result.Headers["X-Log"], ",")},
		{"header X-Block", strings.Join(result.Headers["X-Block"], ",")},
	} {
		if strings.Contains(field.val, logSecret) {
			t.Errorf("%s still contains log-rule secret", field.name)
		}
		if strings.Contains(field.val, blockSecret) {
			t.Errorf("%s still contains block-rule secret", field.name)
		}
	}
}

func TestRedactionEngine_MatchesValue_SourceRule(t *testing.T) {
	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "api-key", Source: &RedactionSource{Value: "secret-abc-123"}},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}

	matches := engine.MatchesValue("secret-abc-123")
	if len(matches) != 1 || matches[0] != "api-key" {
		t.Errorf("MatchesValue() = %v, want [\"api-key\"]", matches)
	}
}

func TestRedactionEngine_MatchesValue_PatternRule(t *testing.T) {
	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "openai-keys", Pattern: "sk-[a-zA-Z0-9]{20,}"},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}

	matches := engine.MatchesValue("sk-abcdefghijklmnopqrstuvwxyz")
	if len(matches) != 1 || matches[0] != "openai-keys" {
		t.Errorf("MatchesValue() = %v, want [\"openai-keys\"]", matches)
	}
}

func TestRedactionEngine_MatchesValue_NoMatch(t *testing.T) {
	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "api-key", Source: &RedactionSource{Value: "secret-abc-123"}},
			{Name: "openai-keys", Pattern: "sk-[a-zA-Z0-9]{20,}"},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}

	matches := engine.MatchesValue("totally-different-value")
	if len(matches) != 0 {
		t.Errorf("MatchesValue() = %v, want empty", matches)
	}
}

func TestRedactionEngine_MatchesValue_MultipleMatches(t *testing.T) {
	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "exact-match", Source: &RedactionSource{Value: "sk-abcdefghijklmnopqrstuvwxyz"}},
			{Name: "pattern-match", Pattern: "sk-[a-zA-Z0-9]{20,}"},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}

	matches := engine.MatchesValue("sk-abcdefghijklmnopqrstuvwxyz")
	if len(matches) != 2 {
		t.Errorf("MatchesValue() = %v, want 2 matches", matches)
	}
}

func TestRedactionEngine_MatchesValue_SourceContains(t *testing.T) {
	// Source rules use strings.Contains — value can be a substring
	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "partial", Source: &RedactionSource{Value: "secret"}},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}

	// The credential value contains the redaction rule's resolved value
	matches := engine.MatchesValue("my-secret-token")
	if len(matches) != 1 || matches[0] != "partial" {
		t.Errorf("MatchesValue() = %v, want [\"partial\"]", matches)
	}
}

func TestValidateCredentialRedactionConflicts_SourceConflict(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "secret-abc-123")

	injector := &GitHubCredentialInjector{}
	injector.Configure(map[string]any{"enabled": true})

	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "api-key", Source: &RedactionSource{Value: "secret-abc-123"}},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}

	err = validateCredentialRedactionConflicts([]CredentialInjector{injector}, engine)
	if err == nil {
		t.Fatal("expected error for conflicting credential and redaction rule")
	}
	if !strings.Contains(err.Error(), "github") {
		t.Errorf("error should mention injector name, got: %v", err)
	}
	if !strings.Contains(err.Error(), "api-key") {
		t.Errorf("error should mention rule name, got: %v", err)
	}
}

func TestValidateCredentialRedactionConflicts_PatternConflict(t *testing.T) {
	injector := &GitHubCredentialInjector{token: "sk-abcdefghijklmnopqrstuvwxyz", enabled: true}

	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "openai-keys", Pattern: "sk-[a-zA-Z0-9]{20,}"},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}

	err = validateCredentialRedactionConflicts([]CredentialInjector{injector}, engine)
	if err == nil {
		t.Fatal("expected error for pattern-matching credential")
	}
	if !strings.Contains(err.Error(), "openai-keys") {
		t.Errorf("error should mention rule name, got: %v", err)
	}
}

func TestValidateCredentialRedactionConflicts_NoConflict(t *testing.T) {
	injector := &GitHubCredentialInjector{token: "github-token-xyz", enabled: true}

	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "aws-keys", Pattern: "AKIA[0-9A-Z]{16}"},
			{Name: "other-secret", Source: &RedactionSource{Value: "completely-different"}},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}

	err = validateCredentialRedactionConflicts([]CredentialInjector{injector}, engine)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidateCredentialRedactionConflicts_NilEngine(t *testing.T) {
	injector := &GitHubCredentialInjector{token: "some-token", enabled: true}

	err := validateCredentialRedactionConflicts([]CredentialInjector{injector}, nil)
	if err != nil {
		t.Errorf("expected no error with nil engine, got: %v", err)
	}
}

func TestValidateCredentialRedactionConflicts_NoInjectors(t *testing.T) {
	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "some-rule", Source: &RedactionSource{Value: "some-secret"}},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}

	err = validateCredentialRedactionConflicts(nil, engine)
	if err != nil {
		t.Errorf("expected no error with no injectors, got: %v", err)
	}
}

func TestValidateCredentialRedactionConflicts_DisabledInjectorSkipped(t *testing.T) {
	// Disabled injector should not trigger conflict even if value would match
	injector := &GitHubCredentialInjector{token: "", enabled: false}

	cfg := &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "catch-all", Source: &RedactionSource{Value: "anything"}},
		},
	}
	engine, err := NewRedactionEngine(cfg, "")
	if err != nil {
		t.Fatalf("NewRedactionEngine: %v", err)
	}

	err = validateCredentialRedactionConflicts([]CredentialInjector{injector}, engine)
	if err != nil {
		t.Errorf("expected no error for disabled injector, got: %v", err)
	}
}
