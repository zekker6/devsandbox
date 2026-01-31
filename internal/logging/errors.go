package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrorLogger logs errors from the logging subsystem to a local file.
// This prevents silent failures when remote logging destinations are unreachable.
type ErrorLogger struct {
	file *os.File
	mu   sync.Mutex
}

// NewErrorLogger creates an error logger that writes to the specified file.
// The file is created if it doesn't exist, and appended to if it does.
func NewErrorLogger(path string) (*ErrorLogger, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open error log: %w", err)
	}

	return &ErrorLogger{file: file}, nil
}

// LogError writes an error entry to the log file.
func (l *ErrorLogger) LogError(component, operation string, err error) {
	if l == nil || l.file == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	timestamp := time.Now().Format(time.RFC3339)
	line := fmt.Sprintf("%s [%s] %s: %v\n", timestamp, component, operation, err)
	_, _ = l.file.WriteString(line)
}

// LogErrorf writes a formatted error entry to the log file.
func (l *ErrorLogger) LogErrorf(component, format string, args ...any) {
	if l == nil || l.file == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	timestamp := time.Now().Format(time.RFC3339)
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("%s [%s] %s\n", timestamp, component, msg)
	_, _ = l.file.WriteString(line)
}

// Close closes the error log file.
func (l *ErrorLogger) Close() error {
	if l == nil || l.file == nil {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	return l.file.Close()
}
