package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"devsandbox/internal/logging"
)

// recordingWriter is a logging.Writer test double that counts Write calls.
type recordingWriter struct {
	count atomic.Int64
}

func (w *recordingWriter) Write(_ *logging.Entry) error {
	w.count.Add(1)
	return nil
}

func (w *recordingWriter) Close() error { return nil }

func TestLogRequest_RedactsSensitiveHeaders(t *testing.T) {
	dir := t.TempDir()
	rl, err := NewRequestLogger(dir, nil, false, nil)
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

// Regression: goproxy can dispatch HTTPS requests with a nil URL when its
// internal url.Parse fallback fails. LogRequest must not panic in that case.
// https://github.com/elazarl/goproxy/blob/v1.8.3/https.go#L272-L274
func TestLogRequest_NilURL(t *testing.T) {
	dir := t.TempDir()
	rl, err := NewRequestLogger(dir, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()

	req := &http.Request{
		Method:     "GET",
		RequestURI: "/some/path",
		Header:     http.Header{},
	}

	entry, _ := rl.LogRequest(req)
	if entry == nil {
		t.Fatal("expected entry, got nil")
	}
	if entry.URL != "/some/path" {
		t.Errorf("URL = %q, want %q (falling back to RequestURI)", entry.URL, "/some/path")
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
	rl, err := NewRequestLogger(dir, nil, false, nil)
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
	rl, err := NewRequestLogger(dir, nil, false, nil)
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
	rl, err := NewRequestLogger(dir, nil, false, nil)
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

func TestIsStreamingResponse(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		want        bool
	}{
		{"sse plain", "text/event-stream", true},
		{"sse with charset", "text/event-stream; charset=utf-8", true},
		{"sse mixed case + spaces", "  Text/Event-Stream ; charset=utf-8", true},
		{"ndjson", "application/x-ndjson", true},
		{"json", "application/json", false},
		{"json with charset", "application/json; charset=utf-8", false},
		{"empty", "", false},
		{"octet-stream", "application/octet-stream", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{}}
			if tt.contentType != "" {
				resp.Header.Set("Content-Type", tt.contentType)
			}
			if got := isStreamingResponse(resp); got != tt.want {
				t.Errorf("isStreamingResponse(%q) = %v, want %v", tt.contentType, got, tt.want)
			}
		})
	}
}

// TestLogResponse_StreamingBodyNotConsumed verifies that LogResponse leaves a
// streaming response body untouched. Buffering it would block goproxy from
// relaying response headers until the stream closes, causing SSE clients to
// time out waiting for headers.
func TestLogResponse_StreamingBodyNotConsumed(t *testing.T) {
	dir := t.TempDir()
	rl, err := NewRequestLogger(dir, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()

	body := &blockingReadCloser{ch: make(chan struct{})}
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body:       body,
	}

	entry := &RequestLog{Method: "POST", URL: "https://api.openai.com/v1/responses"}
	rl.LogResponse(entry, resp, time.Now())

	if body.readCalled.Load() {
		t.Error("LogResponse read from a streaming response body; it must be passed through untouched")
	}
	// Original body must remain in place so goproxy can stream it to the client.
	if resp.Body != body {
		t.Error("LogResponse replaced the streaming response body; original must be preserved")
	}
	// Metadata is still captured.
	if entry.StatusCode != 200 || entry.ResponseHeaders["Content-Type"][0] != "text/event-stream" {
		t.Error("LogResponse should still capture status and headers for streaming responses")
	}
}

// blockingReadCloser is a Body that blocks forever on Read (like a live SSE
// stream) and records whether Read was ever called.
type blockingReadCloser struct {
	ch         chan struct{}
	readCalled atomic.Bool
}

func (b *blockingReadCloser) Read(_ []byte) (int, error) {
	b.readCalled.Store(true)
	<-b.ch // block until Close
	return 0, io.EOF
}

func (b *blockingReadCloser) Close() error {
	close(b.ch)
	return nil
}

// TestLogResponseStreaming_StreamsAndLogsOnClose verifies the body streams
// through unchanged and the log entry (with captured body) is written when the
// body is closed - not buffered up front. The response has an empty
// Content-Type, reproducing codex's /backend-api/codex/responses.
func TestLogResponseStreaming_StreamsAndLogsOnClose(t *testing.T) {
	dir := t.TempDir()
	rl, err := NewRequestLogger(dir, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()

	const body = "streamed-token-1 streamed-token-2"
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{}, // empty Content-Type, like codex
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    &http.Request{Method: "POST"},
	}
	entry := &RequestLog{Method: "POST", URL: "https://chatgpt.com/backend-api/codex/responses"}

	rl.LogResponseStreaming(entry, resp, time.Now())

	// Nothing should be logged yet: the body has not been read/closed.
	if got := readActiveLogFile(t, dir); got != "" {
		t.Errorf("entry logged before body close; streaming response was buffered: %q", got)
	}

	// Body streams through unchanged.
	got, _ := io.ReadAll(resp.Body)
	if string(got) != body {
		t.Errorf("streamed body = %q, want %q", got, body)
	}
	_ = resp.Body.Close()

	// On close, the entry is logged with the captured body.
	if string(entry.ResponseBody) != body {
		t.Errorf("captured body = %q, want %q", entry.ResponseBody, body)
	}
	if contents := readActiveLogFile(t, dir); !strings.Contains(contents, "codex/responses") {
		t.Errorf("expected response logged on close, got %q", contents)
	}
}

// TestLogResponseStreaming_CapsCapturedBody verifies large bodies stream in full
// but only a bounded prefix is captured for logging.
func TestLogResponseStreaming_CapsCapturedBody(t *testing.T) {
	dir := t.TempDir()
	rl, err := NewRequestLogger(dir, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()

	big := strings.Repeat("x", maxResponseLogBytes+4096)
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(big)),
		Request:    &http.Request{Method: "GET"},
	}
	entry := &RequestLog{Method: "GET", URL: "https://example.com/big"}

	rl.LogResponseStreaming(entry, resp, time.Now())

	got, _ := io.ReadAll(resp.Body)
	if len(got) != len(big) {
		t.Errorf("streamed %d bytes, want full %d", len(got), len(big))
	}
	_ = resp.Body.Close()

	if len(entry.ResponseBody) != maxResponseLogBytes {
		t.Errorf("captured %d bytes, want cap %d", len(entry.ResponseBody), maxResponseLogBytes)
	}
}

// TestLogResponseStreaming_HeadNotWrapped verifies HEAD responses are logged
// immediately and their body is left untouched (Content-Length preservation).
func TestLogResponseStreaming_HeadNotWrapped(t *testing.T) {
	dir := t.TempDir()
	rl, err := NewRequestLogger(dir, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()

	origBody := io.NopCloser(strings.NewReader(""))
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Length": {"1048576"}},
		Body:       origBody,
		Request:    &http.Request{Method: http.MethodHead},
	}
	entry := &RequestLog{Method: http.MethodHead, URL: "https://reg.example.com/v2/manifests/v1"}

	rl.LogResponseStreaming(entry, resp, time.Now())

	if resp.Body != origBody {
		t.Error("HEAD response body was wrapped; must be left untouched to preserve Content-Length")
	}
	// HEAD is logged immediately (no body to wait for).
	if contents := readActiveLogFile(t, dir); !strings.Contains(contents, "manifests/v1") {
		t.Errorf("expected HEAD entry logged immediately, got %q", contents)
	}
}

func TestRedactHeaders_Nil(t *testing.T) {
	result := redactHeaders(nil)
	if result != nil {
		t.Error("redactHeaders(nil) should return nil")
	}
}

// readActiveLogFile reads concatenated contents of all uncompressed request
// log files in the directory. Files are named like
// "requests_<YYYYMMDD>_<NNNN>.jsonl" so we glob rather than guess the path.
// Returns "" if no files exist or all are empty.
func readActiveLogFile(t *testing.T, dir string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, RequestLogPrefix+"_*"+RequestLogSuffix))
	if err != nil {
		t.Fatalf("glob log files: %v", err)
	}
	var sb strings.Builder
	for _, p := range matches {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		sb.Write(data)
	}
	return sb.String()
}

func TestRequestLogger_Log_SkipsMatchingEntries(t *testing.T) {
	dir := t.TempDir()
	skipEngine, err := NewLogSkipEngine(&LogSkipConfig{Rules: []LogSkipRule{
		{Pattern: "telemetry.example.com", Type: PatternTypeExact},
	}})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}

	dispatcher := logging.NewDispatcher()
	rec := &recordingWriter{}
	dispatcher.AddWriter(rec)

	rl, err := NewRequestLogger(dir, dispatcher, true, skipEngine)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	defer func() { _ = rl.Close() }()

	skipped := &RequestLog{
		Timestamp: time.Now(),
		Method:    "POST",
		URL:       "https://telemetry.example.com/v1/traces",
	}
	if err := rl.Log(skipped); err != nil {
		t.Fatalf("Log: %v", err)
	}

	if got := readActiveLogFile(t, dir); got != "" {
		t.Errorf("expected empty log file, got %q", got)
	}
	if n := rec.count.Load(); n != 0 {
		t.Errorf("expected 0 dispatcher writes, got %d", n)
	}
}

func TestRequestLogger_Log_KeepsNonMatchingEntries(t *testing.T) {
	dir := t.TempDir()
	skipEngine, err := NewLogSkipEngine(&LogSkipConfig{Rules: []LogSkipRule{
		{Pattern: "telemetry.example.com", Type: PatternTypeExact},
	}})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}

	dispatcher := logging.NewDispatcher()
	rec := &recordingWriter{}
	dispatcher.AddWriter(rec)

	rl, err := NewRequestLogger(dir, dispatcher, true, skipEngine)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	defer func() { _ = rl.Close() }()

	kept := &RequestLog{
		Timestamp:  time.Now(),
		Method:     "GET",
		URL:        "https://api.example.com/v1/chat",
		StatusCode: 200,
	}
	if err := rl.Log(kept); err != nil {
		t.Fatalf("Log: %v", err)
	}

	contents := readActiveLogFile(t, dir)
	if !strings.Contains(contents, "api.example.com") {
		t.Errorf("expected log file to contain non-matched URL, got %q", contents)
	}
	if n := rec.count.Load(); n != 1 {
		t.Errorf("expected exactly 1 dispatcher write, got %d", n)
	}
}

func TestRequestLogger_Log_NilSkipEngineAlwaysLogs(t *testing.T) {
	dir := t.TempDir()
	dispatcher := logging.NewDispatcher()
	rec := &recordingWriter{}
	dispatcher.AddWriter(rec)

	rl, err := NewRequestLogger(dir, dispatcher, true, nil)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	defer func() { _ = rl.Close() }()

	entry := &RequestLog{
		Timestamp:  time.Now(),
		Method:     "GET",
		URL:        "https://anything.example.com/",
		StatusCode: 200,
	}
	if err := rl.Log(entry); err != nil {
		t.Fatalf("Log: %v", err)
	}

	if got := readActiveLogFile(t, dir); !strings.Contains(got, "anything.example.com") {
		t.Errorf("nil skip engine should always log, got %q", got)
	}
	if n := rec.count.Load(); n != 1 {
		t.Errorf("nil skip engine: expected 1 dispatcher write, got %d", n)
	}
}
