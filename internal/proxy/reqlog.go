package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
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
	mu             sync.Mutex
}

// NewRequestLogger creates a new request logger.
// If dispatcher is provided, logs will also be forwarded to remote destinations.
// If ownsDispatcher is true, the dispatcher will be closed when the logger is closed.
func NewRequestLogger(dir string, dispatcher *logging.Dispatcher, ownsDispatcher bool) (*RequestLogger, error) {
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
	}, nil
}

// Log writes a request/response pair to the log and forwards to remote destinations.
func (rl *RequestLogger) Log(entry *RequestLog) error {
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
	entry := &RequestLog{
		Timestamp:      time.Now(),
		Method:         req.Method,
		URL:            req.URL.String(),
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

// Close closes the logger and flushes remote destinations.
// The dispatcher is only closed if this logger owns it.
func (rl *RequestLogger) Close() error {
	if rl.dispatcher != nil && rl.ownsDispatcher {
		_ = rl.dispatcher.Close()
	}
	return rl.writer.Close()
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
