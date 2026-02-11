package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComponentLogger_Nil(t *testing.T) {
	// Nil logger should not panic
	var l *ComponentLogger
	l.Warnf("test %s", "warn")
	l.Infof("test %s", "info")
	l.Errorf("test %s", "error")
}

func TestComponentLogger_LocalOnly(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")

	el, err := NewErrorLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = el.Close() }()

	l := NewComponentLogger("builder", el, nil)
	l.Warnf("mount conflict: %s", "/home/test")
	l.Infof("setup complete")
	l.Errorf("fatal: %v", "disk full")

	_ = el.Close()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)
	if !strings.Contains(content, "[builder]") {
		t.Error("expected component name in log output")
	}
	if !strings.Contains(content, "mount conflict: /home/test") {
		t.Error("expected warn message in log output")
	}
	if !strings.Contains(content, "setup complete") {
		t.Error("expected info message in log output")
	}
	if !strings.Contains(content, "fatal: disk full") {
		t.Error("expected error message in log output")
	}
}

func TestComponentLogger_WithDispatcher(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")

	el, err := NewErrorLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = el.Close() }()

	d := NewDispatcher()
	// Capture dispatched entries
	var captured []*Entry
	d.AddWriter(&captureWriter{entries: &captured})

	l := d.ComponentLogger("mounts", el)
	l.Warnf("pattern failed: %s", "*.env")
	l.Infof("expanded %d paths", 5)

	if len(captured) != 2 {
		t.Fatalf("expected 2 dispatched entries, got %d", len(captured))
	}

	if captured[0].Level != LevelWarn {
		t.Errorf("expected warn level, got %s", captured[0].Level)
	}
	if captured[0].Fields["component"] != "mounts" {
		t.Errorf("expected component=mounts, got %v", captured[0].Fields["component"])
	}
	if !strings.Contains(captured[0].Message, "pattern failed: *.env") {
		t.Errorf("unexpected message: %s", captured[0].Message)
	}

	if captured[1].Level != LevelInfo {
		t.Errorf("expected info level, got %s", captured[1].Level)
	}
}

func TestComponentLogger_NilBoth(t *testing.T) {
	// Both nil: should not panic, just no-op
	l := NewComponentLogger("test", nil, nil)
	l.Warnf("test")
	l.Infof("test")
	l.Errorf("test")
}

// captureWriter captures dispatched entries for testing.
type captureWriter struct {
	entries *[]*Entry
}

func (w *captureWriter) Write(entry *Entry) error {
	*w.entries = append(*w.entries, entry)
	return nil
}

func (w *captureWriter) Close() error {
	return nil
}
