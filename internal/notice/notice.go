// internal/notice/notice.go
package notice

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

type noticeState struct {
	mu      sync.Mutex
	stderr  io.Writer
	logFile *os.File
	logPath string
	verbose bool
	wrote   bool // anything written silently during running phase
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

func writeMessage(level, format string, args ...any) {
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
}

// Info writes a user-facing informational message.
func Info(format string, args ...any) { writeMessage("info", format, args...) }

// Warn writes a user-facing warning.
func Warn(format string, args ...any) { writeMessage("warn", format, args...) }

// Error writes a user-facing error (does not terminate — caller handles that).
func Error(format string, args ...any) { writeMessage("error", format, args...) }

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
