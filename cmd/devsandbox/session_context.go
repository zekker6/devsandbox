package main

import (
	"os"
	"time"

	"devsandbox/internal/logging"
	"devsandbox/internal/notice"
	"devsandbox/internal/version"
)

// buildSessionContext constructs a SessionContext for the current
// `devsandbox claude` invocation. Pure function — extracted out of runSandbox
// so it can be unit-tested without the full main flow.
func buildSessionContext(sandboxName, sandboxPath, projectDir, isolator string) (*logging.Context, error) {
	return logging.NewContext(
		sandboxName,
		sandboxPath,
		projectDir,
		isolator,
		version.Version,
		os.Getpid(),
	)
}

// noticeSinkFor returns a SinkFunc that forwards notice entries through the
// dispatcher with `component=wrapper`. The Dispatcher's session-fields map is
// merged in by Dispatcher.Write, so the sink itself only attaches the
// component label.
func noticeSinkFor(d *logging.Dispatcher) notice.SinkFunc {
	return func(level notice.Level, msg string, ts time.Time) {
		_ = d.Write(&logging.Entry{
			Timestamp: ts,
			Level:     logging.Level(level),
			Message:   msg,
			Fields:    map[string]any{"component": "wrapper"},
		})
	}
}
