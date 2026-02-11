package proxy

import (
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

func TestRedactHeaders_Nil(t *testing.T) {
	result := redactHeaders(nil)
	if result != nil {
		t.Error("redactHeaders(nil) should return nil")
	}
}
