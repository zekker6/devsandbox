package proxy

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		Suffix:   ".log.gz",
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

	// Check that file was created
	files, _ := filepath.Glob(filepath.Join(tmpDir, "test_*.log.gz"))
	if len(files) != 1 {
		t.Errorf("expected 1 file, got %d", len(files))
	}

	// Read and decompress file contents
	f, _ := os.Open(files[0])
	defer func() { _ = f.Close() }()
	gz, _ := gzip.NewReader(f)
	defer func() { _ = gz.Close() }()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, gz)

	if buf.String() != msg {
		t.Errorf("file content = %q, want %q", buf.String(), msg)
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
		Dir:      tmpDir,
		Prefix:   "test",
		Suffix:   ".log.gz",
		MaxSize:  50, // 50 bytes
		MaxFiles: 3,
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

	// Should have at most MaxFiles files due to pruning
	files, _ := filepath.Glob(filepath.Join(tmpDir, "test_*.log.gz"))
	if len(files) > 3 {
		t.Errorf("expected at most 3 files, got %d", len(files))
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
		Dir:      tmpDir,
		Prefix:   "test",
		Suffix:   ".log.gz",
		MaxSize:  10, // Very small
		MaxFiles: 2,  // Keep only 2 files
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

	// Should have at most 2 files
	files, _ := filepath.Glob(filepath.Join(tmpDir, "test_*.log.gz"))
	if len(files) > 2 {
		t.Errorf("expected at most 2 files after pruning, got %d", len(files))
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
		Suffix:   ".log.gz",
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
	files1, _ := filepath.Glob(filepath.Join(tmpDir, "test_*.log.gz"))
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
	files2, _ := filepath.Glob(filepath.Join(tmpDir, "test_*.log.gz"))
	if len(files2) != 1 {
		t.Errorf("expected 1 file after reuse, got %d (file was not reused)", len(files2))
	}

	// Read the file and verify both messages are present
	// Note: gzip streams are concatenated, so we need a multi-stream reader
	f, _ := os.Open(files2[0])
	defer func() { _ = f.Close() }()

	var allContent bytes.Buffer
	for {
		gz, err := gzip.NewReader(f)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("gzip.NewReader failed: %v", err)
		}
		_, _ = io.Copy(&allContent, gz)
		_ = gz.Close()
	}

	content := allContent.String()
	if !strings.Contains(content, "first message") {
		t.Errorf("content missing 'first message': %q", content)
	}
	if !strings.Contains(content, "second message") {
		t.Errorf("content missing 'second message': %q", content)
	}
}
