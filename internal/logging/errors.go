package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"devsandbox/internal/logrotate"
)

// ErrorLogger logs errors from the logging subsystem to a local file.
// This prevents silent failures when remote logging destinations are unreachable.
type ErrorLogger struct {
	file *os.File
	path string
	mu   sync.Mutex
}

// NewErrorLogger creates an error logger that writes to the specified file.
// The file is created if it doesn't exist, and appended to if it does.
//
// The log is rotated before the append handle opens, so a long-lived host
// keeps a bounded number of bounded files rather than one that only grows.
// A rotation failure never fails the open: the log still receives its entries,
// and the failure is recorded in the log itself, which is the only place this
// package can report anything (it must not import internal/notice - notice is
// imported by internal/config, which this package imports).
func NewErrorLogger(path string) (*ErrorLogger, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	_, rotateErr := logrotate.Rotate(path, logrotate.Options{})

	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("failed to open error log: %w", err)
	}

	l := &ErrorLogger{file: file, path: path}
	if rotateErr != nil {
		l.LogErrorf("logrotate", "failed to rotate %s: %v", path, rotateErr)
	}
	return l, nil
}

// write appends one entry, reopening the log first when another process
// rotated the file out from under this handle. Without the reopen this process
// would keep appending to the renamed backup, which nothing size-checks again.
//
// The handle is read and written under the mutex, never outside it: the reopen
// reassigns l.file, and these loggers are shared by the socket proxies, which
// write from a goroutine per connection.
func (l *ErrorLogger) write(line string) {
	if l == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file == nil {
		return
	}
	l.file = logrotate.ReopenIfRotated(l.file, l.path, 0o600)
	_, _ = l.file.WriteString(line)
}

// LogError writes an error entry to the log file.
func (l *ErrorLogger) LogError(component, operation string, err error) {
	l.write(fmt.Sprintf("%s [%s] %s: %v\n", time.Now().Format(time.RFC3339), component, operation, err))
}

// LogErrorf writes a formatted error entry to the log file.
func (l *ErrorLogger) LogErrorf(component, format string, args ...any) {
	l.write(fmt.Sprintf("%s [%s] ERROR %s\n", time.Now().Format(time.RFC3339), component, fmt.Sprintf(format, args...)))
}

// LogInfof writes a formatted info entry to the log file.
func (l *ErrorLogger) LogInfof(component, format string, args ...any) {
	l.write(fmt.Sprintf("%s [%s] INFO %s\n", time.Now().Format(time.RFC3339), component, fmt.Sprintf(format, args...)))
}

// Close closes the error log file. The handle is dropped as well as closed: a
// write that arrived afterwards would otherwise find ReopenIfRotated unable to
// stat the closed descriptor, reopen the path, and resurrect a logger the
// caller has already shut down.
func (l *ErrorLogger) Close() error {
	if l == nil {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
