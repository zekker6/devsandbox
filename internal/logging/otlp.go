package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	collectorlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	"devsandbox/internal/version"
)

// OTLPConfig contains OpenTelemetry writer configuration.
type OTLPConfig struct {
	// Endpoint is the OTLP endpoint.
	// For HTTP: "http://localhost:4318/v1/logs"
	// For gRPC: "localhost:4317"
	Endpoint string

	// Protocol is "http" or "grpc" (default: http).
	Protocol string

	// Headers are custom headers (for authentication, etc.).
	Headers map[string]string

	// BatchSize is the number of entries before automatic flush.
	BatchSize int

	// FlushInterval is the maximum time before flushing buffered entries.
	FlushInterval time.Duration

	// Timeout is the request timeout.
	Timeout time.Duration

	// Insecure disables TLS for gRPC connections.
	Insecure bool

	// ResourceAttributes are custom attributes added to the resource.
	ResourceAttributes map[string]string

	// ErrorLogger logs internal errors to a file (optional).
	ErrorLogger *ErrorLogger
}

// OTLPWriter sends logs to an OpenTelemetry collector.
type OTLPWriter struct {
	cfg         OTLPConfig
	httpClient  *http.Client
	grpcConn    *grpc.ClientConn
	grpcClient  collectorlogs.LogsServiceClient
	errorLogger *ErrorLogger
	buffer      []*Entry
	mu          sync.Mutex
	done        chan struct{}
	wg          sync.WaitGroup
	closing     bool
}

// NewOTLPWriter creates a new OTLP writer.
func NewOTLPWriter(cfg OTLPConfig) (*OTLPWriter, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("OTLP endpoint is required")
	}

	// Apply defaults
	if cfg.Protocol == "" {
		cfg.Protocol = "http"
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 1 * time.Second
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}

	w := &OTLPWriter{
		cfg:         cfg,
		errorLogger: cfg.ErrorLogger,
		buffer:      make([]*Entry, 0, cfg.BatchSize),
		done:        make(chan struct{}),
	}

	// Initialize transport
	switch cfg.Protocol {
	case "grpc":
		if err := w.initGRPC(); err != nil {
			return nil, fmt.Errorf("failed to initialize gRPC: %w", err)
		}
	case "http":
		w.httpClient = &http.Client{Timeout: cfg.Timeout}
	default:
		return nil, fmt.Errorf("unsupported protocol: %s (use 'http' or 'grpc')", cfg.Protocol)
	}

	// Start background flusher
	w.wg.Add(1)
	go w.flushLoop()

	return w, nil
}

func (w *OTLPWriter) initGRPC() error {
	var opts []grpc.DialOption

	if w.cfg.Insecure {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	conn, err := grpc.NewClient(w.cfg.Endpoint, opts...)
	if err != nil {
		return err
	}

	w.grpcConn = conn
	w.grpcClient = collectorlogs.NewLogsServiceClient(conn)
	return nil
}

// Write buffers a log entry for batched sending.
func (w *OTLPWriter) Write(entry *Entry) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closing {
		return fmt.Errorf("writer is closing")
	}

	w.buffer = append(w.buffer, entry)

	// Flush if batch is full
	if len(w.buffer) >= w.cfg.BatchSize {
		return w.flushLocked()
	}

	return nil
}

// Close flushes remaining entries and stops the writer.
func (w *OTLPWriter) Close() error {
	w.mu.Lock()
	w.closing = true
	w.mu.Unlock()

	// Signal flush loop to stop
	close(w.done)
	w.wg.Wait()

	// Final flush
	w.mu.Lock()
	_ = w.flushLocked()
	w.mu.Unlock()

	// Close gRPC connection
	if w.grpcConn != nil {
		return w.grpcConn.Close()
	}

	return nil
}

func (w *OTLPWriter) flushLoop() {
	defer w.wg.Done()

	ticker := time.NewTicker(w.cfg.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			w.mu.Lock()
			_ = w.flushLocked()
			w.mu.Unlock()
		case <-w.done:
			return
		}
	}
}

func (w *OTLPWriter) flushLocked() error {
	if len(w.buffer) == 0 {
		return nil
	}

	entries := w.buffer
	w.buffer = make([]*Entry, 0, w.cfg.BatchSize)

	// Send asynchronously to not block the caller
	go w.send(entries)

	return nil
}

func (w *OTLPWriter) send(entries []*Entry) {
	switch w.cfg.Protocol {
	case "grpc":
		w.sendGRPC(entries)
	default:
		w.sendHTTP(entries)
	}
}

func (w *OTLPWriter) sendHTTP(entries []*Entry) {
	payload := w.buildJSONPayload(entries)

	ctx, cancel := context.WithTimeout(context.Background(), w.cfg.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.cfg.Endpoint, bytes.NewReader(payload))
	if err != nil {
		w.errorLogger.LogErrorf("otlp-http", "failed to create request to %s: %v", w.cfg.Endpoint, err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	for k, v := range w.cfg.Headers {
		req.Header.Set(k, v)
	}

	resp, err := w.httpClient.Do(req)
	if err != nil {
		w.errorLogger.LogErrorf("otlp-http", "failed to send %d entries to %s: %v", len(entries), w.cfg.Endpoint, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		w.errorLogger.LogErrorf("otlp-http", "server %s returned status %d for %d entries", w.cfg.Endpoint, resp.StatusCode, len(entries))
	}
}

func (w *OTLPWriter) sendGRPC(entries []*Entry) {
	request := w.buildProtoRequest(entries)

	ctx, cancel := context.WithTimeout(context.Background(), w.cfg.Timeout)
	defer cancel()

	// Add headers as metadata
	if len(w.cfg.Headers) > 0 {
		md := metadata.New(w.cfg.Headers)
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	_, err := w.grpcClient.Export(ctx, request)
	if err != nil {
		w.errorLogger.LogErrorf("otlp-grpc", "failed to export %d entries to %s: %v", len(entries), w.cfg.Endpoint, err)
	}
}

func (w *OTLPWriter) buildResourceAttributes() []*commonpb.KeyValue {
	attrs := []*commonpb.KeyValue{
		{
			Key:   "service.name",
			Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "devsandbox"}},
		},
		{
			Key:   "service.version",
			Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: version.Version}},
		},
		{
			Key:   "service.commit",
			Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: version.Commit}},
		},
	}

	// Add custom resource attributes
	for k, v := range w.cfg.ResourceAttributes {
		attrs = append(attrs, &commonpb.KeyValue{
			Key:   k,
			Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}},
		})
	}

	return attrs
}

func (w *OTLPWriter) buildProtoRequest(entries []*Entry) *collectorlogs.ExportLogsServiceRequest {
	logRecords := make([]*logspb.LogRecord, 0, len(entries))

	for _, e := range entries {
		record := &logspb.LogRecord{
			TimeUnixNano:         uint64(e.Timestamp.UnixNano()),
			ObservedTimeUnixNano: uint64(time.Now().UnixNano()),
			SeverityNumber:       levelToSeverityNumber(e.Level),
			SeverityText:         string(e.Level),
			Body:                 &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: e.Message}},
			Attributes:           fieldsToProtoAttributes(e.Fields),
		}
		logRecords = append(logRecords, record)
	}

	return &collectorlogs.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{
			{
				Resource: &resourcepb.Resource{
					Attributes: w.buildResourceAttributes(),
				},
				ScopeLogs: []*logspb.ScopeLogs{
					{
						Scope: &commonpb.InstrumentationScope{
							Name:    "devsandbox.proxy",
							Version: version.Version,
						},
						LogRecords: logRecords,
					},
				},
			},
		},
	}
}

func (w *OTLPWriter) buildJSONPayload(entries []*Entry) []byte {
	logRecords := make([]otlpLogRecord, 0, len(entries))

	for _, e := range entries {
		record := otlpLogRecord{
			TimeUnixNano:         uint64(e.Timestamp.UnixNano()),
			SeverityNumber:       int(levelToSeverityNumber(e.Level)),
			SeverityText:         string(e.Level),
			Body:                 otlpAnyValue{StringValue: e.Message},
			Attributes:           fieldsToJSONAttributes(e.Fields),
			ObservedTimeUnixNano: uint64(time.Now().UnixNano()),
		}
		logRecords = append(logRecords, record)
	}

	// Build resource attributes for JSON
	resourceAttrs := []otlpKeyValue{
		{Key: "service.name", Value: otlpAnyValue{StringValue: "devsandbox"}},
		{Key: "service.version", Value: otlpAnyValue{StringValue: version.Version}},
		{Key: "service.commit", Value: otlpAnyValue{StringValue: version.Commit}},
	}
	for k, v := range w.cfg.ResourceAttributes {
		resourceAttrs = append(resourceAttrs, otlpKeyValue{Key: k, Value: otlpAnyValue{StringValue: v}})
	}

	payload := otlpLogsPayload{
		ResourceLogs: []otlpResourceLogs{
			{
				Resource: otlpResource{
					Attributes: resourceAttrs,
				},
				ScopeLogs: []otlpScopeLogs{
					{
						Scope: otlpScope{
							Name:    "devsandbox.proxy",
							Version: version.Version,
						},
						LogRecords: logRecords,
					},
				},
			},
		},
	}

	data, _ := json.Marshal(payload)
	return data
}

func levelToSeverityNumber(level Level) logspb.SeverityNumber {
	switch level {
	case LevelDebug:
		return logspb.SeverityNumber_SEVERITY_NUMBER_DEBUG
	case LevelInfo:
		return logspb.SeverityNumber_SEVERITY_NUMBER_INFO
	case LevelWarn:
		return logspb.SeverityNumber_SEVERITY_NUMBER_WARN
	case LevelError:
		return logspb.SeverityNumber_SEVERITY_NUMBER_ERROR
	default:
		return logspb.SeverityNumber_SEVERITY_NUMBER_INFO
	}
}

func fieldsToProtoAttributes(fields map[string]any) []*commonpb.KeyValue {
	if len(fields) == 0 {
		return nil
	}

	attrs := make([]*commonpb.KeyValue, 0, len(fields))
	for k, v := range fields {
		attr := &commonpb.KeyValue{Key: k}
		switch val := v.(type) {
		case string:
			attr.Value = &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: val}}
		case int:
			attr.Value = &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: int64(val)}}
		case int64:
			attr.Value = &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: val}}
		case int32:
			attr.Value = &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: int64(val)}}
		case bool:
			attr.Value = &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: val}}
		case float64:
			attr.Value = &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: val}}
		default:
			attr.Value = &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: fmt.Sprintf("%v", v)}}
		}
		attrs = append(attrs, attr)
	}
	return attrs
}

func fieldsToJSONAttributes(fields map[string]any) []otlpKeyValue {
	if len(fields) == 0 {
		return nil
	}

	attrs := make([]otlpKeyValue, 0, len(fields))
	for k, v := range fields {
		attr := otlpKeyValue{Key: k}
		switch val := v.(type) {
		case string:
			attr.Value = otlpAnyValue{StringValue: val}
		case int:
			attr.Value = otlpAnyValue{IntValue: int64(val)}
		case int64:
			attr.Value = otlpAnyValue{IntValue: val}
		case int32:
			attr.Value = otlpAnyValue{IntValue: int64(val)}
		case bool:
			attr.Value = otlpAnyValue{BoolValue: val}
		default:
			attr.Value = otlpAnyValue{StringValue: fmt.Sprintf("%v", v)}
		}
		attrs = append(attrs, attr)
	}
	return attrs
}

// JSON structures for HTTP transport
type otlpLogsPayload struct {
	ResourceLogs []otlpResourceLogs `json:"resourceLogs"`
}

type otlpResourceLogs struct {
	Resource  otlpResource    `json:"resource"`
	ScopeLogs []otlpScopeLogs `json:"scopeLogs"`
}

type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes"`
}

type otlpScopeLogs struct {
	Scope      otlpScope       `json:"scope"`
	LogRecords []otlpLogRecord `json:"logRecords"`
}

type otlpScope struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type otlpLogRecord struct {
	TimeUnixNano         uint64         `json:"timeUnixNano,string"`
	ObservedTimeUnixNano uint64         `json:"observedTimeUnixNano,string"`
	SeverityNumber       int            `json:"severityNumber"`
	SeverityText         string         `json:"severityText"`
	Body                 otlpAnyValue   `json:"body"`
	Attributes           []otlpKeyValue `json:"attributes,omitempty"`
}

type otlpKeyValue struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

type otlpAnyValue struct {
	StringValue string `json:"stringValue,omitempty"`
	IntValue    int64  `json:"intValue,omitempty,string"`
	BoolValue   bool   `json:"boolValue,omitempty"`
}
