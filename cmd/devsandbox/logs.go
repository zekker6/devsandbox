package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/olekukonko/tablewriter"

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
	if strings.HasPrefix(s, ">=") {
		min, err = strconv.Atoi(strings.TrimPrefix(s, ">="))
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid status filter: %s", s)
		}
		return 0, min, 0, nil
	}
	if strings.HasPrefix(s, ">") {
		val, err := strconv.Atoi(strings.TrimPrefix(s, ">"))
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid status filter: %s", s)
		}
		return 0, val + 1, 0, nil
	}
	if strings.HasPrefix(s, "<=") {
		max, err = strconv.Atoi(strings.TrimPrefix(s, "<="))
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid status filter: %s", s)
		}
		return 0, 0, max, nil
	}
	if strings.HasPrefix(s, "<") {
		val, err := strconv.Atoi(strings.TrimPrefix(s, "<"))
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
				return followProxyLogs(logDir, filter, jsonOutput, compact, noColor)
			}

			return viewProxyLogs(logDir, filter, last, jsonOutput, showBody, compact, noColor, stats)
		},
	}

	cmd.Flags().StringVarP(&sandboxName, "sandbox", "s", "", "Sandbox name (default: current directory)")
	cmd.Flags().IntVarP(&last, "last", "n", 0, "Show only last N entries")
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
	// Find log files
	pattern := filepath.Join(logDir, proxy.RequestLogPrefix+"*"+proxy.RequestLogSuffix)
	files, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}

	if len(files) == 0 {
		fmt.Println("No log files found.")
		return nil
	}

	// Sort files by name (chronological order)
	sort.Strings(files)

	// Read all entries
	var entries []proxy.RequestLog
	for _, file := range files {
		fileEntries, err := readProxyLogFile(file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to read %s: %v\n", filepath.Base(file), err)
			continue
		}
		entries = append(entries, fileEntries...)
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

func followProxyLogs(logDir string, filter *ProxyLogFilter, jsonOutput, compact, noColor bool) error {
	// Set up signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		cancel()
	}()

	pattern := filepath.Join(logDir, proxy.RequestLogPrefix+"*"+proxy.RequestLogSuffix)

	// Track the last file and position
	var lastFile string
	var lastPos int64

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	fmt.Fprintf(os.Stderr, "Following logs in %s (Ctrl+C to stop)...\n", logDir)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// Find current log files
			files, err := filepath.Glob(pattern)
			if err != nil {
				continue
			}
			if len(files) == 0 {
				continue
			}

			sort.Strings(files)
			currentFile := files[len(files)-1]

			// If file changed, reset position
			if currentFile != lastFile {
				lastFile = currentFile
				lastPos = 0
			}

			// Read new entries from current file
			entries, newPos, err := readProxyLogFileFrom(currentFile, lastPos)
			if err != nil {
				continue
			}
			lastPos = newPos

			// Filter and display
			for _, e := range entries {
				if filter.Match(&e) {
					if jsonOutput {
						data, _ := json.Marshal(e)
						fmt.Println(string(data))
					} else if compact {
						printProxyLogCompactLine(&e, noColor)
					} else {
						printProxyLogLine(&e, noColor)
					}
				}
			}
		}
	}
}

func readProxyLogFile(path string) ([]proxy.RequestLog, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var entries []proxy.RequestLog

	// Handle concatenated gzip streams
	for {
		gz, err := gzip.NewReader(f)
		if err == io.EOF {
			break
		}
		if err != nil {
			// Truncated or corrupted gzip stream - stop reading
			break
		}

		decoder := json.NewDecoder(gz)
		for {
			var entry proxy.RequestLog
			if err := decoder.Decode(&entry); err != nil {
				if err == io.EOF {
					break
				}
				// Handle truncated gzip stream (unexpected EOF in compressed data)
				if err == io.ErrUnexpectedEOF || strings.Contains(err.Error(), "unexpected EOF") {
					break
				}
				// Skip malformed JSON entries but continue
				continue
			}
			entries = append(entries, entry)
		}
		_ = gz.Close()
	}

	return entries, nil
}

func readProxyLogFileFrom(path string, offset int64) ([]proxy.RequestLog, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer func() { _ = f.Close() }()

	// Get current file size
	info, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}

	// If file hasn't grown, nothing to read
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

	var entries []proxy.RequestLog

	// Read new gzip stream(s)
	for {
		gz, err := gzip.NewReader(f)
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}

		decoder := json.NewDecoder(gz)
		for {
			var entry proxy.RequestLog
			if err := decoder.Decode(&entry); err != nil {
				if err == io.EOF {
					break
				}
				// Handle truncated gzip stream
				if err == io.ErrUnexpectedEOF || strings.Contains(err.Error(), "unexpected EOF") {
					break
				}
				continue
			}
			entries = append(entries, entry)
		}
		_ = gz.Close()
	}

	return entries, info.Size(), nil
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

func printProxyLogLine(e *proxy.RequestLog, noColor bool) {
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
			fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
		}
		lines = append(lines, l1...)

		// Read proxy internal logs
		l2, err := readProxyInternalLogs(logDir, since)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
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
	pattern := filepath.Join(logDir, proxy.ProxyLogPrefix+"*"+proxy.ProxyLogSuffix)
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}

	sort.Strings(files)

	var lines []string
	for _, file := range files {
		l, err := readGzipLogFile(file, since)
		if err != nil {
			continue
		}
		lines = append(lines, l...)
	}

	return lines, nil
}

func readGzipLogFile(path string, since time.Time) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var lines []string

	// Handle concatenated gzip streams
	for {
		gz, err := gzip.NewReader(f)
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}

		scanner := bufio.NewScanner(gz)
		for scanner.Scan() {
			line := scanner.Text()
			// Proxy internal logs have format: 2024/01/15 10:30:00 message
			if !since.IsZero() && len(line) >= 19 {
				if t, err := time.Parse("2006/01/02 15:04:05", line[:19]); err == nil {
					if t.Before(since) {
						continue
					}
				}
			}
			lines = append(lines, line)
		}
		_ = gz.Close()
	}

	return lines, nil
}

func followInternalLogs(logDir, logType string, since time.Time) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		cancel()
	}()

	fmt.Fprintf(os.Stderr, "Following internal logs in %s (Ctrl+C to stop)...\n", logDir)

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
