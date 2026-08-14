package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"devsandbox/internal/config"
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

	// Exactly once. Reading to EOF finalizes, and the Close above finalizes
	// again; without the logOnce guard both write an entry, which doubles every
	// streamed request in `devsandbox logs proxy` and double-counts
	// RequestCount() into the session.end audit field.
	if n := countLogLines(t, dir); n != 1 {
		t.Errorf("log holds %d entries for one streamed response, want 1", n)
	}
	if got := rl.RequestCount(); got != 1 {
		t.Errorf("RequestCount() = %d for one streamed response, want 1", got)
	}
}

// countLogLines returns the number of non-empty lines in the active log file.
func countLogLines(t *testing.T, dir string) int {
	t.Helper()

	n := 0
	for line := range strings.SplitSeq(readActiveLogFile(t, dir), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
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

	big := strings.Repeat("x", config.DefaultMaxLogBodyBytes+4096)
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

	if len(entry.ResponseBody) != config.DefaultMaxLogBodyBytes {
		t.Errorf("captured %d bytes, want cap %d", len(entry.ResponseBody), config.DefaultMaxLogBodyBytes)
	}
	if !entry.ResponseBodyTruncated {
		t.Error("resp_body_truncated not set; a reader cannot tell truncation from a short body")
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

func TestCaptureHeaders_Nil(t *testing.T) {
	result, truncated := captureHeaders(nil)
	if result != nil {
		t.Error("captureHeaders(nil) should return nil")
	}
	if truncated {
		t.Error("captureHeaders(nil) should not report truncation")
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

// trickleBody yields one byte per Read forever and never reaches EOF, like a
// chunked upload a sandboxed client keeps open. Reading it to completion never
// terminates, which is what the unbounded io.ReadAll capture used to attempt.
type trickleBody struct {
	delay  time.Duration
	closed chan struct{}
	once   sync.Once
}

func newTrickleBody(delay time.Duration) *trickleBody {
	return &trickleBody{delay: delay, closed: make(chan struct{})}
}

func (b *trickleBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	select {
	case <-b.closed:
		return 0, io.EOF
	case <-time.After(b.delay):
	}
	p[0] = 'x'
	return 1, nil
}

func (b *trickleBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

// stallingBody delivers a prefix, then blocks until released, then delivers a
// suffix and ends. It models a client that stops sending mid-body.
type stallingBody struct {
	prefix, suffix string
	release        chan struct{}
	pos            int
	stalled        bool
}

func (b *stallingBody) Read(p []byte) (int, error) {
	if b.pos < len(b.prefix) {
		n := copy(p, b.prefix[b.pos:])
		b.pos += n
		return n, nil
	}
	if !b.stalled {
		b.stalled = true
		<-b.release
	}
	rest := b.pos - len(b.prefix)
	if rest >= len(b.suffix) {
		return 0, io.EOF
	}
	n := copy(p, b.suffix[rest:])
	b.pos += n
	return n, nil
}

func (b *stallingBody) Close() error { return nil }

// TestLogRequest_CapsCapturedBody verifies the captured prefix is bounded while
// the body forwarded upstream stays complete. Before the bound existed,
// LogRequest buffered the whole body into the log entry.
func TestLogRequest_CapsCapturedBody(t *testing.T) {
	dir := t.TempDir()
	rl, err := NewRequestLogger(dir, nil, false, nil, WithMaxBodyLogBytes(1024))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()

	body := strings.Repeat("a", 8192)
	req, err := http.NewRequest("POST", "https://api.example.com/upload", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	entry, captured := rl.LogRequest(req)

	if len(entry.RequestBody) != 1024 {
		t.Errorf("captured %d bytes into the log entry, want cap 1024", len(entry.RequestBody))
	}
	if len(captured) != 1024 {
		t.Errorf("returned %d captured bytes, want cap 1024", len(captured))
	}
	if !entry.RequestBodyTruncated {
		t.Error("req_body_truncated not set; a reader cannot tell truncation from a short body")
	}
	forwarded, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read forwarded body: %v", err)
	}
	if string(forwarded) != body {
		t.Errorf("forwarded %d bytes, want the full %d - upstream must receive the body intact",
			len(forwarded), len(body))
	}
}

// TestLogRequest_NeverEndingBodyDoesNotBlock verifies a body that never reaches
// EOF cannot pin the handler. LogRequest runs before filtering, so an unbounded
// read here is reachable by any sandboxed client.
func TestLogRequest_NeverEndingBodyDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	rl, err := NewRequestLogger(dir, nil, false, nil, WithMaxBodyLogBytes(16))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()

	body := newTrickleBody(time.Millisecond)
	defer func() { _ = body.Close() }()

	req := &http.Request{
		Method: "POST",
		URL:    mustParseURL(t, "https://api.example.com/stream"),
		Header: http.Header{},
		Body:   body,
	}

	done := make(chan *RequestLog, 1)
	go func() {
		entry, _ := rl.LogRequest(req)
		done <- entry
	}()

	select {
	case entry := <-done:
		if len(entry.RequestBody) != 16 {
			t.Errorf("captured %d bytes, want cap 16", len(entry.RequestBody))
		}
		if !entry.RequestBodyTruncated {
			t.Error("req_body_truncated not set for a body that exceeds the cap")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("LogRequest did not return: an endless request body blocks the handler")
	}
}

// TestLogRequest_StalledBodyHitsCaptureDeadline verifies a client that stops
// sending mid-body releases the handler at the capture deadline, and that the
// bytes it eventually sends still reach upstream in order.
func TestLogRequest_StalledBodyHitsCaptureDeadline(t *testing.T) {
	dir := t.TempDir()
	rl, err := NewRequestLogger(dir, nil, false, nil, WithMaxBodyLogBytes(1024))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()
	rl.bodyCaptureTimeout = 50 * time.Millisecond

	body := &stallingBody{prefix: "sent-", suffix: "eventually", release: make(chan struct{})}
	req := &http.Request{
		Method: "POST",
		URL:    mustParseURL(t, "https://api.example.com/slow"),
		Header: http.Header{},
		Body:   body,
	}

	done := make(chan *RequestLog, 1)
	go func() {
		entry, _ := rl.LogRequest(req)
		done <- entry
	}()

	var entry *RequestLog
	select {
	case entry = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("LogRequest did not return: a stalled request body blocks the handler")
	}

	if !entry.RequestBodyTruncated {
		t.Error("req_body_truncated not set for a capture cut short by its deadline")
	}

	// The client resumes: the forwarded body must still be complete and ordered.
	close(body.release)
	forwarded, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read forwarded body: %v", err)
	}
	if want := body.prefix + body.suffix; string(forwarded) != want {
		t.Errorf("forwarded %q, want %q", forwarded, want)
	}
}

// TestLogRequest_CanceledRequestReleasesCapture verifies a client that goes
// away releases the capture immediately, without waiting out the deadline.
func TestLogRequest_CanceledRequestReleasesCapture(t *testing.T) {
	dir := t.TempDir()
	rl, err := NewRequestLogger(dir, nil, false, nil, WithMaxBodyLogBytes(1024))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()
	rl.bodyCaptureTimeout = time.Hour // only cancellation can end this capture

	ctx, cancel := context.WithCancel(context.Background())
	body := &stallingBody{prefix: "partial", release: make(chan struct{})}
	req := (&http.Request{
		Method: "POST",
		URL:    mustParseURL(t, "https://api.example.com/gone"),
		Header: http.Header{},
		Body:   body,
	}).WithContext(ctx)

	done := make(chan *RequestLog, 1)
	go func() {
		entry, _ := rl.LogRequest(req)
		done <- entry
	}()

	// Give the capture a moment to reach the stall, then abandon the request.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case entry := <-done:
		if !entry.RequestBodyTruncated {
			t.Error("req_body_truncated not set for a capture cut short by cancellation")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("LogRequest ignored request cancellation")
	}
	close(body.release)
}

// TestLogRequest_SmallBodyCapturedExactly guards the common case: a body under
// the cap is captured byte-identically, so redaction and log readers see what
// was actually sent.
func TestLogRequest_SmallBodyCapturedExactly(t *testing.T) {
	dir := t.TempDir()
	rl, err := NewRequestLogger(dir, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()

	const body = `{"user":"john","token":"secret"}`
	req, err := http.NewRequest("POST", "https://api.example.com/users", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	entry, captured := rl.LogRequest(req)

	if string(entry.RequestBody) != body {
		t.Errorf("captured %q, want %q", entry.RequestBody, body)
	}
	if string(captured) != body {
		t.Errorf("returned %q, want %q", captured, body)
	}
	if entry.RequestBodyTruncated {
		t.Error("req_body_truncated set for a body under the cap")
	}
	forwarded, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read forwarded body: %v", err)
	}
	if string(forwarded) != body {
		t.Errorf("forwarded %q, want %q", forwarded, body)
	}
}

// TestLogRequest_ZeroLimitCapturesNothing verifies an explicit
// max_log_body_bytes = 0 records no body at all and still forwards it whole.
func TestLogRequest_ZeroLimitCapturesNothing(t *testing.T) {
	dir := t.TempDir()
	rl, err := NewRequestLogger(dir, nil, false, nil, WithMaxBodyLogBytes(0))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()

	const body = "payload"
	req, err := http.NewRequest("POST", "https://api.example.com/users", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	entry, captured := rl.LogRequest(req)

	if len(entry.RequestBody) != 0 || len(captured) != 0 {
		t.Errorf("captured %q with a zero limit, want nothing", entry.RequestBody)
	}
	if !entry.RequestBodyTruncated {
		t.Error("req_body_truncated not set: the entry must not read as an empty body")
	}
	forwarded, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read forwarded body: %v", err)
	}
	if string(forwarded) != body {
		t.Errorf("forwarded %q, want %q", forwarded, body)
	}
}

// TestCaptureBody_BoundsInMemoryBodies covers the path redaction uses when it
// replaces an already-captured body with its rewritten copy.
func TestCaptureBody_BoundsInMemoryBodies(t *testing.T) {
	dir := t.TempDir()
	rl, err := NewRequestLogger(dir, nil, false, nil, WithMaxBodyLogBytes(8))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rl.Close() }()

	short, truncated := rl.CaptureBody([]byte("abc"))
	if string(short) != "abc" || truncated {
		t.Errorf("CaptureBody(short) = %q, %v; want %q, false", short, truncated, "abc")
	}

	long, truncated := rl.CaptureBody([]byte(strings.Repeat("z", 64)))
	if len(long) != 8 || !truncated {
		t.Errorf("CaptureBody(long) = %d bytes, %v; want 8, true", len(long), truncated)
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
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

// TestCaptureHeaders_BoundsOversizedSet covers the log-line budget's remaining
// hole: the bodies are capped, but a sandbox-chosen upstream can answer with
// megabytes of headers, and Go's transport accepts up to 10 MiB of them.
func TestCaptureHeaders_BoundsOversizedSet(t *testing.T) {
	h := http.Header{
		"Content-Type":  []string{"application/json"},
		"X-Flood":       []string{strings.Repeat("z", maxLogHeaderBytes+1)},
		"X-Small":       []string{"kept"},
		"Authorization": []string{"Bearer secret"},
	}

	got, truncated := captureHeaders(h)
	if !truncated {
		t.Error("truncated = false, want true for a header set past the budget")
	}
	if _, ok := got["X-Flood"]; ok {
		t.Error("the oversized header was recorded")
	}
	if v := got["X-Small"]; len(v) != 1 || v[0] != "kept" {
		t.Errorf("X-Small = %v, want it kept: one oversized header must cost only itself", v)
	}
	if v := got["Content-Type"]; len(v) != 1 || v[0] != "application/json" {
		t.Errorf("Content-Type = %v, want it kept", v)
	}
	if v := got["Authorization"]; len(v) != 1 || v[0] != redactedHeaderValue {
		t.Errorf("Authorization = %v, want %q", v, redactedHeaderValue)
	}

	total := 0
	for k, vals := range got {
		total += len(k)
		for _, v := range vals {
			total += len(v)
		}
	}
	if total > maxLogHeaderBytes {
		t.Errorf("recorded %d bytes of headers, want <= %d", total, maxLogHeaderBytes)
	}
}

// TestCaptureHeaders_TruncationIsDeterministic pins the sorted-key walk: map
// iteration order would make the surviving subset differ per run.
func TestCaptureHeaders_TruncationIsDeterministic(t *testing.T) {
	h := http.Header{}
	for i := range 40 {
		h[fmt.Sprintf("X-Header-%02d", i)] = []string{strings.Repeat("v", 4096)}
	}

	first, _ := captureHeaders(h)
	for range 20 {
		got, _ := captureHeaders(h)
		if !maps.EqualFunc(first, got, slices.Equal) {
			t.Fatal("captureHeaders recorded a different subset on a repeat call")
		}
	}
}

// TestCaptureHeaders_OrdinarySetUntouched is the counterweight: normal headers
// must survive whole, since the bound is for the pathological case only.
func TestCaptureHeaders_OrdinarySetUntouched(t *testing.T) {
	h := http.Header{
		"Content-Type": []string{"application/json"},
		"Accept":       []string{"application/json", "text/plain"},
		"Cookie":       []string{"session=abc"},
	}

	got, truncated := captureHeaders(h)
	if truncated {
		t.Error("truncated = true for an ordinary header set")
	}
	if len(got) != len(h) {
		t.Errorf("recorded %d headers, want %d", len(got), len(h))
	}
	if v := got["Accept"]; !slices.Equal(v, []string{"application/json", "text/plain"}) {
		t.Errorf("Accept = %v, want both values", v)
	}
	if v := got["Cookie"]; len(v) != 1 || v[0] != redactedHeaderValue {
		t.Errorf("Cookie = %v, want %q", v, redactedHeaderValue)
	}
	if got, want := len(h["Cookie"]), 1; got != want || h["Cookie"][0] != "session=abc" {
		t.Error("captureHeaders mutated the caller's header set")
	}
}

// TestLogResponse_BoundsHeaders drives the bound through the entry a response
// produces, which is the direction a sandbox-chosen upstream controls.
func TestLogResponse_BoundsHeaders(t *testing.T) {
	rl := &RequestLogger{maxBodyBytes: 1024}

	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"X-Flood": []string{strings.Repeat("z", maxLogHeaderBytes+1)}},
		Body:       io.NopCloser(strings.NewReader("ok")),
	}
	entry := &RequestLog{}
	rl.LogResponse(entry, resp, time.Now())

	if !entry.ResponseHeadersTruncated {
		t.Error("resp_headers_truncated = false, want true")
	}
	if _, ok := entry.ResponseHeaders["X-Flood"]; ok {
		t.Error("the oversized response header was recorded")
	}
}
