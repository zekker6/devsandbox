package logging

import (
	"fmt"
	"time"
)

// ComponentLogger provides scoped logging for a specific component.
// It writes to both a local ErrorLogger (file) and a remote Dispatcher
// (syslog, OTLP) when configured. Nil-safe: if both are nil, calls are no-ops.
type ComponentLogger struct {
	component   string
	errorLogger *ErrorLogger
	dispatcher  *Dispatcher
}

// NewComponentLogger creates a logger for the given component.
// Either errorLogger or dispatcher (or both) may be nil.
func NewComponentLogger(component string, errorLogger *ErrorLogger, dispatcher *Dispatcher) *ComponentLogger {
	return &ComponentLogger{
		component:   component,
		errorLogger: errorLogger,
		dispatcher:  dispatcher,
	}
}

// ComponentLogger creates a scoped logger for the given component.
// The receiver may be nil, in which case only the errorLogger is used.
func (d *Dispatcher) ComponentLogger(component string, errorLogger *ErrorLogger) *ComponentLogger {
	return &ComponentLogger{
		component:   component,
		errorLogger: errorLogger,
		dispatcher:  d,
	}
}

// Warnf logs a warning message.
func (l *ComponentLogger) Warnf(format string, args ...any) {
	if l == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	l.writeLocal(LevelWarn, msg)
	l.dispatch(LevelWarn, msg)
}

// Infof logs an informational message.
func (l *ComponentLogger) Infof(format string, args ...any) {
	if l == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	l.writeLocal(LevelInfo, msg)
	l.dispatch(LevelInfo, msg)
}

// Errorf logs an error message.
func (l *ComponentLogger) Errorf(format string, args ...any) {
	if l == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	l.writeLocal(LevelError, msg)
	l.dispatch(LevelError, msg)
}

// writeLocal writes to the local ErrorLogger file.
func (l *ComponentLogger) writeLocal(level Level, msg string) {
	if l.errorLogger == nil {
		return
	}
	switch level {
	case LevelError:
		l.errorLogger.LogErrorf(l.component, "%s", msg)
	case LevelWarn:
		l.errorLogger.LogErrorf(l.component, "WARN %s", msg)
	default:
		l.errorLogger.LogInfof(l.component, "%s", msg)
	}
}

// dispatch sends the entry to remote backends via the Dispatcher.
func (l *ComponentLogger) dispatch(level Level, msg string) {
	if l.dispatcher == nil {
		return
	}
	_ = l.dispatcher.Write(&Entry{
		Timestamp: time.Now(),
		Level:     level,
		Message:   msg,
		Fields: map[string]any{
			"component": l.component,
		},
	})
}
