package proxy

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRotatingFileWriter_Basic(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "filewriter-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	w, err := NewRotatingFileWriter(RotatingFileWriterConfig{
		Dir:      tmpDir,
		Prefix:   "test",
		Suffix:   ".log",
		MaxSize:  1024,
		MaxFiles: 3,
	})
	if err != nil {
		t.Fatalf("NewRotatingFileWriter failed: %v", err)
	}

	// Write some data
	msg := "hello world\n"
	n, err := w.Write([]byte(msg))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != len(msg) {
		t.Errorf("Write returned %d, want %d", n, len(msg))
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Check that file was created (uncompressed)
	files, _ := filepath.Glob(filepath.Join(tmpDir, "test_*.log"))
	if len(files) != 1 {
		t.Errorf("expected 1 file, got %d", len(files))
	}

	// Read file contents directly (no decompression needed)
	content, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if string(content) != msg {
		t.Errorf("file content = %q, want %q", string(content), msg)
	}
}

func TestRotatingFileWriter_Rotation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "filewriter-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Small max size to trigger rotation
	w, err := NewRotatingFileWriter(RotatingFileWriterConfig{
		Dir:           tmpDir,
		Prefix:        "test",
		Suffix:        ".log",
		ArchiveSuffix: ".log.gz",
		MaxSize:       50, // 50 bytes
		MaxFiles:      5,  // Increase to account for both .log and .log.gz files
	})
	if err != nil {
		t.Fatalf("NewRotatingFileWriter failed: %v", err)
	}

	// Write enough data to trigger multiple rotations
	msg := strings.Repeat("x", 30) + "\n"
	for i := 0; i < 5; i++ {
		_, err := w.Write([]byte(msg))
		if err != nil {
			t.Fatalf("Write %d failed: %v", i, err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Wait for async compression to complete
	time.Sleep(100 * time.Millisecond)

	// Check that files were created (mix of .log and .log.gz)
	allFiles, _ := filepath.Glob(filepath.Join(tmpDir, "test_*"))
	if len(allFiles) == 0 {
		t.Errorf("expected at least 1 file, got 0")
	}
}

func TestRotatingFileWriter_Pruning(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "filewriter-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Very small max size to ensure rotation on every write
	w, err := NewRotatingFileWriter(RotatingFileWriterConfig{
		Dir:           tmpDir,
		Prefix:        "test",
		Suffix:        ".log",
		ArchiveSuffix: ".log.gz",
		MaxSize:       10, // Very small
		MaxFiles:      3,  // Keep only 3 files
	})
	if err != nil {
		t.Fatalf("NewRotatingFileWriter failed: %v", err)
	}

	// Write multiple times to create several files
	for i := 0; i < 10; i++ {
		_, _ = w.Write([]byte("data\n"))
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Wait for async compression to complete
	time.Sleep(100 * time.Millisecond)

	// Should have at most 3 files (active + archived)
	allFiles, _ := filepath.Glob(filepath.Join(tmpDir, "test_*"))
	if len(allFiles) > 3 {
		t.Errorf("expected at most 3 files after pruning, got %d", len(allFiles))
	}
}

func TestRotatingFileWriter_ReuseExistingFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "filewriter-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := RotatingFileWriterConfig{
		Dir:      tmpDir,
		Prefix:   "test",
		Suffix:   ".log",
		MaxSize:  1024, // Large enough to not rotate
		MaxFiles: 3,
	}

	// First writer - write some data
	w1, err := NewRotatingFileWriter(cfg)
	if err != nil {
		t.Fatalf("NewRotatingFileWriter 1 failed: %v", err)
	}
	_, err = w1.Write([]byte("first message\n"))
	if err != nil {
		t.Fatalf("Write 1 failed: %v", err)
	}
	if err := w1.Close(); err != nil {
		t.Fatalf("Close 1 failed: %v", err)
	}

	// Count files after first writer
	files1, _ := filepath.Glob(filepath.Join(tmpDir, "test_*.log"))
	if len(files1) != 1 {
		t.Fatalf("expected 1 file after first writer, got %d", len(files1))
	}

	// Second writer - should reuse the same file
	w2, err := NewRotatingFileWriter(cfg)
	if err != nil {
		t.Fatalf("NewRotatingFileWriter 2 failed: %v", err)
	}
	_, err = w2.Write([]byte("second message\n"))
	if err != nil {
		t.Fatalf("Write 2 failed: %v", err)
	}
	if err := w2.Close(); err != nil {
		t.Fatalf("Close 2 failed: %v", err)
	}

	// Should still have only 1 file (reused)
	files2, _ := filepath.Glob(filepath.Join(tmpDir, "test_*.log"))
	if len(files2) != 1 {
		t.Errorf("expected 1 file after reuse, got %d (file was not reused)", len(files2))
	}

	// Read the file and verify both messages are present
	content, err := os.ReadFile(files2[0])
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if !strings.Contains(string(content), "first message") {
		t.Errorf("content missing 'first message': %q", string(content))
	}
	if !strings.Contains(string(content), "second message") {
		t.Errorf("content missing 'second message': %q", string(content))
	}
}

func TestRotatingFileWriter_CompressionOnRotation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "filewriter-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	w, err := NewRotatingFileWriter(RotatingFileWriterConfig{
		Dir:           tmpDir,
		Prefix:        "test",
		Suffix:        ".log",
		ArchiveSuffix: ".log.gz",
		MaxSize:       20, // Small to trigger rotation
		MaxFiles:      10,
	})
	if err != nil {
		t.Fatalf("NewRotatingFileWriter failed: %v", err)
	}

	// Write enough to trigger rotation
	_, _ = w.Write([]byte("first message that is long\n"))
	_, _ = w.Write([]byte("second message\n"))

	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Wait for async compression
	time.Sleep(200 * time.Millisecond)

	// Should have compressed files
	gzFiles, _ := filepath.Glob(filepath.Join(tmpDir, "test_*.log.gz"))
	logFiles, _ := filepath.Glob(filepath.Join(tmpDir, "test_*.log"))

	// At least one compressed file should exist (from rotation)
	if len(gzFiles) == 0 && len(logFiles) > 1 {
		t.Errorf("expected at least one compressed file after rotation")
	}

	// Verify compressed file is readable
	if len(gzFiles) > 0 {
		f, err := os.Open(gzFiles[0])
		if err != nil {
			t.Fatalf("failed to open gzip file: %v", err)
		}
		defer func() { _ = f.Close() }()

		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatalf("failed to create gzip reader: %v", err)
		}
		defer func() { _ = gz.Close() }()

		var buf bytes.Buffer
		_, err = io.Copy(&buf, gz)
		if err != nil {
			t.Fatalf("failed to read gzip content: %v", err)
		}

		if buf.Len() == 0 {
			t.Errorf("compressed file is empty")
		}
	}
}
