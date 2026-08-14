package proxy

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxFileSize = 50 * 1024 * 1024 // 50MB
	defaultMaxFiles    = 5

	// maxRotateAttempts bounds the search for a free index when a candidate is
	// taken - by a file already on disk, or by a compression still holding it.
	maxRotateAttempts = 100
)

// RotatingFileWriterConfig configures a RotatingFileWriter
type RotatingFileWriterConfig struct {
	Dir           string // Directory to write files
	Prefix        string // File name prefix (e.g., "requests", "proxy")
	Suffix        string // File name suffix for active file (e.g., ".jsonl")
	ArchiveSuffix string // File name suffix for rotated files (e.g., ".jsonl.gz"), empty to disable compression
	MaxSize       int64  // Max file size before rotation (bytes)
	MaxFiles      int    // Max number of files to keep
}

// RotatingFileWriter writes to rotating log files.
// Active file is written uncompressed for efficient tailing.
// Rotated files are compressed with gzip.
type RotatingFileWriter struct {
	cfg         RotatingFileWriterConfig
	mu          sync.Mutex
	file        *os.File
	bufWriter   *bufio.Writer
	written     int64
	fileIndex   int
	currentPath string // path to current active file

	// compressMu guards compressing, the set of paths an in-flight compression
	// owns: its source file, which it removes when done, and its archive, which
	// it truncates. Rotation must never hand the writer one of those paths.
	// Held independently of mu - a compression goroutine takes neither.
	compressMu  sync.Mutex
	compressing map[string]struct{}
	compressWG  sync.WaitGroup
}

func NewRotatingFileWriter(cfg RotatingFileWriterConfig) (*RotatingFileWriter, error) {
	if cfg.MaxSize <= 0 {
		cfg.MaxSize = defaultMaxFileSize
	}
	if cfg.MaxFiles <= 0 {
		cfg.MaxFiles = defaultMaxFiles
	}

	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	w := &RotatingFileWriter{
		cfg: cfg,
	}

	// Try to reuse the last file if it's under the size limit
	if err := w.openOrRotate(); err != nil {
		return nil, err
	}

	return w, nil
}

// CurrentPath returns the path to the current active log file.
func (w *RotatingFileWriter) CurrentPath() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.currentPath
}

func (w *RotatingFileWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.bufWriter == nil {
		return 0, fmt.Errorf("writer is closed")
	}

	n, err = w.bufWriter.Write(p)
	if err != nil {
		return n, fmt.Errorf("failed to write: %w", err)
	}

	// Flush to ensure data is visible for tailing
	if err := w.bufWriter.Flush(); err != nil {
		return n, err
	}

	w.written += int64(n)

	if w.written >= w.cfg.MaxSize {
		if err := w.rotate(); err != nil {
			return n, err
		}
	}

	return n, nil
}

// openOrRotate tries to reuse the last file if under size limit, otherwise creates new
func (w *RotatingFileWriter) openOrRotate() error {
	// Find the latest uncompressed file for today
	lastFile, lastSize := w.findLastFile()

	if lastFile != "" && lastSize < w.cfg.MaxSize {
		// Reuse existing uncompressed file
		file, err := os.OpenFile(lastFile, os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			// Fall back to creating new file
			return w.rotate()
		}

		w.file = file
		w.bufWriter = bufio.NewWriter(file)
		w.written = lastSize
		w.currentPath = lastFile
		return nil
	}

	return w.rotate()
}

func (w *RotatingFileWriter) rotate() error {
	oldPath := w.currentPath

	// Close current file
	if w.bufWriter != nil {
		_ = w.bufWriter.Flush()
		w.bufWriter = nil
	}
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}

	// Compress the old file if configured and it exists
	if oldPath != "" && w.cfg.ArchiveSuffix != "" {
		w.startCompression(oldPath)
	}

	index := w.findNextIndex()

	// O_EXCL rather than O_APPEND: rotation always wants a file of its own, so a
	// name that is taken - by a leftover, or by another writer on the same
	// directory - moves to the next index instead of being written into.
	var (
		file     *os.File
		filename string
	)
	for attempt := 0; ; attempt++ {
		if attempt >= maxRotateAttempts {
			return fmt.Errorf("failed to create log file: no free index at or above %d in %s", index-attempt, w.cfg.Dir)
		}

		filename = w.filePath(index)
		if w.compressionOwns(filename) || w.compressionOwns(w.archivePath(filename)) {
			index++
			continue
		}

		f, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			file = f
			break
		}
		if !os.IsExist(err) {
			return fmt.Errorf("failed to create log file: %w", err)
		}
		index++
	}

	w.fileIndex = index
	w.file = file
	w.bufWriter = bufio.NewWriter(file)
	w.written = 0
	w.currentPath = filename

	w.pruneOldFiles()

	return nil
}

// filePath returns the active file name for an index on today's date.
func (w *RotatingFileWriter) filePath(index int) string {
	return filepath.Join(w.cfg.Dir, fmt.Sprintf("%s_%s_%04d%s",
		w.cfg.Prefix,
		time.Now().Format("20060102"),
		index,
		w.cfg.Suffix,
	))
}

// archivePath returns the compressed name for an active file path.
func (w *RotatingFileWriter) archivePath(srcPath string) string {
	return strings.TrimSuffix(srcPath, w.cfg.Suffix) + w.cfg.ArchiveSuffix
}

// startCompression compresses srcPath in the background, claiming both the
// source and the archive path for the duration so rotation cannot reopen a file
// the compressor is about to read, truncate or unlink.
func (w *RotatingFileWriter) startCompression(srcPath string) {
	archivePath := w.archivePath(srcPath)

	w.compressMu.Lock()
	if w.compressing == nil {
		w.compressing = make(map[string]struct{})
	}
	w.compressing[srcPath] = struct{}{}
	w.compressing[archivePath] = struct{}{}
	w.compressMu.Unlock()

	w.compressWG.Add(1)
	go func() {
		defer func() {
			w.compressMu.Lock()
			delete(w.compressing, srcPath)
			delete(w.compressing, archivePath)
			w.compressMu.Unlock()
			w.compressWG.Done()
		}()

		w.compressFile(srcPath)
	}()
}

// compressionOwns reports whether an in-flight compression holds path.
func (w *RotatingFileWriter) compressionOwns(path string) bool {
	w.compressMu.Lock()
	defer w.compressMu.Unlock()
	_, ok := w.compressing[path]
	return ok
}

// compressFile compresses a file with gzip and removes the original
func (w *RotatingFileWriter) compressFile(srcPath string) {
	archivePath := w.archivePath(srcPath)

	src, err := os.Open(srcPath)
	if err != nil {
		return
	}
	defer func() { _ = src.Close() }()

	dst, err := os.OpenFile(archivePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = dst.Close() }()

	gz := gzip.NewWriter(dst)
	defer func() { _ = gz.Close() }()

	if _, err := io.Copy(gz, src); err != nil {
		_ = os.Remove(archivePath) // cleanup on error
		return
	}

	if err := gz.Close(); err != nil {
		_ = os.Remove(archivePath)
		return
	}

	if err := dst.Close(); err != nil {
		_ = os.Remove(archivePath)
		return
	}

	// Remove original uncompressed file
	_ = os.Remove(srcPath)
}

// findNextIndex returns one past the highest index present for today, counting
// both active (.jsonl) and archived (.jsonl.gz) files. Deriving it from the file
// count instead pins it at MaxFiles once pruning caps the directory, which hands
// rotation the index of the file it is archiving away.
func (w *RotatingFileWriter) findNextIndex() int {
	today := time.Now().Format("20060102")
	namePrefix := fmt.Sprintf("%s_%s_", w.cfg.Prefix, today)
	pattern := filepath.Join(w.cfg.Dir, namePrefix+"*")
	matches, _ := filepath.Glob(pattern)

	next := 0
	for _, match := range matches {
		index, ok := parseFileIndex(filepath.Base(match), namePrefix)
		if ok && index >= next {
			next = index + 1
		}
	}
	return next
}

// parseFileIndex reads the numeric index out of a rotated file name of the form
// <prefix><index><suffix>.
func parseFileIndex(name, namePrefix string) (int, bool) {
	rest, ok := strings.CutPrefix(name, namePrefix)
	if !ok {
		return 0, false
	}

	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}

	index, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0, false
	}
	return index, true
}

// findLastFile returns the most recent uncompressed file for today and its size
func (w *RotatingFileWriter) findLastFile() (string, int64) {
	today := time.Now().Format("20060102")
	// Look for uncompressed active files only
	pattern := filepath.Join(w.cfg.Dir, fmt.Sprintf("%s_%s_*%s", w.cfg.Prefix, today, w.cfg.Suffix))
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return "", 0
	}

	// Sort to get the latest (highest index)
	sort.Strings(matches)
	lastFile := matches[len(matches)-1]

	info, err := os.Stat(lastFile)
	if err != nil {
		return "", 0
	}

	return lastFile, info.Size()
}

func (w *RotatingFileWriter) pruneOldFiles() {
	// Prune both active and archived files
	pattern := filepath.Join(w.cfg.Dir, w.cfg.Prefix+"*")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) <= w.cfg.MaxFiles {
		return
	}

	// Oldest first by name, which is rotation order: the date and the zero-padded
	// index sort chronologically. Modification time does not - an archive written
	// well after the rotation that produced it carries a newer mtime than the
	// files that came after it, and would outrank them here.
	sort.Strings(matches)

	toRemove := len(matches) - w.cfg.MaxFiles
	for i := range toRemove {
		// A file an in-flight compression owns is left alone: unlinking the source
		// it is reading does not stop it, so the archive would be written after
		// the prune and put those entries back. Removal stops there rather than
		// skipping ahead, so pruning only ever drops the oldest run of files -
		// taking a newer one while keeping this would leave a hole in the log.
		// The rest goes on a later pass, or on the one Close runs once
		// compression has finished.
		if w.compressionOwns(matches[i]) {
			break
		}
		_ = os.Remove(matches[i])
	}
}

func (w *RotatingFileWriter) Close() error {
	firstErr := w.closeFile()

	// Let the rotations still compressing finish, so shutdown does not leave a
	// truncated archive behind or an unlinked file the caller is about to read.
	w.compressWG.Wait()

	// Those archives were skipped by pruning while they were being written, so
	// the file cap is enforced once more now that nothing holds them.
	w.mu.Lock()
	w.pruneOldFiles()
	w.mu.Unlock()

	return firstErr
}

func (w *RotatingFileWriter) closeFile() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	var firstErr error
	if w.bufWriter != nil {
		if err := w.bufWriter.Flush(); err != nil {
			firstErr = err
		}
		w.bufWriter = nil
	}
	if w.file != nil {
		if err := w.file.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		w.file = nil
	}

	return firstErr
}

func (w *RotatingFileWriter) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.bufWriter != nil {
		if err := w.bufWriter.Flush(); err != nil {
			return err
		}
	}
	if w.file != nil {
		return w.file.Sync()
	}
	return nil
}
