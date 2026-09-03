package logging

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"devsandbox/internal/logrotate"
)

// oversizedLog creates a log file at path that is at the rotation limit, using
// a sparse truncate so the test does not write eight megabytes.
func oversizedLog(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Truncate(logrotate.DefaultMaxSize); err != nil {
		t.Fatalf("truncate %s: %v", path, err)
	}
}

// tail returns the last n bytes of path, so a test can read what was appended
// to a file that is deliberately huge.
func tail(t *testing.T, path string, n int64) string {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	off := info.Size() - n
	if off < 0 {
		off = 0
		n = info.Size()
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, off); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(buf)
}

func TestNewErrorLogger_RotatesAtOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tools-errors.log")
	oversizedLog(t, path)

	logger, err := NewErrorLogger(path)
	if err != nil {
		t.Fatalf("NewErrorLogger: %v", err)
	}
	defer func() { _ = logger.Close() }()

	backup, err := os.Stat(path + ".1")
	if err != nil {
		t.Fatalf("stat backup: %v - the oversized log was not rotated aside", err)
	}
	if backup.Size() != logrotate.DefaultMaxSize {
		t.Errorf("backup size = %d, want %d: the wrong file was renamed", backup.Size(), logrotate.DefaultMaxSize)
	}

	logger.LogErrorf("tools", "after rotation")
	live, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read live log: %v", err)
	}
	if !strings.Contains(string(live), "after rotation") {
		t.Errorf("live log = %q, want the entry written after rotation", live)
	}
	if int64(len(live)) >= logrotate.DefaultMaxSize {
		t.Errorf("live log is %d bytes: the append landed in the old file, not a fresh one", len(live))
	}
}

// A rotation that cannot run must not cost the caller its log: the entries
// still have to be written, and the failure has to be visible somewhere.
func TestNewErrorLogger_RotationFailureKeepsAppendWorking(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tools-errors.log")
	oversizedLog(t, path)
	// A directory where the lock file goes: the lock cannot be taken, so the
	// rotation fails with the log still in place.
	if err := os.Mkdir(path+".lock", 0o755); err != nil {
		t.Fatalf("mkdir lock: %v", err)
	}

	logger, err := NewErrorLogger(path)
	if err != nil {
		t.Fatalf("NewErrorLogger = %v, want the open to succeed despite the rotation failure", err)
	}
	defer func() { _ = logger.Close() }()

	if _, err := os.Stat(path + ".1"); err == nil {
		t.Error("a backup exists: the rotation was expected to fail")
	}

	logger.LogErrorf("tools", "after failed rotation")
	got := tail(t, path, 4096)
	if !strings.Contains(got, "failed to rotate") {
		t.Errorf("log tail = %q, want the rotation failure recorded", got)
	}
	if !strings.Contains(got, "after failed rotation") {
		t.Errorf("log tail = %q, want the entry written after the failed rotation", got)
	}
}

func TestErrorLogger_ReopensAfterRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tools-errors.log")

	logger, err := NewErrorLogger(path)
	if err != nil {
		t.Fatalf("NewErrorLogger: %v", err)
	}
	defer func() { _ = logger.Close() }()

	logger.LogInfof("tools", "before rotation")
	// Another process rotates the log out from under this handle.
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	logger.LogInfof("tools", "after rotation")

	live, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read live log: %v - the writer did not reopen the rotated path", err)
	}
	if !strings.Contains(string(live), "after rotation") {
		t.Errorf("live log = %q, want the entry written after the rotation", live)
	}
	rotated, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("read rotated log: %v", err)
	}
	if strings.Contains(string(rotated), "after rotation") {
		t.Error("the rotated file kept growing: the writer followed the rename instead of reopening")
	}
}

func TestErrorLogger_FailedReopenKeepsOldHandle(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "internal")
	path := filepath.Join(logDir, "tools-errors.log")

	logger, err := NewErrorLogger(path)
	if err != nil {
		t.Fatalf("NewErrorLogger: %v", err)
	}
	defer func() { _ = logger.Close() }()

	logger.LogInfof("tools", "before rotation")

	// Move the log aside and remove its directory, so the path names nothing
	// and cannot be recreated: the reopen has to fail.
	moved := filepath.Join(dir, "moved.log")
	if err := os.Rename(path, moved); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := os.Remove(logDir); err != nil {
		t.Fatalf("remove log dir: %v", err)
	}

	logger.LogInfof("tools", "after failed reopen")

	got, err := os.ReadFile(moved)
	if err != nil {
		t.Fatalf("read moved log: %v", err)
	}
	if !strings.Contains(string(got), "after failed reopen") {
		t.Errorf("moved log = %q, want the write kept on the old handle rather than dropped", got)
	}
}

// TestErrorLogger_ConcurrentWritesUnderRotation: the socket proxies share one
// ErrorLogger across a goroutine per connection, and the handle is now
// reassigned by the rotation reopen rather than fixed at construction. Reading
// it outside the mutex is a data race, which -race catches here.
func TestErrorLogger_ConcurrentWritesUnderRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tools-errors.log")

	logger, err := NewErrorLogger(path)
	if err != nil {
		t.Fatalf("NewErrorLogger: %v", err)
	}
	defer func() { _ = logger.Close() }()

	const writers = 8
	const perWriter = 25
	var wg sync.WaitGroup
	wg.Add(writers + 1)
	for i := range writers {
		go func() {
			defer wg.Done()
			for j := range perWriter {
				logger.LogErrorf("tools", "writer %d entry %d", i, j)
			}
		}()
	}
	// A competing process rotating the log underneath every writer.
	go func() {
		defer wg.Done()
		for range 5 {
			_ = os.Rename(path, path+".1")
		}
	}()
	wg.Wait()

	// Every line landed in one file or the other; none was dropped.
	var lines int
	for _, p := range []string{path, path + ".1"} {
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("read %s: %v", p, err)
		}
		lines += strings.Count(string(data), "\n")
	}
	if lines != writers*perWriter {
		t.Errorf("wrote %d lines across the live log and its backup, want %d", lines, writers*perWriter)
	}
}

// TestErrorLogger_WriteAfterCloseDoesNotResurrect: Close drops the handle as
// well as closing it. Without that, a later write finds ReopenIfRotated unable
// to stat the closed descriptor, reopens the path, and leaves a live file
// handle nothing will ever close.
func TestErrorLogger_WriteAfterCloseDoesNotResurrect(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tools-errors.log")

	logger, err := NewErrorLogger(path)
	if err != nil {
		t.Fatalf("NewErrorLogger: %v", err)
	}
	logger.LogInfof("tools", "before close")
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove log: %v", err)
	}

	logger.LogInfof("tools", "after close")

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a write after Close recreated the log, stat err = %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
