package logging

import (
	"fmt"
	"maps"
	"path/filepath"
	"time"

	"devsandbox/internal/config"
	"devsandbox/internal/source"
)

// DispatcherConfig contains configuration for creating a dispatcher.
type DispatcherConfig struct {
	// Receivers is the list of log receiver configurations.
	Receivers []config.ReceiverConfig

	// GlobalAttrs are custom attributes added to all log entries (for OTLP resource attributes).
	GlobalAttrs map[string]string

	// ErrorLogDir is the directory for the internal error log file.
	// If empty, errors are silently ignored.
	ErrorLogDir string
}

// NewDispatcherFromConfig creates a dispatcher with writers from configuration.
// globalAttrs are custom attributes added to all log entries (for OTLP resource attributes).
// errorLogDir is the directory for internal error logs (pass empty to disable).
func NewDispatcherFromConfig(receivers []config.ReceiverConfig, globalAttrs map[string]string, errorLogDir string) (*Dispatcher, error) {
	cfg := DispatcherConfig{
		Receivers:   receivers,
		GlobalAttrs: globalAttrs,
		ErrorLogDir: errorLogDir,
	}
	return NewDispatcherWithConfig(cfg)
}

// NewDispatcherWithConfig creates a dispatcher from a DispatcherConfig.
func NewDispatcherWithConfig(cfg DispatcherConfig) (*Dispatcher, error) {
	d := NewDispatcher()

	// Create error logger if directory is specified
	var errorLogger *ErrorLogger
	if cfg.ErrorLogDir != "" {
		var err error
		errorLogPath := filepath.Join(cfg.ErrorLogDir, "logging-errors.log")
		errorLogger, err = NewErrorLogger(errorLogPath)
		if err != nil {
			return nil, fmt.Errorf("failed to create error logger: %w", err)
		}
		d.errorLogger = errorLogger
	}

	for i, r := range cfg.Receivers {
		w, err := newWriterFromConfig(r, cfg.GlobalAttrs, errorLogger)
		if err != nil {
			// Close any already-created writers
			_ = d.Close()
			return nil, fmt.Errorf("receiver %d (%s): %w", i, r.Type, err)
		}
		d.AddWriter(w)
	}

	return d, nil
}

// newWriterFromConfig creates a Writer from a ReceiverConfig.
func newWriterFromConfig(r config.ReceiverConfig, globalAttrs map[string]string, errorLogger *ErrorLogger) (Writer, error) {
	switch r.Type {
	case "syslog":
		return NewSyslogWriter(SyslogConfig{
			Facility:    r.Facility,
			Tag:         r.Tag,
			ErrorLogger: errorLogger,
		})

	case "syslog-remote":
		protocol := r.Protocol
		if protocol == "" {
			protocol = "udp"
		}
		return NewSyslogWriter(SyslogConfig{
			Network:     protocol,
			Address:     r.Address,
			Facility:    r.Facility,
			Tag:         r.Tag,
			ErrorLogger: errorLogger,
		})

	case "otlp":
		endpoint := r.Endpoint
		if endpoint == "" {
			endpoint = r.Address
		}
		if endpoint == "" {
			return nil, fmt.Errorf("endpoint is required for otlp receiver")
		}

		// Default to http protocol
		protocol := r.Protocol
		if protocol == "" {
			protocol = "http"
		}

		headers, err := resolveOTLPHeaders(r.Headers, r.HeaderSources)
		if err != nil {
			return nil, err
		}

		cfg := OTLPConfig{
			Endpoint:           endpoint,
			Protocol:           protocol,
			Headers:            headers,
			BatchSize:          r.BatchSize,
			Insecure:           r.Insecure,
			ResourceAttributes: globalAttrs,
			ErrorLogger:        errorLogger,
		}

		if r.FlushInterval != "" {
			d, err := time.ParseDuration(r.FlushInterval)
			if err != nil {
				return nil, fmt.Errorf("invalid flush_interval: %w", err)
			}
			cfg.FlushInterval = d
		}

		return NewOTLPWriter(cfg)

	default:
		return nil, fmt.Errorf("unknown receiver type: %s", r.Type)
	}
}

// resolveOTLPHeaders merges static headers with header sources resolved from
// env/file/value. Sources take precedence on key collisions. An env-sourced
// header that resolves to an empty string is rejected to avoid silently
// sending unauthenticated logs to an auth-enforced endpoint.
func resolveOTLPHeaders(static map[string]string, sources map[string]source.Source) (map[string]string, error) {
	if len(static) == 0 && len(sources) == 0 {
		return nil, nil
	}

	out := make(map[string]string, len(static)+len(sources))
	maps.Copy(out, static)

	for name, src := range sources {
		val, err := src.Resolve()
		if err != nil {
			return nil, fmt.Errorf("otlp header %q: %w", name, err)
		}
		if val == "" {
			return nil, fmt.Errorf("otlp header %q: source resolved to empty value (env var unset or file empty)", name)
		}
		out[name] = val
	}

	return out, nil
}
