// internal/notice/notice.go
package notice

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Level mirrors the logging package's level strings without importing it
// (avoids an import cycle: logging → config → notice).
type Level string

const (
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// SinkFunc is the callback shape used to forward notice entries to an
// external destination (typically the audit log dispatcher).
type SinkFunc func(level Level, msg string, ts time.Time)

// maxBuffered bounds the in-memory ring that captures notice entries before
// a sink is attached. Overflow drops oldest entries and is reported back to
// the caller of AttachSink.
const maxBuffered = 256

type bufferedEntry struct {
	level Level
	msg   string
	ts    time.Time
}

type noticeState struct {
	mu           sync.Mutex
	stderr       io.Writer
	logFile      *os.File
	logPath      string
	verbose      bool
	wrote        bool // anything written silently during running phase
	buffer       []bufferedEntry
	sink         SinkFunc
	droppedCount int
}

var state = &noticeState{stderr: os.Stderr}

// Setup initializes the notice package. logPath is where the wrapper log will
// be written; verbose forces all messages to real stderr regardless of phase;
// stderrOverride is used in tests (pass nil to use os.Stderr).
func Setup(logPath string, verbose bool, stderrOverride io.Writer) error {
	state.mu.Lock()
	defer state.mu.Unlock()

	if stderrOverride != nil {
		state.stderr = stderrOverride
	} else {
		state.stderr = os.Stderr
	}
	state.verbose = verbose
	state.logPath = logPath

	// Reset audit sink/buffer state so a re-Setup (e.g., between tests or in
	// long-running processes that re-init) cannot leak entries to a stale
	// dispatcher or replay a previous session's buffer.
	state.sink = nil
	state.buffer = nil
	state.droppedCount = 0
	state.wrote = false

	if logPath != "" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			return fmt.Errorf("notice: mkdir log dir: %w", err)
		}
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("notice: open log: %w", err)
		}
		state.logFile = f
	}
	return nil
}

// LogPath returns the current wrapper log path (empty if not configured).
func LogPath() string {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.logPath
}

func writeMessage(level string, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	state.mu.Lock()
	defer state.mu.Unlock()

	// Always write to log file if configured.
	if state.logFile != nil {
		fmt.Fprintf(state.logFile, "[%s] %s", level, msg) //nolint:errcheck
		if len(msg) == 0 || msg[len(msg)-1] != '\n' {
			fmt.Fprintln(state.logFile) //nolint:errcheck
		}
	}

	// Decide whether to write to real stderr.
	p := Phase()
	writeToStderr := state.verbose || p == PhaseStartup || p == PhaseTeardown
	if writeToStderr {
		fmt.Fprint(state.stderr, msg) //nolint:errcheck
		if len(msg) == 0 || msg[len(msg)-1] != '\n' {
			fmt.Fprintln(state.stderr) //nolint:errcheck
		}
	} else {
		state.wrote = true
	}

	// Audit forwarding: dispatch live if a sink is attached, otherwise buffer
	// for later drain by AttachSink.
	lvl := levelFor(level)
	if state.sink != nil {
		state.sink(lvl, msg, time.Now())
	} else {
		state.bufferLocked(lvl, msg)
	}
}

// Info writes a user-facing informational message.
func Info(format string, args ...any) { writeMessage("info", format, args...) }

// Warn writes a user-facing warning.
func Warn(format string, args ...any) { writeMessage("warn", format, args...) }

// Error writes a user-facing error (does not terminate — caller handles that).
func Error(format string, args ...any) { writeMessage("error", format, args...) }

// AttachSink binds an external sink (typically wired to the audit log
// dispatcher in main.go) and drains the pre-attach buffer through it.
// Subsequent notice writes call the sink synchronously while still writing to
// stderr/file as today.
//
// Returns the count of entries dropped due to pre-attach buffer overflow.
// Caller is responsible for surfacing this count (e.g., as a notice.overflow
// audit event) — keeping the responsibility in the caller avoids having
// notice depend on the logging package, which would create an import cycle
// (logging → config → notice).
func AttachSink(sink SinkFunc) int {
	if sink == nil {
		return 0
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	state.sink = sink

	for _, e := range state.buffer {
		sink(e.level, e.msg, e.ts)
	}
	state.buffer = nil

	dropped := state.droppedCount
	state.droppedCount = 0
	return dropped
}

// DroppedCount returns the count of entries dropped due to pre-attach buffer
// overflow without resetting it. Primarily useful in tests.
func DroppedCount() int {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.droppedCount
}

// levelFor maps the notice level string to a notice.Level.
func levelFor(level string) Level {
	switch level {
	case "warn":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

// bufferLocked appends a captured entry to the ring; oldest is dropped on
// overflow and droppedCount is incremented. Caller holds state.mu.
func (s *noticeState) bufferLocked(lvl Level, msg string) {
	if len(s.buffer) >= maxBuffered {
		s.buffer = s.buffer[1:]
		s.droppedCount++
	}
	s.buffer = append(s.buffer, bufferedEntry{level: lvl, msg: msg, ts: time.Now()})
}

// Flush emits a final banner on real stderr if any messages were suppressed.
// Safe to call multiple times. Should be called at process exit.
func Flush() {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.wrote && state.logPath != "" {
		fmt.Fprintf(state.stderr, "[devsandbox] wrapper messages were written to %s\n", state.logPath) //nolint:errcheck
		state.wrote = false
	}
	if state.logFile != nil {
		_ = state.logFile.Close()
		state.logFile = nil
	}
}
