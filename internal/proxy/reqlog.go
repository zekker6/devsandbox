package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"devsandbox/internal/logging"
)

const (
	RequestLogPrefix        = "requests"
	RequestLogSuffix        = ".jsonl"    // Active file (uncompressed for tailing)
	RequestLogArchiveSuffix = ".jsonl.gz" // Rotated files (compressed)
)

// RequestLog represents a logged HTTP request/response pair
type RequestLog struct {
	Timestamp        time.Time           `json:"ts"`
	Method           string              `json:"method"`
	URL              string              `json:"url"`
	RequestHeaders   map[string][]string `json:"req_headers,omitempty"`
	RequestBody      []byte              `json:"req_body,omitempty"`
	StatusCode       int                 `json:"status,omitempty"`
	ResponseHeaders  map[string][]string `json:"resp_headers,omitempty"`
	ResponseBody     []byte              `json:"resp_body,omitempty"`
	Duration         time.Duration       `json:"duration_ns,omitempty"`
	Error            string              `json:"error,omitempty"`
	FilterAction     string              `json:"filter_action,omitempty"`
	FilterReason     string              `json:"filter_reason,omitempty"`
	RedactionAction  string              `json:"redaction_action,omitempty"`
	RedactionMatches []string            `json:"redaction_matches,omitempty"`
}

// RequestLogger writes HTTP request/response logs to rotating gzip-compressed files
// and optionally forwards them to remote destinations.
type RequestLogger struct {
	writer         *RotatingFileWriter
	dispatcher     *logging.Dispatcher
	ownsDispatcher bool // true if this logger created/owns the dispatcher
	skipEngine     *LogSkipEngine
	requestCount   atomic.Int64
	mu             sync.Mutex
}

// NewRequestLogger creates a new request logger.
// If dispatcher is provided, logs will also be forwarded to remote destinations.
// If ownsDispatcher is true, the dispatcher will be closed when the logger is closed.
// If skipEngine is non-nil, entries matching its rules are dropped before any I/O.
func NewRequestLogger(dir string, dispatcher *logging.Dispatcher, ownsDispatcher bool, skipEngine *LogSkipEngine) (*RequestLogger, error) {
	writer, err := NewRotatingFileWriter(RotatingFileWriterConfig{
		Dir:           dir,
		Prefix:        RequestLogPrefix,
		Suffix:        RequestLogSuffix,
		ArchiveSuffix: RequestLogArchiveSuffix,
	})
	if err != nil {
		return nil, err
	}

	return &RequestLogger{
		writer:         writer,
		dispatcher:     dispatcher,
		ownsDispatcher: ownsDispatcher,
		skipEngine:     skipEngine,
	}, nil
}

// RequestCount returns the count of non-skipped Log calls handled by this
// logger. Used by session.end audit events.
func (rl *RequestLogger) RequestCount() int64 {
	return rl.requestCount.Load()
}

// Log writes a request/response pair to the log and forwards to remote destinations.
// Entries matching the skip engine are dropped: no file write, no dispatcher forward.
func (rl *RequestLogger) Log(entry *RequestLog) error {
	if rl.skipEngine != nil && rl.skipEngine.ShouldSkip(entry) {
		return nil
	}
	rl.requestCount.Add(1)

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	// Write to local file (protected by lock)
	rl.mu.Lock()
	_, writeErr := rl.writer.Write(data)
	rl.mu.Unlock()

	// Forward to remote destinations outside the lock to prevent blocking
	// on slow network I/O (syslog, OTLP, etc.)
	if rl.dispatcher != nil && rl.dispatcher.HasWriters() {
		logEntry := rl.toLogEntry(entry)
		_ = rl.dispatcher.Write(logEntry) // Don't fail on remote errors
	}

	return writeErr
}

// toLogEntry converts a RequestLog to a logging.Entry for remote forwarding.
func (rl *RequestLogger) toLogEntry(req *RequestLog) *logging.Entry {
	level := logging.LevelInfo
	if req.Error != "" {
		level = logging.LevelError
	} else if req.FilterAction == "block" {
		level = logging.LevelWarn
	} else if req.RedactionAction == "block" {
		level = logging.LevelWarn
	} else if req.StatusCode >= 400 {
		level = logging.LevelWarn
	}

	fields := map[string]any{
		"method":      req.Method,
		"url":         req.URL,
		"status":      req.StatusCode,
		"duration_ms": req.Duration.Milliseconds(),
		"error":       req.Error,
	}

	// Add filter fields if present
	if req.FilterAction != "" {
		fields["filter_action"] = req.FilterAction
	}
	if req.FilterReason != "" {
		fields["filter_reason"] = req.FilterReason
	}

	// Add redaction fields if present
	if req.RedactionAction != "" {
		fields["redaction_action"] = req.RedactionAction
	}
	if len(req.RedactionMatches) > 0 {
		fields["redaction_matches"] = req.RedactionMatches
	}

	return &logging.Entry{
		Timestamp: req.Timestamp,
		Level:     level,
		Message:   fmt.Sprintf("%s %s %d", req.Method, req.URL, req.StatusCode),
		Fields:    fields,
	}
}

// LogRequest captures request details and returns a log entry
func (rl *RequestLogger) LogRequest(req *http.Request) (*RequestLog, []byte) {
	// req.URL can be nil when goproxy's HTTPS handler fails to re-parse a
	// malformed request line - the parse error is swallowed and the request
	// is still dispatched. Fall back to RequestURI so logging never panics.
	// https://github.com/elazarl/goproxy/blob/v1.8.3/https.go#L272-L274
	urlStr := ""
	if req.URL != nil {
		urlStr = req.URL.String()
	} else {
		urlStr = req.RequestURI
	}
	entry := &RequestLog{
		Timestamp:      time.Now(),
		Method:         req.Method,
		URL:            urlStr,
		RequestHeaders: redactHeaders(cloneHeaders(req.Header)),
	}

	// Read and restore request body
	var reqBody []byte
	if req.Body != nil {
		reqBody, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
		entry.RequestBody = reqBody
	}

	return entry, reqBody
}

// LogResponse completes the log entry with response details
func (rl *RequestLogger) LogResponse(entry *RequestLog, resp *http.Response, startTime time.Time) []byte {
	entry.Duration = time.Since(startTime)

	if resp == nil {
		entry.Error = "no response"
		return nil
	}

	entry.StatusCode = resp.StatusCode
	entry.ResponseHeaders = redactHeaders(cloneHeaders(resp.Header))

	// HEAD responses must preserve their upstream Content-Length verbatim
	// (RFC 9110 §9.3.2). Replacing resp.Body — even with an empty reader —
	// causes goproxy to detect the body identity changed and strip the
	// Content-Length header (see goproxy http.go), after which Go's net/http
	// falls back to Transfer-Encoding: chunked. That breaks OCI/registry
	// clients (oras-go, crane, helm, BuildKit, skopeo) which use HEAD for
	// manifest size validation. The HEAD body is empty by spec, so there is
	// nothing to capture for logging anyway.
	if resp.Request != nil && resp.Request.Method == http.MethodHead {
		return nil
	}

	// Streaming responses (Server-Sent Events, newline-delimited JSON) must not
	// be buffered. goproxy relays a response to the client only after the
	// OnResponse handler returns, so io.ReadAll on a long-lived stream blocks
	// until the upstream closes it. The client never receives the response
	// headers and aborts (e.g. codex/OpenAI: "Codex SSE response headers timed
	// out after 20000ms"). Pass the body through untouched and log metadata
	// (status + headers) only; the streamed body is unbounded and not useful to
	// capture in full anyway.
	if isStreamingResponse(resp) {
		return nil
	}

	// Read and restore response body
	var respBody []byte
	if resp.Body != nil {
		respBody, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		entry.ResponseBody = respBody
	}

	return respBody
}

// maxResponseLogBytes caps how much of a response body is captured for logging.
// The full body always streams to the client; only this leading prefix is
// recorded in the log entry, bounding memory for large or long-lived streams.
const maxResponseLogBytes = 256 * 1024

// LogResponseStreaming records response status and headers immediately, then
// arranges for the body to be captured for logging WITHOUT buffering it first.
//
// goproxy relays a response to the client only after the OnResponse handler
// returns, and it does not flush the body until the handler-supplied resp.Body
// is read. Reading the whole body here (io.ReadAll) therefore withholds the
// response *headers* from the client until the body completes. For streaming
// responses that stay open for seconds to minutes (SSE, chunked, HTTP
// upgrades) this is fatal: codex aborts with "SSE response headers timed out
// after 20000ms" while the proxy spends 10-80s reading the stream. Crucially,
// such responses are not always identifiable by Content-Type - codex's
// streamed responses carry an empty Content-Type - so buffering cannot be
// avoided by media-type sniffing alone.
//
// The body is therefore wrapped so it streams to the client unchanged while a
// bounded prefix is captured; the log entry is written when the body is closed
// (stream end or client disconnect). Responses with no streamable body are
// logged immediately and left untouched: HEAD (whose upstream Content-Length
// must be preserved verbatim per RFC 9110 §9.3.2 - replacing the body makes
// goproxy strip Content-Length and switch to chunked, breaking OCI/registry
// clients), 1xx informational/upgrade responses, and empty bodies.
func (rl *RequestLogger) LogResponseStreaming(entry *RequestLog, resp *http.Response, startTime time.Time) {
	entry.Duration = time.Since(startTime)

	if resp == nil {
		entry.Error = "no response"
		_ = rl.Log(entry)
		return
	}

	entry.StatusCode = resp.StatusCode
	entry.ResponseHeaders = redactHeaders(cloneHeaders(resp.Header))

	isHead := resp.Request != nil && resp.Request.Method == http.MethodHead
	if isHead || resp.StatusCode < http.StatusOK || resp.Body == nil || resp.Body == http.NoBody {
		_ = rl.Log(entry)
		return
	}

	resp.Body = &captureBody{
		src:       resp.Body,
		remaining: maxResponseLogBytes,
		entry:     entry,
		logger:    rl,
	}
}

// captureBody wraps an upstream response body so it streams to the consumer
// (goproxy, and thus the client) unchanged while a bounded prefix is captured
// for logging. The log entry is finalized exactly once, when the body reaches
// EOF or is closed, so the proxy never buffers the full body before relaying
// response headers.
type captureBody struct {
	src       io.ReadCloser
	buf       bytes.Buffer
	remaining int
	entry     *RequestLog
	logger    *RequestLogger
	logOnce   sync.Once
}

func (c *captureBody) Read(p []byte) (int, error) {
	n, err := c.src.Read(p)
	if n > 0 && c.remaining > 0 {
		take := min(n, c.remaining)
		c.buf.Write(p[:take])
		c.remaining -= take
	}
	if err == io.EOF {
		c.finalize()
	}
	return n, err
}

func (c *captureBody) Close() error {
	err := c.src.Close()
	c.finalize()
	return err
}

func (c *captureBody) finalize() {
	c.logOnce.Do(func() {
		c.entry.ResponseBody = c.buf.Bytes()
		_ = c.logger.Log(c.entry)
	})
}

// Close closes the logger and flushes remote destinations.
// The dispatcher is only closed if this logger owns it.
func (rl *RequestLogger) Close() error {
	if rl.dispatcher != nil && rl.ownsDispatcher {
		_ = rl.dispatcher.Close()
	}
	return rl.writer.Close()
}

// streamingContentTypes are response media types that represent long-lived
// streams whose bodies must never be buffered by the proxy. See LogResponse.
var streamingContentTypes = map[string]bool{
	"text/event-stream":    true, // Server-Sent Events (OpenAI, Anthropic, codex, claude)
	"application/x-ndjson": true, // newline-delimited JSON streaming (Ollama, etc.)
}

// isStreamingResponse reports whether the response is a streaming protocol that
// must be relayed incrementally rather than read to completion. Detection is by
// Content-Type media type, ignoring any parameters (e.g. "; charset=utf-8").
func isStreamingResponse(resp *http.Response) bool {
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		return false
	}
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return streamingContentTypes[strings.ToLower(strings.TrimSpace(ct))]
}

var sensitiveHeaders = map[string]bool{
	"Authorization":       true,
	"Cookie":              true,
	"Set-Cookie":          true,
	"X-Api-Key":           true,
	"X-Auth-Token":        true,
	"Proxy-Authorization": true,
}

func redactHeaders(headers map[string][]string) map[string][]string {
	if headers == nil {
		return nil
	}
	redacted := make(map[string][]string, len(headers))
	for k, v := range headers {
		if sensitiveHeaders[http.CanonicalHeaderKey(k)] {
			redacted[k] = []string{"[REDACTED]"}
		} else {
			redacted[k] = v
		}
	}
	return redacted
}

func cloneHeaders(h http.Header) map[string][]string {
	if h == nil {
		return nil
	}
	clone := make(map[string][]string, len(h))
	for k, v := range h {
		clone[k] = append([]string(nil), v...)
	}
	return clone
}
