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
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
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

// claimState is what a second process sees when it looks at a log file on its
// way to pruning it: gone, free to unlink, or held by a live writer.
type claimState string

const (
	claimGone claimState = "gone"
	claimFree claimState = "free"
	claimHeld claimState = "held"
)

func sampleClaim(path string) claimState {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return claimGone
	}
	defer func() { _ = f.Close() }()

	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return claimHeld
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return claimFree
}

// TestDupClaimedFile_KeepsClaimAcrossClose pins the mechanism rotation relies
// on: flock lives on the open file description, so a duplicate descriptor holds
// the same lock and closing the original releases nothing.
func TestDupClaimedFile_KeepsClaimAcrossClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active.log")

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !claimFile(f) {
		t.Fatal("could not claim a file nothing else holds")
	}
	if _, err := f.WriteString("entry\n"); err != nil {
		t.Fatalf("write: %v", err)
	}

	dup := dupClaimedFile(f)
	if dup == nil {
		t.Fatal("dupClaimedFile returned nil")
	}
	defer func() { _ = dup.Close() }()

	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := sampleClaim(path); got != claimHeld {
		t.Errorf("claim after closing the original: got %q, want %q", got, claimHeld)
	}

	// The duplicate must also be readable from the start, since it is what the
	// compressor copies out of.
	if _, err := dup.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	got, err := io.ReadAll(dup)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "entry\n" {
		t.Errorf("read %q from the duplicate, want %q", got, "entry\n")
	}

	if err := dup.Close(); err != nil {
		t.Fatalf("close duplicate: %v", err)
	}
	if got := sampleClaim(path); got != claimFree {
		t.Errorf("claim after closing every descriptor: got %q, want %q", got, claimFree)
	}
}

// TestDupClaimedFile_IsCloseOnExec pins the other half of the duplicate's
// contract. Every descriptor os.OpenFile produces is close-on-exec, which is
// what keeps a host log out of the processes devsandbox spawns while a session
// runs; a plain dup(2) drops that flag, so the duplicate rotation hands the
// compressor would ride into every child forked during the compression - a
// read-write descriptor on a host file, and a share of the flock that outlives
// the compressor closing its own copy.
func TestDupClaimedFile_IsCloseOnExec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active.log")

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	if !claimFile(f) {
		t.Fatal("could not claim a file nothing else holds")
	}

	dup := dupClaimedFile(f)
	if dup == nil {
		t.Fatal("dupClaimedFile returned nil")
	}
	defer func() { _ = dup.Close() }()

	flags, err := unix.FcntlInt(dup.Fd(), unix.F_GETFD, 0)
	if err != nil {
		t.Fatalf("F_GETFD on the duplicate: %v", err)
	}
	if flags&unix.FD_CLOEXEC == 0 {
		t.Error("the duplicated descriptor is not close-on-exec; it leaks into every process forked during compression")
	}
}

// TestStartCompression_ArchivesTheHandedOverDescriptor covers the window between
// rotation closing the active file and the compressor claiming it. A compressor
// that reopens the path finds the file unclaimed in between, which is exactly
// what a concurrent session's pruning tests before unlinking - so the entries
// about to be archived can be taken out from under it, and the compression then
// finds nothing to open and writes no archive at all.
//
// The unlink here is that prune, applied at the one instant it does damage. What
// makes it survivable is the descriptor rotation hands over: it carries the
// claim and it names the inode, so the archive is written from the bytes
// regardless of what happened to the name.
func TestStartCompression_ArchivesTheHandedOverDescriptor(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "test_0001.log")
	archivePath := filepath.Join(tmpDir, "test_0001.log.gz")

	const entry = "entry written before the rotation"
	f, err := os.OpenFile(srcPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	if !claimFile(f) {
		t.Fatal("could not claim a file nothing else holds")
	}
	if _, err := f.WriteString(entry + "\n"); err != nil {
		t.Fatalf("write source: %v", err)
	}

	// What rotate does: duplicate before closing, so the claim is never dropped.
	claimed := dupClaimedFile(f)
	if claimed == nil {
		t.Fatal("dupClaimedFile returned nil")
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close source: %v", err)
	}

	// The concurrent session, pruning in the instant the old code left open.
	if err := os.Remove(srcPath); err != nil {
		t.Fatalf("remove source: %v", err)
	}

	w := &RotatingFileWriter{cfg: RotatingFileWriterConfig{
		Dir: tmpDir, Prefix: "test", Suffix: ".log", ArchiveSuffix: ".log.gz",
	}}
	w.startCompression(srcPath, claimed)
	w.compressWG.Wait()

	if got := readLogFileLines(t, archivePath); !slices.Equal(got, []string{entry}) {
		t.Errorf("archive holds %v, want %v", got, []string{entry})
	}
}

// TestRotatingFileWriter_RotationKeepsTheArchivedFileClaimed is the end-to-end
// half: while a rotation's compression is in flight, the file it is reading must
// look claimed to the other session, since claimedByAnotherWriter is the whole
// of what stops that session's pruning from removing it.
func TestRotatingFileWriter_RotationKeepsTheArchivedFileClaimed(t *testing.T) {
	tmpDir := t.TempDir()

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

	const seeded = "entry written before the rotation"
	if _, err := w.Write([]byte(seeded + "\n")); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	rotatedAway := w.CurrentPath()

	// Incompressible, so the compression this rotation starts is still running
	// when the claim is sampled.
	if _, err := w.Write(append(incompressibleBytes(2<<20), '\n')); err != nil {
		t.Fatalf("bulk Write failed: %v", err)
	}

	if got := sampleClaim(rotatedAway); got == claimFree {
		t.Error("the file being archived was free for the taking: another session's pruning would have unlinked it")
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if entries := readRotatedEntries(t, tmpDir, "test"); !slices.Contains(entries, seeded) {
		t.Error("the archived entry did not survive the rotation")
	}
}

// lockPath opens path and holds the same advisory lock a writer takes, standing
// in for a second process working in the log directory. flock is held per open
// file description, so a lock taken here conflicts with this process's other
// descriptors exactly as another process's would.
func lockPath(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if !claimFile(f) {
		t.Fatalf("could not claim %s", path)
	}
}

// TestCompressFile_ArchiveHeldByAnotherWriter pins that a compression whose
// archive path is held elsewhere writes nothing. The claim is what keeps a
// second process's pruning off a half-written archive; taking it after an
// O_TRUNC would destroy the contents it exists to protect.
func TestCompressFile_ArchiveHeldByAnotherWriter(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "test_0001.log")
	archivePath := filepath.Join(tmpDir, "test_0001.log.gz")

	if err := os.WriteFile(srcPath, []byte("entry\n"), 0o600); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	const held = "another writer's bytes"
	if err := os.WriteFile(archivePath, []byte(held), 0o600); err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	lockPath(t, archivePath)

	w := &RotatingFileWriter{cfg: RotatingFileWriterConfig{
		Dir: tmpDir, Prefix: "test", Suffix: ".log", ArchiveSuffix: ".log.gz",
	}}
	w.compressFile(srcPath, nil)

	got, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if string(got) != held {
		t.Errorf("archive held by another writer was overwritten: got %q, want %q", got, held)
	}
	if _, err := os.Stat(srcPath); err != nil {
		t.Errorf("source removed although nothing archived it: %v", err)
	}
}

// TestCompressFile_SourceHeldByAnotherWriter pins that a source another writer
// has claimed is left alone: those bytes belong to that writer now, and
// removing the file would unlink one it is still appending to.
func TestCompressFile_SourceHeldByAnotherWriter(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "test_0001.log")
	archivePath := filepath.Join(tmpDir, "test_0001.log.gz")

	if err := os.WriteFile(srcPath, []byte("entry\n"), 0o600); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	lockPath(t, srcPath)

	w := &RotatingFileWriter{cfg: RotatingFileWriterConfig{
		Dir: tmpDir, Prefix: "test", Suffix: ".log", ArchiveSuffix: ".log.gz",
	}}
	w.compressFile(srcPath, nil)

	if _, err := os.Stat(srcPath); err != nil {
		t.Errorf("source claimed by another writer was removed: %v", err)
	}
	if _, err := os.Stat(archivePath); err == nil {
		t.Error("archived a file another writer is still appending to")
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

// TestRotatingFileWriter_ConcurrentWritersDoNotShareAFile covers two sessions
// for one project: the proxy log directory is derived from the sandbox root, so
// both writers land in it. Appending to one file made each count only its own
// bytes toward MaxSize, and the first to rotate handed the shared file to the
// compressor, which unlinked it while the other kept writing to the dead inode.
func TestRotatingFileWriter_ConcurrentWritersDoNotShareAFile(t *testing.T) {
	dir := t.TempDir()
	cfg := RotatingFileWriterConfig{
		Dir:           dir,
		Prefix:        "requests",
		Suffix:        ".jsonl",
		ArchiveSuffix: ".jsonl.gz",
		MaxSize:       1 << 20,
		MaxFiles:      10,
	}

	first, err := NewRotatingFileWriter(cfg)
	if err != nil {
		t.Fatalf("first writer: %v", err)
	}
	defer func() { _ = first.Close() }()
	if _, err := first.Write([]byte("first-session\n")); err != nil {
		t.Fatalf("first write: %v", err)
	}

	second, err := NewRotatingFileWriter(cfg)
	if err != nil {
		t.Fatalf("second writer: %v", err)
	}
	defer func() { _ = second.Close() }()
	if _, err := second.Write([]byte("second-session\n")); err != nil {
		t.Fatalf("second write: %v", err)
	}

	if first.CurrentPath() == second.CurrentPath() {
		t.Fatalf("both writers took %s", first.CurrentPath())
	}

	// Rotating the first must not disturb the file the second still holds.
	first.mu.Lock()
	rotateErr := first.rotate()
	first.mu.Unlock()
	if rotateErr != nil {
		t.Fatalf("rotate: %v", rotateErr)
	}
	first.compressWG.Wait()

	held := second.CurrentPath()
	if _, err := os.Stat(held); err != nil {
		t.Fatalf("live writer's file was removed by the other's rotation: %v", err)
	}
	if _, err := second.Write([]byte("still-here\n")); err != nil {
		t.Fatalf("second write after the other rotated: %v", err)
	}
	got, err := os.ReadFile(held)
	if err != nil {
		t.Fatalf("read live writer's file: %v", err)
	}
	for _, want := range []string{"second-session", "still-here"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("live writer's file lost %q, holds %q", want, got)
		}
	}
}

// A writer that exited leaves nothing holding its file, so the next session
// reuses it rather than starting a new index on every launch.
func TestRotatingFileWriter_ReusesTheFileOfAnExitedWriter(t *testing.T) {
	dir := t.TempDir()
	cfg := RotatingFileWriterConfig{
		Dir:      dir,
		Prefix:   "requests",
		Suffix:   ".jsonl",
		MaxSize:  1 << 20,
		MaxFiles: 10,
	}

	first, err := NewRotatingFileWriter(cfg)
	if err != nil {
		t.Fatalf("first writer: %v", err)
	}
	if _, err := first.Write([]byte("before-exit\n")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	path := first.CurrentPath()
	if err := first.Close(); err != nil {
		t.Fatalf("close first writer: %v", err)
	}

	second, err := NewRotatingFileWriter(cfg)
	if err != nil {
		t.Fatalf("second writer: %v", err)
	}
	defer func() { _ = second.Close() }()

	if second.CurrentPath() != path {
		t.Errorf("second writer took %s, want the exited writer's %s", second.CurrentPath(), path)
	}
}
