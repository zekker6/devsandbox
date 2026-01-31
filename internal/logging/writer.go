// Package logging provides remote log forwarding capabilities.
package logging

import (
	"sync"
	"time"
)

// Level represents log severity level.
type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Entry represents a log entry to be forwarded.
type Entry struct {
	Timestamp time.Time
	Level     Level
	Message   string
	Fields    map[string]any
}

// Writer is the interface for log destinations.
type Writer interface {
	// Write sends a log entry to the destination.
	Write(entry *Entry) error

	// Close flushes any buffered data and closes the writer.
	Close() error
}

// Dispatcher fans out log entries to multiple writers.
type Dispatcher struct {
	writers     []Writer
	errorLogger *ErrorLogger
	mu          sync.RWMutex
}

// NewDispatcher creates a new log dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		writers: make([]Writer, 0),
	}
}

// AddWriter adds a writer to the dispatcher.
func (d *Dispatcher) AddWriter(w Writer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writers = append(d.writers, w)
}

// Write sends an entry to all registered writers.
// Errors from individual writers are ignored to ensure delivery to other writers.
func (d *Dispatcher) Write(entry *Entry) error {
	d.mu.RLock()
	defer d.mu.RUnlock()

	for _, w := range d.writers {
		_ = w.Write(entry)
	}

	return nil
}

// Close closes all registered writers and the error logger.
func (d *Dispatcher) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, w := range d.writers {
		_ = w.Close()
	}

	if d.errorLogger != nil {
		_ = d.errorLogger.Close()
	}

	d.writers = nil
	return nil
}

// HasWriters returns true if the dispatcher has any writers registered.
func (d *Dispatcher) HasWriters() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.writers) > 0
}
