package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"devsandbox/internal/config"
	"devsandbox/internal/logging"
)

const (
	RequestLogPrefix        = "requests"
	RequestLogSuffix        = ".jsonl"    // Active file (uncompressed for tailing)
	RequestLogArchiveSuffix = ".jsonl.gz" // Rotated files (compressed)
)

// RequestLog represents a logged HTTP request/response pair.
//
// The body fields hold a bounded prefix of what was sent, not necessarily the
// whole body - see RequestLogger.maxBodyBytes. The matching Truncated flag
// distinguishes a body that was cut from one that was short.
type RequestLog struct {
	Timestamp                time.Time           `json:"ts"`
	Method                   string              `json:"method"`
	URL                      string              `json:"url"`
	RequestHeaders           map[string][]string `json:"req_headers,omitempty"`
	RequestHeadersTruncated  bool                `json:"req_headers_truncated,omitempty"`
	RequestBody              []byte              `json:"req_body,omitempty"`
	RequestBodyTruncated     bool                `json:"req_body_truncated,omitempty"`
	StatusCode               int                 `json:"status,omitempty"`
	ResponseHeaders          map[string][]string `json:"resp_headers,omitempty"`
	ResponseHeadersTruncated bool                `json:"resp_headers_truncated,omitempty"`
	ResponseBody             []byte              `json:"resp_body,omitempty"`
	ResponseBodyTruncated    bool                `json:"resp_body_truncated,omitempty"`
	Duration                 time.Duration       `json:"duration_ns,omitempty"`
	Error                    string              `json:"error,omitempty"`
	FilterAction             string              `json:"filter_action,omitempty"`
	FilterReason             string              `json:"filter_reason,omitempty"`
	RedactionAction          string              `json:"redaction_action,omitempty"`
	RedactionMatches         []string            `json:"redaction_matches,omitempty"`
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

	// maxBodyBytes bounds what a log entry records of a body; the body itself
	// is always forwarded whole. bodyCaptureTimeout bounds how long a request
	// capture may hold the handler.
	maxBodyBytes       int
	bodyCaptureTimeout time.Duration
}

// RequestLoggerOption configures optional RequestLogger behavior.
type RequestLoggerOption func(*RequestLogger)

// WithMaxBodyLogBytes bounds how many bytes of a request or response body are
// recorded in a log entry. The full body always reaches its destination; only
// the recorded copy is bounded. Zero records no bodies at all; a negative
// value is treated as zero.
func WithMaxBodyLogBytes(n int) RequestLoggerOption {
	return func(rl *RequestLogger) { rl.maxBodyBytes = max(n, 0) }
}

// NewRequestLogger creates a new request logger.
// If dispatcher is provided, logs will also be forwarded to remote destinations.
// If ownsDispatcher is true, the dispatcher will be closed when the logger is closed.
// If skipEngine is non-nil, entries matching its rules are dropped before any I/O.
func NewRequestLogger(dir string, dispatcher *logging.Dispatcher, ownsDispatcher bool, skipEngine *LogSkipEngine, opts ...RequestLoggerOption) (*RequestLogger, error) {
	writer, err := NewRotatingFileWriter(RotatingFileWriterConfig{
		Dir:           dir,
		Prefix:        RequestLogPrefix,
		Suffix:        RequestLogSuffix,
		ArchiveSuffix: RequestLogArchiveSuffix,
	})
	if err != nil {
		return nil, err
	}

	rl := &RequestLogger{
		writer:             writer,
		dispatcher:         dispatcher,
		ownsDispatcher:     ownsDispatcher,
		skipEngine:         skipEngine,
		maxBodyBytes:       config.DefaultMaxLogBodyBytes,
		bodyCaptureTimeout: defaultBodyCaptureTimeout,
	}
	for _, opt := range opts {
		opt(rl)
	}
	return rl, nil
}

// CaptureBody bounds a body already held in memory, reporting whether anything
// was dropped. Every path that fills a body field of a log entry goes through
// here or through the streaming captures, so the bound holds for all of them -
// redaction, which replaces a captured body with its rewritten copy, included.
func (rl *RequestLogger) CaptureBody(body []byte) ([]byte, bool) {
	if len(body) <= rl.maxBodyBytes {
		return body, false
	}
	return body[:rl.maxBodyBytes], true
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
		Timestamp: time.Now(),
		Method:    req.Method,
		URL:       urlStr,
	}
	entry.RequestHeaders, entry.RequestHeadersTruncated = captureHeaders(req.Header)

	// Capture a bounded prefix of the request body rather than buffering it
	// whole. LogRequest runs before any filter decision, so an unbounded
	// io.ReadAll here lets a sandboxed client exhaust host memory - the proxy
	// runs outside the sandbox's resource limits - with a body policy would
	// have refused, and a body that never ends holds the handler forever. What
	// the capture read is replayed ahead of the unread remainder, so upstream
	// still receives the body intact.
	var reqBody []byte
	if req.Body != nil {
		capture := newBodyCapture(req.Body, rl.maxBodyBytes)
		reqBody, entry.RequestBodyTruncated = capture.wait(req.Context(), rl.bodyCaptureTimeout)
		entry.RequestBody = reqBody
		req.Body = capture.body()
	}

	return entry, reqBody
}

// defaultBodyCaptureTimeout bounds how long LogRequest waits for a request
// body's captured prefix. A client that trickles or stalls its body must not
// hold the handler: past this the entry records what arrived, marked
// truncated, and the body streams on at whatever pace the client manages.
const defaultBodyCaptureTimeout = 5 * time.Second

// bodyCapture reads a bounded prefix of a request body in the background, so a
// slow or stalled client cannot hold the proxy handler. Everything it reads is
// replayed ahead of the unread remainder, so the body forwarded upstream stays
// byte-identical to the one the client sent.
type bodyCapture struct {
	src   io.ReadCloser
	limit int
	done  chan struct{}

	mu  sync.Mutex
	buf bytes.Buffer
}

func newBodyCapture(src io.ReadCloser, limit int) *bodyCapture {
	c := &bodyCapture{src: src, limit: limit, done: make(chan struct{})}
	go c.run()
	return c
}

// run reads up to limit+1 bytes. The extra byte is what tells a body that
// exactly fills the limit apart from one that exceeds it, and is why a zero
// limit can still report truncation rather than an empty body.
func (c *bodyCapture) run() {
	defer close(c.done)
	_, _ = io.Copy(c, io.LimitReader(c.src, int64(c.limit)+1))
}

// Write accumulates captured bytes. It is the io.Copy destination in run and
// is locked because wait snapshots the buffer while that copy is in flight.
func (c *bodyCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

// read returns a copy of everything captured so far.
func (c *bodyCapture) read() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.buf.Bytes())
}

// wait blocks until the capture completes, the request is canceled, or the
// timeout elapses, and returns the captured prefix and whether it is short of
// the body. Returning early abandons nothing: body() replays whatever the
// capture reads whenever it finishes.
func (c *bodyCapture) wait(ctx context.Context, timeout time.Duration) ([]byte, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-c.done:
		captured := c.read()
		if len(captured) > c.limit {
			return captured[:c.limit], true
		}
		return captured, false
	case <-ctx.Done():
	case <-timer.C:
	}

	// Still reading: whatever it has is a prefix by definition.
	captured := c.read()
	if len(captured) > c.limit {
		captured = captured[:c.limit]
	}
	return captured, true
}

// body returns the request body to forward: everything the capture read,
// followed by the unread remainder. Reading it waits for a capture still in
// flight, which is the pace the client itself set - the handler was already
// released by wait.
func (c *bodyCapture) body() io.ReadCloser { return &captureReplay{capture: c} }

type captureReplay struct {
	capture *bodyCapture
	rest    io.Reader
}

func (r *captureReplay) Read(p []byte) (int, error) {
	if r.rest == nil {
		<-r.capture.done
		r.rest = io.MultiReader(bytes.NewReader(r.capture.read()), r.capture.src)
	}
	return r.rest.Read(p)
}

// Close closes the original body. Doing so while the capture is mid-read is
// deliberate: it is what unblocks a capture waiting on a client that went away.
func (r *captureReplay) Close() error { return r.capture.src.Close() }

// LogResponse completes the log entry with response details
func (rl *RequestLogger) LogResponse(entry *RequestLog, resp *http.Response, startTime time.Time) []byte {
	entry.Duration = time.Since(startTime)

	if resp == nil {
		entry.Error = "no response"
		return nil
	}

	entry.StatusCode = resp.StatusCode
	entry.ResponseHeaders, entry.ResponseHeadersTruncated = captureHeaders(resp.Header)

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
		entry.ResponseBody, entry.ResponseBodyTruncated = rl.CaptureBody(respBody)
	}

	return respBody
}

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
	entry.ResponseHeaders, entry.ResponseHeadersTruncated = captureHeaders(resp.Header)

	isHead := resp.Request != nil && resp.Request.Method == http.MethodHead
	if isHead || resp.StatusCode < http.StatusOK || resp.Body == nil || resp.Body == http.NoBody {
		_ = rl.Log(entry)
		return
	}

	resp.Body = &captureBody{
		src:       resp.Body,
		remaining: rl.maxBodyBytes,
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
	truncated bool
	entry     *RequestLog
	logger    *RequestLogger
	logOnce   sync.Once
}

func (c *captureBody) Read(p []byte) (int, error) {
	n, err := c.src.Read(p)
	if n > 0 {
		take := min(n, c.remaining)
		if take > 0 {
			c.buf.Write(p[:take])
			c.remaining -= take
		}
		if take < n {
			c.truncated = true
		}
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
		c.entry.ResponseBodyTruncated = c.truncated
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

const redactedHeaderValue = "[REDACTED]"

var sensitiveHeaders = map[string]bool{
	"Authorization":       true,
	"Cookie":              true,
	"Set-Cookie":          true,
	"X-Api-Key":           true,
	"X-Auth-Token":        true,
	"Proxy-Authorization": true,
}

// maxLogHeaderBytes bounds what one log entry records of a single header set.
//
// The bodies are already bounded, and config.MaxLogBodyBytesLimit is set so
// that two of them plus the entry's own fields stay under the 8 MiB line the
// reader accepts. Headers were the hole left in that arithmetic: the sandbox
// chooses which host a request reaches, so an upstream it controls can answer
// with megabytes of response headers, and Go's transport accepts up to 10 MiB
// of them. That produced a record the reader refuses and, before the reader
// learned to step over one, cost every later entry in the same file. It is
// also a log-flooding lever: a handful of such responses fills the whole
// rotation and pushes real entries out.
const maxLogHeaderBytes = 64 * 1024

// captureHeaders clones, redacts and bounds a header set for a log entry,
// reporting whether anything was dropped.
//
// Keys are taken in sorted order and one that does not fit is skipped rather
// than ending the walk, so a single oversized header costs only itself and the
// recorded subset is the same on every run - map iteration order would make it
// arbitrary.
func captureHeaders(h map[string][]string) (map[string][]string, bool) {
	if h == nil {
		return nil, false
	}
	out := make(map[string][]string, len(h))
	used := 0
	truncated := false
	for _, k := range slices.Sorted(maps.Keys(h)) {
		vals := h[k]
		if sensitiveHeaders[http.CanonicalHeaderKey(k)] {
			vals = []string{redactedHeaderValue}
		}
		size := len(k)
		for _, v := range vals {
			size += len(v)
		}
		if used+size > maxLogHeaderBytes {
			truncated = true
			continue
		}
		used += size
		out[k] = slices.Clone(vals)
	}
	return out, truncated
}
