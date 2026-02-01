package proxy

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxFileSize = 50 * 1024 * 1024 // 50MB
	defaultMaxFiles    = 5
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
}

func NewRotatingFileWriter(cfg RotatingFileWriterConfig) (*RotatingFileWriter, error) {
	if cfg.MaxSize <= 0 {
		cfg.MaxSize = defaultMaxFileSize
	}
	if cfg.MaxFiles <= 0 {
		cfg.MaxFiles = defaultMaxFiles
	}

	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
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
		file, err := os.OpenFile(lastFile, os.O_WRONLY|os.O_APPEND, 0o644)
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
		go w.compressFile(oldPath)
	}

	w.fileIndex = w.findNextIndex()

	filename := filepath.Join(w.cfg.Dir, fmt.Sprintf("%s_%s_%04d%s",
		w.cfg.Prefix,
		time.Now().Format("20060102"),
		w.fileIndex,
		w.cfg.Suffix,
	))

	file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("failed to create log file: %w", err)
	}

	w.file = file
	w.bufWriter = bufio.NewWriter(file)
	w.written = 0
	w.currentPath = filename

	w.pruneOldFiles()

	return nil
}

// compressFile compresses a file with gzip and removes the original
func (w *RotatingFileWriter) compressFile(srcPath string) {
	// Build archive path by replacing suffix
	archivePath := strings.TrimSuffix(srcPath, w.cfg.Suffix) + w.cfg.ArchiveSuffix

	src, err := os.Open(srcPath)
	if err != nil {
		return
	}
	defer func() { _ = src.Close() }()

	dst, err := os.Create(archivePath)
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

func (w *RotatingFileWriter) findNextIndex() int {
	today := time.Now().Format("20060102")
	// Count both active (.jsonl) and archived (.jsonl.gz) files
	pattern := filepath.Join(w.cfg.Dir, fmt.Sprintf("%s_%s_*", w.cfg.Prefix, today))
	matches, _ := filepath.Glob(pattern)
	return len(matches)
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

	type fileInfo struct {
		path    string
		modTime time.Time
	}
	files := make([]fileInfo, 0, len(matches))
	for _, path := range matches {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		files = append(files, fileInfo{path: path, modTime: info.ModTime()})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})

	toRemove := len(files) - w.cfg.MaxFiles
	for i := range toRemove {
		_ = os.Remove(files[i].path)
	}
}

func (w *RotatingFileWriter) Close() error {
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
