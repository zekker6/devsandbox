package proxy

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestLogRequest_RedactsSensitiveHeaders(t *testing.T) {
	dir := t.TempDir()
	rl, err := NewRequestLogger(dir, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()

	req, _ := http.NewRequest("GET", "https://api.example.com/data", nil)
	req.Header.Set("Authorization", "Bearer secret-token-123")
	req.Header.Set("Cookie", "session=abc123")
	req.Header.Set("X-Api-Key", "key-456")
	req.Header.Set("Accept", "application/json")

	entry, _ := rl.LogRequest(req)

	// Authorization should be redacted
	if auth := entry.RequestHeaders["Authorization"]; len(auth) > 0 && strings.Contains(auth[0], "secret") {
		t.Error("Authorization header should be redacted in log entry")
	}
	// Cookie should be redacted
	if cookie := entry.RequestHeaders["Cookie"]; len(cookie) > 0 && strings.Contains(cookie[0], "abc123") {
		t.Error("Cookie header should be redacted in log entry")
	}
	// X-Api-Key should be redacted
	if apiKey := entry.RequestHeaders["X-Api-Key"]; len(apiKey) > 0 && strings.Contains(apiKey[0], "key-456") {
		t.Error("X-Api-Key header should be redacted in log entry")
	}
	// Accept should NOT be redacted
	if accept := entry.RequestHeaders["Accept"]; len(accept) == 0 || accept[0] != "application/json" {
		t.Error("Accept header should not be redacted")
	}
}

func TestRequestLog_RedactionFields(t *testing.T) {
	entry := &RequestLog{
		Method:           "POST",
		URL:              "https://api.example.com/v1/chat",
		RedactionAction:  "block",
		RedactionMatches: []string{"api-key", "db-password"},
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}

	// Verify fields are present in JSON
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded["redaction_action"] != "block" {
		t.Errorf("redaction_action = %v, want block", decoded["redaction_action"])
	}
	matches, ok := decoded["redaction_matches"].([]any)
	if !ok || len(matches) != 2 {
		t.Errorf("redaction_matches = %v, want [api-key, db-password]", decoded["redaction_matches"])
	}
}

func TestRequestLog_RedactionFields_Omitted(t *testing.T) {
	entry := &RequestLog{
		Method: "GET",
		URL:    "https://example.com/",
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if _, exists := decoded["redaction_action"]; exists {
		t.Error("redaction_action should be omitted when empty")
	}
	if _, exists := decoded["redaction_matches"]; exists {
		t.Error("redaction_matches should be omitted when empty")
	}
}

func TestRequestLog_RedactionUpdatesEntry(t *testing.T) {
	secret := "super-secret-value-123"
	redacted := "[REDACTED:test-rule]"

	entry := &RequestLog{
		URL:            "https://api.example.com/v1?key=" + secret,
		RequestBody:    []byte(`{"token": "` + secret + `"}`),
		RequestHeaders: map[string][]string{"X-Token": {secret}},
	}

	// Simulate what server.go should do after redaction:
	// update the entry with redacted values from the RedactionResult
	result := &RedactionResult{
		Matched: true,
		Action:  RedactionActionRedact,
		URL:     strings.ReplaceAll(entry.URL, secret, redacted),
		Body:    []byte(strings.ReplaceAll(string(entry.RequestBody), secret, redacted)),
		Headers: map[string][]string{
			"X-Token": {strings.ReplaceAll(entry.RequestHeaders["X-Token"][0], secret, redacted)},
		},
	}

	// Apply redacted values to entry (this is what the fix in server.go must do)
	if result.Body != nil {
		entry.RequestBody = result.Body
	}
	if result.URL != "" {
		entry.URL = result.URL
	}
	if result.Headers != nil {
		entry.RequestHeaders = result.Headers
	}

	if strings.Contains(entry.URL, secret) {
		t.Error("URL still contains secret after redaction")
	}
	if strings.Contains(string(entry.RequestBody), secret) {
		t.Error("RequestBody still contains secret after redaction")
	}
	if strings.Contains(entry.RequestHeaders["X-Token"][0], secret) {
		t.Error("RequestHeaders still contains secret after redaction")
	}
}

func TestToLogEntry_RedactionBlock(t *testing.T) {
	dir := t.TempDir()
	rl, err := NewRequestLogger(dir, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()

	entry := &RequestLog{
		Method:           "POST",
		URL:              "https://api.example.com/v1",
		RedactionAction:  "block",
		RedactionMatches: []string{"rule-1", "rule-2"},
	}

	logEntry := rl.toLogEntry(entry)

	if logEntry.Level != "warn" {
		t.Errorf("level = %v, want warn for redaction block", logEntry.Level)
	}
	if logEntry.Fields["redaction_action"] != "block" {
		t.Errorf("redaction_action = %v, want block", logEntry.Fields["redaction_action"])
	}
	matches, ok := logEntry.Fields["redaction_matches"].([]string)
	if !ok || len(matches) != 2 {
		t.Errorf("redaction_matches = %v, want [rule-1, rule-2]", logEntry.Fields["redaction_matches"])
	}
}

func TestToLogEntry_RedactionRedact(t *testing.T) {
	dir := t.TempDir()
	rl, err := NewRequestLogger(dir, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()

	entry := &RequestLog{
		Method:           "POST",
		URL:              "https://api.example.com/v1",
		RedactionAction:  "redact",
		RedactionMatches: []string{"api-key"},
	}

	logEntry := rl.toLogEntry(entry)

	if logEntry.Fields["redaction_action"] != "redact" {
		t.Errorf("redaction_action = %v, want redact", logEntry.Fields["redaction_action"])
	}
	matches, ok := logEntry.Fields["redaction_matches"].([]string)
	if !ok || len(matches) != 1 || matches[0] != "api-key" {
		t.Errorf("redaction_matches = %v, want [api-key]", logEntry.Fields["redaction_matches"])
	}
	// Redact action (not block) should be info level by default
	if logEntry.Level != "info" {
		t.Errorf("level = %v, want info for redact action", logEntry.Level)
	}
}

func TestToLogEntry_NoRedaction(t *testing.T) {
	dir := t.TempDir()
	rl, err := NewRequestLogger(dir, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()

	entry := &RequestLog{
		Method:     "GET",
		URL:        "https://example.com/",
		StatusCode: 200,
	}

	logEntry := rl.toLogEntry(entry)

	if _, exists := logEntry.Fields["redaction_action"]; exists {
		t.Error("redaction_action should not be present when no redaction occurred")
	}
	if _, exists := logEntry.Fields["redaction_matches"]; exists {
		t.Error("redaction_matches should not be present when no redaction occurred")
	}
}

func TestRedactHeaders_Nil(t *testing.T) {
	result := redactHeaders(nil)
	if result != nil {
		t.Error("redactHeaders(nil) should return nil")
	}
}
