package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"devsandbox/internal/logrotate"
)

// TestReadLoggingErrorsLogs_ReadsRotatedBackups pins the reader to the files
// rotation actually holds the bytes in. A rotation moves every existing entry
// into logging-errors.log.1 and leaves the live name empty, so a reader that
// opens only the live path reports no logging errors in the window where there
// are most of them.
func TestReadLoggingErrorsLogs_ReadsRotatedBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logging-errors.log")

	now := time.Now()
	stamp := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }

	write := func(name, line string) {
		t.Helper()
		if err := os.WriteFile(name, []byte(line+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(logrotate.BackupPath(path, 2), fmt.Sprintf("%s [logging] oldest", stamp(-3*time.Minute)))
	write(logrotate.BackupPath(path, 1), fmt.Sprintf("%s [logging] rotated", stamp(-2*time.Minute)))
	write(path, fmt.Sprintf("%s [logging] live", stamp(-time.Minute)))

	lines, err := readLoggingErrorsLogs(path, time.Time{})
	if err != nil {
		t.Fatalf("readLoggingErrorsLogs: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3: %v", len(lines), lines)
	}
	for i, want := range []string{"oldest", "rotated", "live"} {
		if !strings.HasSuffix(lines[i], want) {
			t.Errorf("line %d = %q, want it to end in %q (oldest backup first)", i, lines[i], want)
		}
	}
}

// TestReadLoggingErrorsLogs_MissingFilesAreNotErrors covers the two normal
// cases: a sandbox that never logged an error has no live file, and a log that
// has not rotated yet has no backups.
func TestReadLoggingErrorsLogs_MissingFilesAreNotErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logging-errors.log")

	lines, err := readLoggingErrorsLogs(path, time.Time{})
	if err != nil {
		t.Fatalf("no log at all: %v", err)
	}
	if len(lines) != 0 {
		t.Fatalf("got %v, want no lines", lines)
	}

	entry := time.Now().Format(time.RFC3339) + " [logging] live"
	if err := os.WriteFile(path, []byte(entry+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, err = readLoggingErrorsLogs(path, time.Time{})
	if err != nil {
		t.Fatalf("unrotated log: %v", err)
	}
	if !slices.Equal(lines, []string{entry}) {
		t.Fatalf("got %v, want %v", lines, []string{entry})
	}
}

// TestTailFile_RestartsAfterRotation pins the follow loop to the file the path
// names now. A rotation replaces the log with an empty one, so the offset held
// from the previous inode is past its end; without a reset the tail waits for
// the new log to grow past a size it no longer has and never prints again.
func TestTailFile_RestartsAfterRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logging-errors.log")

	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, offset, err := tailFile(path, 0)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(path, logrotate.BackupPath(path, 1)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("third\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	lines, newOffset, err := tailFile(path, offset)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(lines, []string{"third"}) {
		t.Fatalf("got %v, want the rotated-in file read from its start", lines)
	}
	if newOffset != int64(len("third\n")) {
		t.Errorf("offset = %d, want %d", newOffset, len("third\n"))
	}
}
