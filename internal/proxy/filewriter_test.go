package proxy

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// readRotatedEntries returns every line readable from a writer's active and
// archived files, oldest index first.
func readRotatedEntries(t *testing.T, dir, prefix string) []string {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(dir, prefix+"_*"))
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}
	sort.Strings(matches)

	var entries []string
	for _, path := range matches {
		entries = append(entries, readLogFileLines(t, path)...)
	}
	return entries
}

func readLogFileLines(t *testing.T, path string) []string {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("failed to open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatalf("failed to read %s as gzip: %v", path, err)
		}
		defer func() { _ = gz.Close() }()
		r = gz
	}

	var lines []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			lines = append(lines, line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("failed to scan %s: %v", path, err)
	}
	return lines
}

// assertNewestEntriesReadable checks that got is a non-empty contiguous suffix
// of want: pruning may drop the oldest entries, but nothing written after them
// may go missing.
func assertNewestEntriesReadable(t *testing.T, want, got []string) {
	t.Helper()

	if len(got) == 0 {
		t.Fatalf("no entries survived rotation, %d were written", len(want))
	}
	if len(got) > len(want) {
		t.Fatalf("read %d entries, only %d were written", len(got), len(want))
	}
	if newest := want[len(want)-len(got):]; !slices.Equal(got, newest) {
		t.Errorf("readable entries are not the newest ones written\ngot:  %v\nwant: %v", got, newest)
	}
}

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
	var written []string
	for i := range 10 {
		entry := fmt.Sprintf("data-%02d", i)
		if _, err := w.Write([]byte(entry + "\n")); err != nil {
			t.Fatalf("Write %d failed: %v", i, err)
		}
		written = append(written, entry)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Should have at most 3 files (active + archived)
	allFiles, _ := filepath.Glob(filepath.Join(tmpDir, "test_*"))
	if len(allFiles) > 3 {
		t.Errorf("expected at most 3 files after pruning, got %d", len(allFiles))
	}

	// Pruning drops the oldest files; everything newer must still be readable.
	assertNewestEntriesReadable(t, written, readRotatedEntries(t, tmpDir, "test"))
}

// TestRotatingFileWriter_SaturationKeepsEntriesReadable drives the writer past
// the point where pruning caps the file count. An index derived from that count
// is pinned at MaxFiles, so the writer reopens the path it just handed to the
// compressor and the entries written after saturation are lost.
func TestRotatingFileWriter_SaturationKeepsEntriesReadable(t *testing.T) {
	tmpDir := t.TempDir()

	w, err := NewRotatingFileWriter(RotatingFileWriterConfig{
		Dir:           tmpDir,
		Prefix:        "test",
		Suffix:        ".log",
		ArchiveSuffix: ".log.gz",
		MaxSize:       32,
		MaxFiles:      4,
	})
	if err != nil {
		t.Fatalf("NewRotatingFileWriter failed: %v", err)
	}

	var written []string
	for i := range 12 {
		entry := fmt.Sprintf("entry-%02d-%s", i, strings.Repeat("x", 32))
		if _, err := w.Write([]byte(entry + "\n")); err != nil {
			t.Fatalf("Write %d failed: %v", i, err)
		}
		written = append(written, entry)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	assertNewestEntriesReadable(t, written, readRotatedEntries(t, tmpDir, "test"))
}

// TestRotatingFileWriter_SaturationKeepsRotating covers the second
// manifestation: a writer with no ArchiveSuffix compresses nothing and removes
// nothing, so a pinned index reopens the saturated file O_APPEND with the byte
// counter reset - rotation silently stops and one file grows without bound.
func TestRotatingFileWriter_SaturationKeepsRotating(t *testing.T) {
	tmpDir := t.TempDir()

	const maxSize = 32
	w, err := NewRotatingFileWriter(RotatingFileWriterConfig{
		Dir:      tmpDir,
		Prefix:   "test",
		Suffix:   ".log",
		MaxSize:  maxSize,
		MaxFiles: 3,
	})
	if err != nil {
		t.Fatalf("NewRotatingFileWriter failed: %v", err)
	}

	var written []string
	entrySize := 0
	for i := range 10 {
		entry := fmt.Sprintf("entry-%02d-%s", i, strings.Repeat("y", 40))
		if _, err := w.Write([]byte(entry + "\n")); err != nil {
			t.Fatalf("Write %d failed: %v", i, err)
		}
		written = append(written, entry)
		entrySize = len(entry) + 1
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Each entry on its own exceeds MaxSize, so every file holds exactly one.
	files, _ := filepath.Glob(filepath.Join(tmpDir, "test_*"))
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("failed to stat %s: %v", path, err)
		}
		if info.Size() > int64(entrySize) {
			t.Errorf("%s is %d bytes, rotation stopped at MaxSize %d", filepath.Base(path), info.Size(), maxSize)
		}
	}

	assertNewestEntriesReadable(t, written, readRotatedEntries(t, tmpDir, "test"))
}

// TestRotatingFileWriter_RotationDuringCompression pins the ordering hazard
// directly: rotate spawns the compressor before choosing the next index, so an
// index that lands back on the file being compressed hands the writer a path
// the compressor is about to unlink.
func TestRotatingFileWriter_RotationDuringCompression(t *testing.T) {
	tmpDir := t.TempDir()
	day := time.Now().Format("20060102")

	// A directory pruning has already been through: index 0000 is gone, so the
	// file count (2) equals the index of the active file rotation is about to
	// compress.
	writeGzipFile(t, filepath.Join(tmpDir, "test_"+day+"_0001.log.gz"), "archived entry\n")
	if err := os.WriteFile(filepath.Join(tmpDir, "test_"+day+"_0002.log"), []byte("active entry\n"), 0o600); err != nil {
		t.Fatalf("failed to seed active file: %v", err)
	}

	w, err := NewRotatingFileWriter(RotatingFileWriterConfig{
		Dir:           tmpDir,
		Prefix:        "test",
		Suffix:        ".log",
		ArchiveSuffix: ".log.gz",
		MaxSize:       2 << 20,
		MaxFiles:      8,
	})
	if err != nil {
		t.Fatalf("NewRotatingFileWriter failed: %v", err)
	}

	// Incompressible payload, so the compression started by this rotation is
	// still running when the writer picks the next index.
	if _, err := w.Write(append(incompressibleBytes(2<<20), '\n')); err != nil {
		t.Fatalf("bulk Write failed: %v", err)
	}

	// Give the compressor time to finish and remove its source path.
	time.Sleep(500 * time.Millisecond)

	const survivor = "written after the rotation"
	if _, err := w.Write([]byte(survivor + "\n")); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if entries := readRotatedEntries(t, tmpDir, "test"); !slices.Contains(entries, survivor) {
		t.Errorf("entry written after rotation was lost: the compressor unlinked the reopened file")
	}
}

// TestRotatingFileWriter_PrunesByRotationOrder pins which files pruning keeps
// when an archive was written long after the rotation that produced it: its
// modification time is then newer than that of the files rotated after it, so
// ordering by mtime keeps stale data and drops the newest entries.
func TestRotatingFileWriter_PrunesByRotationOrder(t *testing.T) {
	tmpDir := t.TempDir()
	day := time.Now().Format("20060102")

	for i := range 5 {
		writeGzipFile(t, filepath.Join(tmpDir, fmt.Sprintf("test_%s_%04d.log.gz", day, i)), fmt.Sprintf("entry-%d\n", i))
	}
	// The oldest archive finished compressing last, so it carries the newest mtime.
	oldest := filepath.Join(tmpDir, fmt.Sprintf("test_%s_0000.log.gz", day))
	now := time.Now()
	if err := os.Chtimes(oldest, now.Add(time.Hour), now.Add(time.Hour)); err != nil {
		t.Fatalf("failed to set mtime: %v", err)
	}

	w, err := NewRotatingFileWriter(RotatingFileWriterConfig{
		Dir:           tmpDir,
		Prefix:        "test",
		Suffix:        ".log",
		ArchiveSuffix: ".log.gz",
		MaxSize:       1024,
		MaxFiles:      3,
	})
	if err != nil {
		t.Fatalf("NewRotatingFileWriter failed: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	matches, _ := filepath.Glob(filepath.Join(tmpDir, "test_*"))
	sort.Strings(matches)
	got := make([]string, 0, len(matches))
	for _, path := range matches {
		got = append(got, filepath.Base(path))
	}

	want := []string{
		fmt.Sprintf("test_%s_0003.log.gz", day),
		fmt.Sprintf("test_%s_0004.log.gz", day),
		fmt.Sprintf("test_%s_0005.log", day),
	}
	if !slices.Equal(got, want) {
		t.Errorf("pruning kept the wrong files\ngot:  %v\nwant: %v", got, want)
	}
}

func writeGzipFile(t *testing.T, path, content string) {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(content)); err != nil {
		t.Fatalf("failed to gzip %s: %v", path, err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("failed to close gzip writer for %s: %v", path, err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
}

// incompressibleBytes returns n bytes of printable noise that gzip cannot
// shrink, so compressing them takes measurable time.
func incompressibleBytes(n int) []byte {
	rnd := rand.New(rand.NewSource(1)) //nolint:gosec // test fixture, not security material
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[rnd.Intn(len(alphabet))]
	}
	return b
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
