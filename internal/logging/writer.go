// Package logging provides remote log forwarding capabilities.
package logging

import (
	"maps"
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
	writers       []Writer
	errorLogger   *ErrorLogger
	sessionFields map[string]any
	mu            sync.RWMutex
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

// SetSessionFields stores per-session fields that are merged into every
// dispatched Entry's Fields map at Write time. Per-event keys win on
// collision, so this is a "fill the gaps" merge. Pass nil to clear.
func (d *Dispatcher) SetSessionFields(fields map[string]any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if fields == nil {
		d.sessionFields = nil
		return
	}
	cp := make(map[string]any, len(fields))
	maps.Copy(cp, fields)
	d.sessionFields = cp
}

// Write sends an entry to all registered writers, merging session fields
// into a copy of entry.Fields beforehand. The caller-supplied entry is not
// mutated. Errors from individual writers are ignored to ensure delivery to
// other writers.
func (d *Dispatcher) Write(entry *Entry) error {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if len(d.sessionFields) > 0 {
		merged := make(map[string]any, len(entry.Fields)+len(d.sessionFields))
		maps.Copy(merged, d.sessionFields)
		maps.Copy(merged, entry.Fields)
		copyEntry := *entry
		copyEntry.Fields = merged
		entry = &copyEntry
	}

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

// Event dispatches a structured event entry: Message and Fields["event"] are
// both set to name, the supplied fields are merged in (caller-supplied map is
// not mutated), and the entry is written through the dispatcher. The "event"
// key is always the helper-controlled name even if the caller passed one.
func (d *Dispatcher) Event(level Level, name string, fields map[string]any) error {
	merged := make(map[string]any, len(fields)+1)
	maps.Copy(merged, fields)
	merged["event"] = name

	return d.Write(&Entry{
		Timestamp: time.Now(),
		Level:     level,
		Message:   name,
		Fields:    merged,
	})
}

// HasWriters returns true if the dispatcher has any writers registered.
func (d *Dispatcher) HasWriters() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.writers) > 0
}
