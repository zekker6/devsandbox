package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/olekukonko/tablewriter"

	"devsandbox/internal/notice"
	"devsandbox/internal/proxy"
	"devsandbox/internal/sandbox"
)

func newLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "View sandbox logs",
		Long: `View proxy and internal logs for sandboxes.

Subcommands:
  proxy     View HTTP/HTTPS request logs captured in proxy mode
  internal  View internal logs (proxy server errors, logging failures)`,
		Example: `  devsandbox logs proxy                      # View proxy request logs
  devsandbox logs proxy -f                   # Follow/tail proxy logs
  devsandbox logs proxy --since 1h           # Logs from last hour
  devsandbox logs internal                   # View internal logs
  devsandbox logs internal --type logging    # View logging errors only`,
	}

	cmd.AddCommand(newLogsProxyCmd())
	cmd.AddCommand(newLogsInternalCmd())

	return cmd
}

// ProxyLogFilter defines filters for proxy log entries.
type ProxyLogFilter struct {
	URL        string
	Method     string
	StatusCode int
	StatusMin  int
	StatusMax  int
	Since      time.Time
	Until      time.Time
	ErrorsOnly bool
}

// Match returns true if the entry matches all filter criteria.
func (f *ProxyLogFilter) Match(entry *proxy.RequestLog) bool {
	// URL filter (substring match)
	if f.URL != "" && !strings.Contains(entry.URL, f.URL) {
		return false
	}

	// Method filter (case-insensitive)
	if f.Method != "" && !strings.EqualFold(entry.Method, f.Method) {
		return false
	}

	// Status code filters
	if f.StatusCode > 0 && entry.StatusCode != f.StatusCode {
		return false
	}
	if f.StatusMin > 0 && entry.StatusCode < f.StatusMin {
		return false
	}
	if f.StatusMax > 0 && entry.StatusCode > f.StatusMax {
		return false
	}

	// Time filters
	if !f.Since.IsZero() && entry.Timestamp.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && entry.Timestamp.After(f.Until) {
		return false
	}

	// Errors only
	if f.ErrorsOnly && entry.Error == "" && entry.StatusCode < 400 {
		return false
	}

	return true
}

// ParseTimeFilter parses various time formats into a time.Time.
// Supported formats:
// - RFC3339: 2024-01-15T10:30:00Z
// - Date: 2024-01-15 (start of day)
// - Relative: 1h, 30m, 2d, 1w (from now)
// - Keywords: today, yesterday
func ParseTimeFilter(s string) (time.Time, error) {
	s = strings.TrimSpace(strings.ToLower(s))

	// Keywords
	now := time.Now()
	switch s {
	case "today":
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()), nil
	case "yesterday":
		yesterday := now.AddDate(0, 0, -1)
		return time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 0, 0, 0, 0, now.Location()), nil
	case "now":
		return now, nil
	}

	// Relative time (e.g., 1h, 30m, 2d, 1w)
	if matched, _ := regexp.MatchString(`^\d+[smhdw]$`, s); matched {
		unit := s[len(s)-1]
		value, _ := strconv.Atoi(s[:len(s)-1])

		var duration time.Duration
		switch unit {
		case 's':
			duration = time.Duration(value) * time.Second
		case 'm':
			duration = time.Duration(value) * time.Minute
		case 'h':
			duration = time.Duration(value) * time.Hour
		case 'd':
			duration = time.Duration(value) * 24 * time.Hour
		case 'w':
			duration = time.Duration(value) * 7 * 24 * time.Hour
		}
		return now.Add(-duration), nil
	}

	// RFC3339
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}

	// Date only (YYYY-MM-DD)
	if t, err := time.ParseInLocation("2006-01-02", s, now.Location()); err == nil {
		return t, nil
	}

	return time.Time{}, fmt.Errorf("invalid time format: %s (use RFC3339, YYYY-MM-DD, relative like 1h/2d, or today/yesterday)", s)
}

// ParseStatusFilter parses status code filter strings.
// Supported formats:
// - Single: 200
// - Range: 400-599
// - Comparison: >=400, <500
func ParseStatusFilter(s string) (exact, min, max int, err error) {
	s = strings.TrimSpace(s)

	// Range: 400-599
	if strings.Contains(s, "-") {
		parts := strings.Split(s, "-")
		if len(parts) != 2 {
			return 0, 0, 0, fmt.Errorf("invalid status range: %s", s)
		}
		min, err = strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid status range: %s", s)
		}
		max, err = strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid status range: %s", s)
		}
		return 0, min, max, nil
	}

	// Comparison: >=400, <500
	if after, ok := strings.CutPrefix(s, ">="); ok {
		min, err = strconv.Atoi(after)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid status filter: %s", s)
		}
		return 0, min, 0, nil
	}
	if after, ok := strings.CutPrefix(s, ">"); ok {
		val, err := strconv.Atoi(after)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid status filter: %s", s)
		}
		return 0, val + 1, 0, nil
	}
	if after, ok := strings.CutPrefix(s, "<="); ok {
		max, err = strconv.Atoi(after)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid status filter: %s", s)
		}
		return 0, 0, max, nil
	}
	if after, ok := strings.CutPrefix(s, "<"); ok {
		val, err := strconv.Atoi(after)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid status filter: %s", s)
		}
		return 0, 0, val - 1, nil
	}

	// Single value
	exact, err = strconv.Atoi(s)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid status code: %s", s)
	}
	return exact, 0, 0, nil
}

func newLogsProxyCmd() *cobra.Command {
	var (
		sandboxName  string
		last         int
		follow       bool
		jsonOutput   bool
		showBody     bool
		filterURL    string
		filterMethod string
		filterStatus string
		since        string
		until        string
		errorsOnly   bool
		noColor      bool
		compact      bool
		stats        bool
	)

	cmd := &cobra.Command{
		Use:   "proxy [sandbox-name]",
		Short: "View proxy request logs",
		Long: `View HTTP/HTTPS request logs captured by proxy mode.

If no sandbox name is provided, uses the current directory's sandbox.

Time filters support multiple formats:
  - RFC3339: 2024-01-15T10:30:00Z
  - Date: 2024-01-15 (start of day)
  - Relative: 1h, 30m, 2d, 1w (from now)
  - Keywords: today, yesterday

Status filters support:
  - Single value: --status 200
  - Range: --status 400-599
  - Comparison: --status ">=400"`,
		Example: `  devsandbox logs proxy                      # All logs for current project
  devsandbox logs proxy myproject            # Logs for specific sandbox
  devsandbox logs proxy --last 50            # Show last 50 requests
  devsandbox logs proxy -f                   # Follow/tail logs
  devsandbox logs proxy --since 1h           # Logs from last hour
  devsandbox logs proxy --since today        # Logs from today
  devsandbox logs proxy --errors             # Show only errors
  devsandbox logs proxy --status 400-599     # Filter by status range
  devsandbox logs proxy --url /api --method POST  # Filter by URL and method
  devsandbox logs proxy --json               # JSON output
  devsandbox logs proxy --compact            # Compact one-line format
  devsandbox logs proxy --stats              # Show statistics summary`,
		RunE: func(cmd *cobra.Command, args []string) error {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return err
			}

			// Build filter
			filter := &ProxyLogFilter{
				URL:        filterURL,
				Method:     filterMethod,
				ErrorsOnly: errorsOnly,
			}

			// Parse time filters
			if since != "" {
				t, err := ParseTimeFilter(since)
				if err != nil {
					return err
				}
				filter.Since = t
			}
			if until != "" {
				t, err := ParseTimeFilter(until)
				if err != nil {
					return err
				}
				filter.Until = t
			}

			// Parse status filter
			if filterStatus != "" {
				exact, min, max, err := ParseStatusFilter(filterStatus)
				if err != nil {
					return err
				}
				filter.StatusCode = exact
				filter.StatusMin = min
				filter.StatusMax = max
			}

			// Determine sandbox name
			name := sandboxName
			if len(args) > 0 {
				name = args[0]
			}
			if name == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				name = sandbox.GenerateSandboxName(cwd)
			}

			baseDir := sandbox.SandboxBasePath(homeDir)
			sandboxRoot := filepath.Join(baseDir, name)
			logDir := filepath.Join(sandboxRoot, proxy.LogBaseDirName, proxy.ProxyLogDirName)

			// Check if log directory exists
			if _, err := os.Stat(logDir); os.IsNotExist(err) {
				return fmt.Errorf("no logs found for sandbox %q (run with --proxy to capture logs)", name)
			}

			if follow {
				return followProxyLogs(logDir, filter, jsonOutput, showBody, compact, noColor)
			}

			return viewProxyLogs(logDir, filter, last, jsonOutput, showBody, compact, noColor, stats)
		},
	}

	cmd.Flags().StringVarP(&sandboxName, "sandbox", "s", "", "Sandbox name (default: current directory)")
	cmd.Flags().IntVarP(&last, "last", "n", 0, "Show only last N entries (default: 100)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow/tail log output")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	cmd.Flags().BoolVar(&showBody, "body", false, "Include request/response bodies")
	cmd.Flags().StringVar(&filterURL, "url", "", "Filter by URL (substring match)")
	cmd.Flags().StringVar(&filterMethod, "method", "", "Filter by HTTP method")
	cmd.Flags().StringVar(&filterStatus, "status", "", "Filter by status code (e.g., 200, 400-599, >=400)")
	cmd.Flags().StringVar(&since, "since", "", "Show logs since time (e.g., 1h, today, 2024-01-15)")
	cmd.Flags().StringVar(&until, "until", "", "Show logs until time")
	cmd.Flags().BoolVar(&errorsOnly, "errors", false, "Show only errors (status >= 400 or error field)")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "Disable colored output")
	cmd.Flags().BoolVar(&compact, "compact", false, "Compact one-line output format")
	cmd.Flags().BoolVar(&stats, "stats", false, "Show summary statistics")

	return cmd
}

func viewProxyLogs(logDir string, filter *ProxyLogFilter, last int, jsonOutput, showBody, compact, noColor, showStats bool) error {
	// Rejected up front rather than clamped: the trims below index
	// entries[len(entries)-last:], so a negative value indexes past the end and
	// panics. `logs internal` guards its own --last with `last > 0`.
	if last < 0 {
		return fmt.Errorf("--last must not be negative, got %d", last)
	}

	// Find both compressed and uncompressed log files
	activePattern := filepath.Join(logDir, proxy.RequestLogPrefix+"*"+proxy.RequestLogSuffix)
	archivePattern := filepath.Join(logDir, proxy.RequestLogPrefix+"*"+proxy.RequestLogArchiveSuffix)

	activeFiles, err := filepath.Glob(activePattern)
	if err != nil {
		return fmt.Errorf("invalid log pattern: %w", err)
	}
	archiveFiles, err := filepath.Glob(archivePattern)
	if err != nil {
		return fmt.Errorf("invalid archive pattern: %w", err)
	}

	files := append(archiveFiles, activeFiles...)
	if len(files) == 0 {
		fmt.Println("No log files found.")
		return nil
	}

	// Sort files by name (chronological order)
	sort.Strings(files)

	// If --last not specified, default to last 100 entries
	if last == 0 {
		last = 100
	}

	// Read entries from files (newest first, stop when we have enough)
	var entries []proxy.RequestLog

	// Process files in reverse order (newest first)
	for _, file := range slices.Backward(files) {

		// Both readers return the entries they got alongside the error, so a
		// file that stops short still contributes what it held rather than
		// being dropped whole.
		fileEntries, oversized, err := readProxyLogFileWithLimit(file, last)
		if oversized > 0 {
			notice.Warn("%s: %d record(s) past the %d MiB line limit were skipped",
				filepath.Base(file), oversized, proxyLogMaxLineBytes>>20)
		}
		if err != nil {
			if len(fileEntries) == 0 {
				notice.Warn("%s could not be read: %v", filepath.Base(file), err)
			} else {
				notice.Warn("%s was read only up to the first unreadable record: %v",
					filepath.Base(file), err)
			}
		}

		// Prepend entries (since we're reading newest first)
		entries = append(fileEntries, entries...)

		// Trim to limit
		if len(entries) > last {
			entries = entries[len(entries)-last:]
		}

		// Stop if we have enough entries
		if len(entries) >= last {
			break
		}
	}

	// Apply filter
	var filtered []proxy.RequestLog
	for _, e := range entries {
		if filter.Match(&e) {
			filtered = append(filtered, e)
		}
	}
	entries = filtered

	if len(entries) == 0 {
		fmt.Println("No matching log entries.")
		return nil
	}

	// Apply --last limit
	if last > 0 && len(entries) > last {
		entries = entries[len(entries)-last:]
	}

	// Output
	if jsonOutput {
		return printProxyLogsJSON(entries, showBody)
	}
	if compact {
		return printProxyLogsCompact(entries, noColor)
	}

	err = printProxyLogsTable(entries, showBody, noColor)
	if err != nil {
		return err
	}

	if showStats {
		printProxyStats(entries)
	}

	return nil
}

func followProxyLogs(logDir string, filter *ProxyLogFilter, jsonOutput, showBody, compact, noColor bool) error {
	// Set up signal handling for graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Pattern for uncompressed active files
	activePattern := filepath.Join(logDir, proxy.RequestLogPrefix+"*"+proxy.RequestLogSuffix)

	// Helper to print an entry
	printEntry := func(e *proxy.RequestLog) {
		if !filter.Match(e) {
			return
		}
		if jsonOutput {
			out := *e
			if !showBody {
				out.RequestBody = nil
				out.ResponseBody = nil
			}
			data, err := json.Marshal(out)
			if err != nil {
				notice.Warn("failed to marshal log entry: %v", err)
				return
			}
			fmt.Println(string(data))
		} else if compact {
			printProxyLogCompactLine(e, noColor)
		} else {
			printProxyLogLine(e, showBody, noColor)
		}
	}

	// Find current active log file
	findCurrentFile := func() string {
		files, err := filepath.Glob(activePattern)
		if err != nil || len(files) == 0 {
			return ""
		}
		sort.Strings(files)
		return files[len(files)-1]
	}

	// Show last 10 entries first (like tail -f)
	currentFile := findCurrentFile()
	if currentFile != "" {
		entries, oversized, err := readUncompressedProxyLogFile(currentFile, 10)
		if oversized > 0 {
			// Counted over the whole file, not over the 10 entries shown: the
			// reader steps over an oversized record wherever it sits.
			notice.Warn("%d record(s) past the %d MiB line limit were skipped",
				oversized, proxyLogMaxLineBytes>>20)
		}
		if err != nil {
			notice.Warn("failed to read recent entries: %v", err)
		}
		for i := range entries {
			printEntry(&entries[i])
		}
	}

	// Track file position for tailing
	var lastFile string
	var lastOffset int64

	// Start at end of current file
	if currentFile != "" {
		if info, err := os.Stat(currentFile); err == nil {
			lastFile = currentFile
			lastOffset = info.Size()
		}
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	notice.Info("Following logs in %s (Ctrl+C to stop)...", logDir)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			currentFile := findCurrentFile()
			if currentFile == "" {
				continue
			}

			// If file changed (rotation), start from beginning of new file
			if currentFile != lastFile {
				lastFile = currentFile
				lastOffset = 0
			}

			// Read new complete lines from file
			entries, newOffset, err := tailProxyLogFile(currentFile, lastOffset)
			if err != nil {
				continue
			}
			lastOffset = newOffset

			for i := range entries {
				printEntry(&entries[i])
			}
		}
	}
}

// tailProxyLogFile reads new entries from an uncompressed JSONL file starting at offset.
// It returns only complete lines and tracks the position after the last complete line,
// so partial lines (from in-progress writes) are not lost.
func tailProxyLogFile(path string, offset int64) ([]proxy.RequestLog, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}

	// No new data
	if info.Size() <= offset {
		return nil, offset, nil
	}

	// Seek to last position
	if offset > 0 {
		_, err = f.Seek(offset, io.SeekStart)
		if err != nil {
			return nil, offset, err
		}
	}

	// Read all available data
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, offset, err
	}

	var entries []proxy.RequestLog
	var bytesConsumed int64

	// Process complete lines only (ending with \n)
	for len(data) > 0 {
		newlineIdx := bytes.IndexByte(data, '\n')
		if newlineIdx == -1 {
			// No complete line remaining - don't advance past partial data
			break
		}

		line := data[:newlineIdx]
		data = data[newlineIdx+1:]
		bytesConsumed += int64(newlineIdx + 1)

		if len(line) == 0 {
			continue
		}

		var entry proxy.RequestLog
		if err := json.Unmarshal(line, &entry); err != nil {
			// Skip malformed lines but still advance past them
			continue
		}
		entries = append(entries, entry)
	}

	return entries, offset + bytesConsumed, nil
}

func readProxyLogFileWithLimit(path string, limit int) ([]proxy.RequestLog, int, error) {
	// Check if file is compressed
	isCompressed := strings.HasSuffix(path, ".gz")

	if isCompressed {
		return readCompressedProxyLogFile(path, limit)
	}
	return readUncompressedProxyLogFile(path, limit)
}

// proxyLogMaxLineBytes bounds a single log line. An entry carries captured
// request and response bodies, which the proxy bounds at max_log_body_bytes
// (256KiB each by default, config.MaxLogBodyBytesLimit at most), so a smaller
// bound would refuse ordinary large records.
const proxyLogMaxLineBytes = 8 << 20

// scanProxyLogEntries appends the newline-delimited entries read from r,
// skipping any line that does not parse and keeping only the newest `limit`
// entries when one is set. The second return is the number of records skipped
// for being past proxyLogMaxLineBytes.
//
// It is deliberately line-based rather than json.Decoder-based: Decode latches
// a *json.SyntaxError permanently and returns it without consuming input, so a
// loop that skips-and-continues on decode errors never advances past corrupt
// bytes. Here a corrupt record costs only itself - and so does an oversized
// one, which under bufio.Scanner ended the scan and took every later entry in
// the file with it.
func scanProxyLogEntries(r io.Reader, limit int, entries []proxy.RequestLog) ([]proxy.RequestLog, int, error) {
	reader := bufio.NewReaderSize(r, 64<<10)
	oversized := 0

	for {
		line, tooLong, err := readBoundedLine(reader, proxyLogMaxLineBytes)
		if tooLong {
			oversized++
		} else if len(line) > 0 {
			var entry proxy.RequestLog
			if jsonErr := json.Unmarshal(line, &entry); jsonErr == nil {
				entries = append(entries, entry)

				// If limit is set, keep only the last N entries (sliding window)
				if limit > 0 && len(entries) > limit*2 {
					entries = entries[len(entries)-limit:]
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return entries, oversized, nil
			}
			return entries, oversized, err
		}
	}
}

// readBoundedLine reads one newline-terminated line, holding at most max bytes.
// A longer line is consumed to its end and reported as oversized with no
// content, so the reader resumes at the next record rather than at whatever
// byte a fixed-size buffer stopped on.
func readBoundedLine(r *bufio.Reader, max int) (line []byte, oversized bool, err error) {
	for {
		chunk, readErr := r.ReadSlice('\n')
		if !oversized && len(line)+len(chunk) > max {
			oversized = true
			line = nil
		}
		if !oversized {
			line = append(line, chunk...)
		}
		if readErr == nil {
			return trimLineEnd(line), oversized, nil
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		// io.EOF or a read error: what came before it is the final line.
		return trimLineEnd(line), oversized, readErr
	}
}

func trimLineEnd(line []byte) []byte {
	line = bytes.TrimSuffix(line, []byte("\n"))
	return bytes.TrimSuffix(line, []byte("\r"))
}

func readUncompressedProxyLogFile(path string, limit int) ([]proxy.RequestLog, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()

	entries, oversized, scanErr := scanProxyLogEntries(f, limit, nil)

	// Final trim if limit is set
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}

	// The entries read before the failure are still returned: the follow path
	// prints them after warning.
	return entries, oversized, scanErr
}

// isBenignArchiveEnd reports whether err is the normal way a rotated archive
// stops rather than a loss of records. A writer killed mid-rotation leaves its
// last member without a trailer, and everything it flushed has already been
// read by the time the reader notices.
//
// A bad checksum or a malformed header describes that tail only once records
// have actually come out of the file. Met with nothing read they mean the file
// is not the archive its name claims, and classifying them as benign anyway
// reported a corrupt or non-gzip archive as an empty one - no entries, no error
// and no warning, which is the silent failure this project does not accept.
func isBenignArchiveEnd(err error, recovered int) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if recovered == 0 {
		return false
	}
	return errors.Is(err, gzip.ErrChecksum) || errors.Is(err, gzip.ErrHeader)
}

func readCompressedProxyLogFile(path string, limit int) ([]proxy.RequestLog, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()

	var (
		entries   []proxy.RequestLog
		oversized int
		readErr   error
	)

	// Handle concatenated gzip streams
	for {
		gz, err := gzip.NewReader(f)
		if err == io.EOF {
			break
		}
		if err != nil {
			// Truncated or corrupted gzip stream - stop reading.
			if !isBenignArchiveEnd(err, len(entries)+oversized) {
				readErr = fmt.Errorf("gzip stream: %w", err)
			}
			break
		}

		var (
			scanErr     error
			memberSkips int
		)
		entries, memberSkips, scanErr = scanProxyLogEntries(gz, limit, entries)
		oversized += memberSkips
		_ = gz.Close()

		// A truncated or corrupt member ends the archive: the reader is already
		// buffered past whatever follows, so there is no next member to find.
		// An oversized record is not that - it is stepped over, counted and
		// reported, and the rest of the member still reaches the caller.
		if scanErr != nil {
			// The error travels with the entries read so far rather than being
			// dropped.
			if !isBenignArchiveEnd(scanErr, len(entries)+oversized) {
				readErr = scanErr
			}
			break
		}
	}

	// Final trim if limit is set
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}

	return entries, oversized, readErr
}

func printProxyLogsJSON(entries []proxy.RequestLog, showBody bool) error {
	output := entries
	if !showBody {
		output = make([]proxy.RequestLog, len(entries))
		for i, e := range entries {
			output[i] = e
			output[i].RequestBody = nil
			output[i].ResponseBody = nil
		}
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

func printProxyLogsTable(entries []proxy.RequestLog, showBody, noColor bool) error {
	table := tablewriter.NewWriter(os.Stdout)

	if showBody {
		table.Header("TIME", "METHOD", "STATUS", "DURATION", "URL", "REQ BODY", "RESP BODY")
	} else {
		table.Header("TIME", "METHOD", "STATUS", "DURATION", "URL")
	}

	for _, e := range entries {
		status := fmt.Sprintf("%d", e.StatusCode)
		if e.Error != "" {
			status = "ERR"
		}

		// Colorize status if not disabled
		if !noColor {
			status = colorizeStatus(status, e.StatusCode, e.Error)
		}

		duration := "-"
		if e.Duration > 0 {
			duration = e.Duration.Round(time.Millisecond).String()
		}

		url := e.URL
		if len(url) > 60 {
			url = url[:57] + "..."
		}

		if showBody {
			reqBody := truncateLogBody(e.RequestBody, 80)
			respBody := truncateLogBody(e.ResponseBody, 80)
			if reqBody == "" {
				reqBody = "-"
			}
			if respBody == "" {
				respBody = "-"
			}
			_ = table.Append(
				e.Timestamp.Format("15:04:05"),
				e.Method,
				status,
				duration,
				url,
				reqBody,
				respBody,
			)
		} else {
			_ = table.Append(
				e.Timestamp.Format("15:04:05"),
				e.Method,
				status,
				duration,
				url,
			)
		}
	}

	return table.Render()
}

func printProxyLogsCompact(entries []proxy.RequestLog, noColor bool) error {
	for _, e := range entries {
		printProxyLogCompactLine(&e, noColor)
	}
	return nil
}

func printProxyLogCompactLine(e *proxy.RequestLog, noColor bool) {
	status := fmt.Sprintf("%d", e.StatusCode)
	if e.Error != "" {
		status = "ERR"
	}

	if !noColor {
		status = colorizeStatus(status, e.StatusCode, e.Error)
	}

	duration := "-"
	if e.Duration > 0 {
		duration = fmt.Sprintf("%dms", e.Duration.Milliseconds())
	}

	fmt.Printf("%s %s %s %s %s\n",
		e.Timestamp.Format("15:04:05"),
		e.Method,
		status,
		duration,
		e.URL,
	)
}

func printProxyLogLine(e *proxy.RequestLog, showBody, noColor bool) {
	status := fmt.Sprintf("%d", e.StatusCode)
	if e.Error != "" {
		status = "ERR"
	}

	if !noColor {
		status = colorizeStatus(status, e.StatusCode, e.Error)
	}

	duration := "-"
	if e.Duration > 0 {
		duration = e.Duration.Round(time.Millisecond).String()
	}

	fmt.Printf("%s | %s | %s | %s | %s\n",
		e.Timestamp.Format("15:04:05"),
		e.Method,
		status,
		duration,
		e.URL,
	)

	if showBody {
		if len(e.RequestBody) > 0 {
			fmt.Printf("  → REQ: %s\n", truncateLogBody(e.RequestBody, 200))
		}
		if len(e.ResponseBody) > 0 {
			fmt.Printf("  ← RSP: %s\n", truncateLogBody(e.ResponseBody, 200))
		}
	}
}

func colorizeStatus(status string, code int, errMsg string) string {
	if errMsg != "" {
		return "\033[1;31m" + status + "\033[0m" // Bold red
	}

	switch {
	case code >= 500:
		return "\033[31m" + status + "\033[0m" // Red
	case code >= 400:
		return "\033[33m" + status + "\033[0m" // Yellow
	case code >= 300:
		return "\033[36m" + status + "\033[0m" // Cyan
	case code >= 200:
		return "\033[32m" + status + "\033[0m" // Green
	default:
		return status
	}
}

func printProxyStats(entries []proxy.RequestLog) {
	if len(entries) == 0 {
		return
	}

	var (
		total     = len(entries)
		success   int // 2xx
		redirect  int // 3xx
		clientErr int // 4xx
		serverErr int // 5xx
		errors    int // error field set
		totalDur  time.Duration
		durCount  int
		minTime   = entries[0].Timestamp
		maxTime   = entries[0].Timestamp
	)

	for _, e := range entries {
		switch {
		case e.Error != "":
			errors++
		case e.StatusCode >= 500:
			serverErr++
		case e.StatusCode >= 400:
			clientErr++
		case e.StatusCode >= 300:
			redirect++
		case e.StatusCode >= 200:
			success++
		}

		if e.Duration > 0 {
			totalDur += e.Duration
			durCount++
		}

		if e.Timestamp.Before(minTime) {
			minTime = e.Timestamp
		}
		if e.Timestamp.After(maxTime) {
			maxTime = e.Timestamp
		}
	}

	fmt.Println()
	fmt.Println("Summary:")
	fmt.Printf("  Total requests: %d\n", total)
	if success > 0 {
		fmt.Printf("  Success (2xx):  %d (%.0f%%)\n", success, float64(success)/float64(total)*100)
	}
	if redirect > 0 {
		fmt.Printf("  Redirect (3xx): %d (%.0f%%)\n", redirect, float64(redirect)/float64(total)*100)
	}
	if clientErr > 0 {
		fmt.Printf("  Client err (4xx): %d (%.0f%%)\n", clientErr, float64(clientErr)/float64(total)*100)
	}
	if serverErr > 0 {
		fmt.Printf("  Server err (5xx): %d (%.0f%%)\n", serverErr, float64(serverErr)/float64(total)*100)
	}
	if errors > 0 {
		fmt.Printf("  Errors: %d (%.0f%%)\n", errors, float64(errors)/float64(total)*100)
	}
	if durCount > 0 {
		avgDur := totalDur / time.Duration(durCount)
		fmt.Printf("  Avg duration: %s\n", avgDur.Round(time.Millisecond))
	}
	fmt.Printf("  Time range: %s - %s\n", minTime.Format("2006-01-02 15:04"), maxTime.Format("15:04"))
}

func truncateLogBody(body []byte, maxLen int) string {
	if len(body) == 0 {
		return ""
	}
	s := string(body)
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\t", " ")
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

func newLogsInternalCmd() *cobra.Command {
	var (
		sandboxName string
		logType     string
		last        int
		follow      bool
		since       string
	)

	cmd := &cobra.Command{
		Use:   "internal [sandbox-name]",
		Short: "View internal logs",
		Long: `View internal logs including proxy server errors and logging failures.

Log types:
  proxy    - Proxy server internal logs (warnings, errors from goproxy)
  logging  - Remote logging failures (OTLP, syslog errors)
  all      - All internal logs (default)`,
		Example: `  devsandbox logs internal                   # All internal logs
  devsandbox logs internal --type logging    # Logging errors only
  devsandbox logs internal --type proxy      # Proxy server logs only
  devsandbox logs internal -f                # Follow internal logs
  devsandbox logs internal --last 100        # Last 100 lines`,
		RunE: func(cmd *cobra.Command, args []string) error {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return err
			}

			// Determine sandbox name
			name := sandboxName
			if len(args) > 0 {
				name = args[0]
			}
			if name == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				name = sandbox.GenerateSandboxName(cwd)
			}

			baseDir := sandbox.SandboxBasePath(homeDir)
			sandboxRoot := filepath.Join(baseDir, name)
			logDir := filepath.Join(sandboxRoot, proxy.LogBaseDirName, proxy.InternalLogDirName)

			// Check if log directory exists
			if _, err := os.Stat(logDir); os.IsNotExist(err) {
				return fmt.Errorf("no internal logs found for sandbox %q", name)
			}

			// Parse time filter
			var sinceTime time.Time
			if since != "" {
				t, err := ParseTimeFilter(since)
				if err != nil {
					return err
				}
				sinceTime = t
			}

			if follow {
				return followInternalLogs(logDir, logType, sinceTime)
			}

			return viewInternalLogs(logDir, logType, last, sinceTime)
		},
	}

	cmd.Flags().StringVarP(&sandboxName, "sandbox", "s", "", "Sandbox name (default: current directory)")
	cmd.Flags().StringVar(&logType, "type", "all", "Log type: proxy, logging, or all")
	cmd.Flags().IntVarP(&last, "last", "n", 0, "Show only last N lines")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow/tail log output")
	cmd.Flags().StringVar(&since, "since", "", "Show logs since time")

	return cmd
}

func viewInternalLogs(logDir, logType string, last int, since time.Time) error {
	var lines []string

	// Collect lines from relevant log files
	switch logType {
	case "logging":
		l, err := readLoggingErrorsLog(filepath.Join(logDir, "logging-errors.log"), since)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		lines = append(lines, l...)

	case "proxy":
		l, err := readProxyInternalLogs(logDir, since)
		if err != nil {
			return err
		}
		lines = append(lines, l...)

	default: // "all"
		// Read logging errors
		l1, err := readLoggingErrorsLog(filepath.Join(logDir, "logging-errors.log"), since)
		if err != nil && !os.IsNotExist(err) {
			notice.Warn("%v", err)
		}
		lines = append(lines, l1...)

		// Read proxy internal logs
		l2, err := readProxyInternalLogs(logDir, since)
		if err != nil {
			notice.Warn("%v", err)
		}
		lines = append(lines, l2...)

		// Sort by timestamp (lines start with timestamp)
		sort.Strings(lines)
	}

	if len(lines) == 0 {
		fmt.Println("No internal log entries found.")
		return nil
	}

	// Apply --last limit
	if last > 0 && len(lines) > last {
		lines = lines[len(lines)-last:]
	}

	for _, line := range lines {
		fmt.Println(line)
	}

	return nil
}

func readLoggingErrorsLog(path string, since time.Time) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !since.IsZero() {
			// Try to parse timestamp from line (format: 2024-01-15T10:30:00+00:00 [...])
			if len(line) >= 25 {
				if t, err := time.Parse(time.RFC3339, line[:25]); err == nil {
					if t.Before(since) {
						continue
					}
				}
			}
		}
		lines = append(lines, line)
	}

	return lines, scanner.Err()
}

func readProxyInternalLogs(logDir string, since time.Time) ([]string, error) {
	// Both the active file and its compressed rotations, because the writer
	// produces both: matching only one suffix is how this reader used to return
	// nothing at all.
	var files []string
	for _, suffix := range []string{proxy.ProxyLogSuffix, proxy.ProxyLogArchiveSuffix} {
		matches, err := filepath.Glob(filepath.Join(logDir, proxy.ProxyLogPrefix+"*"+suffix))
		if err != nil {
			return nil, err
		}
		files = append(files, matches...)
	}

	sort.Strings(files)

	var lines []string
	for _, file := range files {
		l, err := readProxyLogFile(file, since)
		if err != nil {
			// A file that cannot be read is reported, not skipped. Swallowing
			// it made an unreadable log indistinguishable from an empty one,
			// which is how the whole reader stayed silently broken.
			notice.Warn("skipping %s: %v", file, err)
			continue
		}
		lines = append(lines, l...)
	}

	return lines, nil
}

// readProxyLogFile reads one internal proxy log, compressed or not.
//
// The encoding is sniffed rather than taken from the name because the two have
// disagreed: the writer named its *active* file `.log.gz` and wrote plain text
// into it, so every such file already on disk is mislabeled. Sniffing reads
// those and the correctly named ones alike.
func readProxyLogFile(path string, since time.Time) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	r := bufio.NewReader(f)
	magic, err := r.Peek(2)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil // empty file, nothing to report
		}
		return nil, err
	}
	if magic[0] != 0x1f || magic[1] != 0x8b {
		return scanProxyLogLines(r, since)
	}

	var lines []string
	// Handle concatenated gzip streams.
	for {
		gz, err := gzip.NewReader(r)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if len(lines) > 0 {
				// A truncated trailing stream after readable data is a log
				// still being written, not a broken file.
				break
			}
			return nil, err
		}
		got, err := scanProxyLogLines(gz, since)
		_ = gz.Close()
		if err != nil {
			return nil, err
		}
		lines = append(lines, got...)
	}

	return lines, nil
}

// scanProxyLogLines returns the lines of an internal proxy log at or after
// since. Its records are stamped `2024/01/15 10:30:00 message`; a line that does
// not parse as one is kept, since dropping it would hide exactly the output a
// caller reading these logs is looking for.
//
// The scanner's own error is reported rather than dropped. A record longer than
// bufio.MaxScanTokenSize ends the scan, and swallowing that hands the caller a
// silently truncated log - and in the gzip branch, an aborted scan that also
// leaves the deflate stream mid-member, so every later member is lost too.
func scanProxyLogLines(r io.Reader, since time.Time) ([]string, error) {
	var lines []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if !since.IsZero() && len(line) >= 19 {
			if t, err := time.Parse("2006/01/02 15:04:05", line[:19]); err == nil {
				if t.Before(since) {
					continue
				}
			}
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read proxy log: %w", err)
	}
	return lines, nil
}

func followInternalLogs(logDir, logType string, since time.Time) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	notice.Info("Following internal logs in %s (Ctrl+C to stop)...", logDir)

	loggingErrorsPath := filepath.Join(logDir, "logging-errors.log")
	var lastLoggingPos int64

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// Follow logging-errors.log (plain text, easy to tail)
			if logType == "all" || logType == "logging" {
				lines, newPos, err := tailFile(loggingErrorsPath, lastLoggingPos)
				if err == nil {
					lastLoggingPos = newPos
					for _, line := range lines {
						fmt.Println(line)
					}
				}
			}
		}
	}
}

func tailFile(path string, offset int64) ([]string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}

	if info.Size() <= offset {
		return nil, offset, nil
	}

	if offset > 0 {
		_, err = f.Seek(offset, io.SeekStart)
		if err != nil {
			return nil, offset, err
		}
	}

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	return lines, info.Size(), scanner.Err()
}
