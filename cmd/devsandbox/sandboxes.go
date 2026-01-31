package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"devsandbox/internal/proxy"
	"devsandbox/internal/sandbox"
)

func newSandboxesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandboxes",
		Short: "Manage sandbox instances",
		Long:  "List, inspect, and prune sandbox instances stored in ~/.local/share/devsandbox/",
	}

	cmd.AddCommand(newListCmd())
	cmd.AddCommand(newPruneCmd())
	cmd.AddCommand(newLogsCmd())

	return cmd
}

func newListCmd() *cobra.Command {
	var (
		jsonOutput bool
		sortBy     string
		noSize     bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all sandboxes",
		Long:  "List all sandbox instances with their metadata",
		Example: `  devsandbox sandboxes list
  devsandbox sandboxes list --json
  devsandbox sandboxes list --sort used
  devsandbox sandboxes list --no-size`,
		RunE: func(cmd *cobra.Command, args []string) error {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return err
			}

			baseDir := sandbox.SandboxBasePath(homeDir)
			sandboxes, err := sandbox.ListSandboxes(baseDir)
			if err != nil {
				return err
			}

			if len(sandboxes) == 0 {
				fmt.Println("No sandboxes found.")
				return nil
			}

			// Calculate sizes (default: on)
			if !noSize {
				for _, s := range sandboxes {
					size, _ := sandbox.GetSandboxSize(s.SandboxRoot)
					s.SizeBytes = size
				}
			}

			// Sort
			sandbox.SortSandboxes(sandboxes, sandbox.SortBy(sortBy))

			if jsonOutput {
				return printJSON(sandboxes)
			}

			return printTable(sandboxes, !noSize)
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	cmd.Flags().StringVar(&sortBy, "sort", "name", "Sort by: name, created, used, size")
	cmd.Flags().BoolVar(&noSize, "no-size", false, "Skip size calculation (faster)")

	return cmd
}

func newPruneCmd() *cobra.Command {
	var (
		all       bool
		keep      int
		olderThan string
		dryRun    bool
		force     bool
	)

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove stale sandboxes",
		Long: `Remove sandbox instances based on various criteria.

Without any flags, only orphaned sandboxes (where the original project
directory no longer exists) are removed.`,
		Example: `  devsandbox sandboxes prune              # Remove orphaned only
  devsandbox sandboxes prune --all        # Remove all sandboxes
  devsandbox sandboxes prune --keep 5     # Keep 5 most recently used
  devsandbox sandboxes prune --older-than 30d  # Remove unused for 30 days
  devsandbox sandboxes prune --dry-run    # Show what would be removed`,
		RunE: func(cmd *cobra.Command, args []string) error {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return err
			}

			baseDir := sandbox.SandboxBasePath(homeDir)
			sandboxes, err := sandbox.ListSandboxes(baseDir)
			if err != nil {
				return err
			}

			if len(sandboxes) == 0 {
				fmt.Println("No sandboxes found.")
				return nil
			}

			// Parse duration
			var duration time.Duration
			if olderThan != "" {
				var err error
				duration, err = parseDuration(olderThan)
				if err != nil {
					return fmt.Errorf("invalid duration %q: %w", olderThan, err)
				}
			}

			opts := sandbox.PruneOptions{
				All:       all,
				Keep:      keep,
				OlderThan: duration,
				DryRun:    dryRun,
			}

			toPrune := sandbox.SelectForPruning(sandboxes, opts)

			if len(toPrune) == 0 {
				fmt.Println("No sandboxes to prune.")
				return nil
			}

			// Calculate sizes for display
			var totalSize int64
			for _, s := range toPrune {
				size, _ := sandbox.GetSandboxSize(s.SandboxRoot)
				s.SizeBytes = size
				totalSize += size
			}

			// Show what will be removed
			fmt.Printf("Sandboxes to remove (%d):\n\n", len(toPrune))
			for _, s := range toPrune {
				status := ""
				if s.Orphaned {
					status = " [orphaned]"
				}
				fmt.Printf("  %s%s\n", s.Name, status)
				fmt.Printf("    Project: %s\n", s.ProjectDir)
				fmt.Printf("    Last used: %s\n", s.LastUsed.Format("2006-01-02 15:04"))
				fmt.Printf("    Size: %s\n", sandbox.FormatSize(s.SizeBytes))
				fmt.Println()
			}
			fmt.Printf("Total: %s\n\n", sandbox.FormatSize(totalSize))

			if dryRun {
				fmt.Println("Dry run - no sandboxes were removed.")
				return nil
			}

			// Confirm unless --force
			if !force {
				fmt.Print("Remove these sandboxes? [y/N] ")
				reader := bufio.NewReader(os.Stdin)
				response, err := reader.ReadString('\n')
				if err != nil {
					return err
				}
				response = strings.TrimSpace(strings.ToLower(response))
				if response != "y" && response != "yes" {
					fmt.Println("Aborted.")
					return nil
				}
			}

			// Remove sandboxes
			var removed, failed int
			for _, s := range toPrune {
				if err := sandbox.RemoveSandbox(s.SandboxRoot); err != nil {
					fmt.Fprintf(os.Stderr, "Failed to remove %s: %v\n", s.Name, err)
					failed++
				} else {
					removed++
				}
			}

			fmt.Printf("Removed %d sandbox(es)", removed)
			if failed > 0 {
				fmt.Printf(", %d failed", failed)
			}
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Remove all sandboxes")
	cmd.Flags().IntVar(&keep, "keep", 0, "Keep N most recently used sandboxes")
	cmd.Flags().StringVar(&olderThan, "older-than", "", "Remove sandboxes not used in duration (e.g., 30d, 2w)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be removed without removing")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation prompt")

	return cmd
}

func printJSON(sandboxes []*sandbox.Metadata) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(sandboxes)
}

func printTable(sandboxes []*sandbox.Metadata, showSize bool) error {
	table := tablewriter.NewWriter(os.Stdout)

	if showSize {
		table.Header("NAME", "PROJECT DIR", "CREATED", "LAST USED", "SIZE", "STATUS")
	} else {
		table.Header("NAME", "PROJECT DIR", "CREATED", "LAST USED", "STATUS")
	}

	for _, s := range sandboxes {
		status := ""
		if s.Orphaned {
			status = "orphaned"
		}

		projectDir := s.ProjectDir
		if len(projectDir) > 40 {
			projectDir = "..." + projectDir[len(projectDir)-37:]
		}

		if showSize {
			_ = table.Append(
				s.Name,
				projectDir,
				s.CreatedAt.Format("2006-01-02"),
				s.LastUsed.Format("2006-01-02"),
				sandbox.FormatSize(s.SizeBytes),
				status,
			)
		} else {
			_ = table.Append(
				s.Name,
				projectDir,
				s.CreatedAt.Format("2006-01-02"),
				s.LastUsed.Format("2006-01-02"),
				status,
			)
		}
	}

	return table.Render()
}

func newLogsCmd() *cobra.Command {
	var (
		sandboxName  string
		last         int
		follow       bool
		jsonOutput   bool
		showBody     bool
		filterURL    string
		filterMethod string
	)

	cmd := &cobra.Command{
		Use:   "logs [sandbox-name]",
		Short: "View proxy request logs",
		Long: `View HTTP/HTTPS request logs captured by proxy mode.

If no sandbox name is provided, uses the current directory's sandbox.`,
		Example: `  devsandbox sandboxes logs                    # Logs for current project
  devsandbox sandboxes logs myproject          # Logs for specific sandbox
  devsandbox sandboxes logs --last 50          # Show last 50 requests
  devsandbox sandboxes logs --json             # JSON output
  devsandbox sandboxes logs --body             # Include request/response bodies
  devsandbox sandboxes logs --url api.example  # Filter by URL
  devsandbox sandboxes logs --method POST      # Filter by method`,
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
				// Use current directory
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				name = sandbox.SanitizeProjectName(filepath.Base(cwd))
			}

			baseDir := sandbox.SandboxBasePath(homeDir)
			sandboxRoot := filepath.Join(baseDir, name)
			logDir := filepath.Join(sandboxRoot, proxy.LogBaseDirName, proxy.ProxyLogDirName)

			// Check if log directory exists
			if _, err := os.Stat(logDir); os.IsNotExist(err) {
				return fmt.Errorf("no logs found for sandbox %q (run with --proxy to capture logs)", name)
			}

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

			// Read and display logs
			var entries []proxy.RequestLog
			for _, file := range files {
				fileEntries, err := readLogFile(file)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to read %s: %v\n", filepath.Base(file), err)
					continue
				}
				entries = append(entries, fileEntries...)
			}

			// Apply filters
			if filterURL != "" || filterMethod != "" {
				var filtered []proxy.RequestLog
				for _, e := range entries {
					if filterURL != "" && !strings.Contains(e.URL, filterURL) {
						continue
					}
					if filterMethod != "" && !strings.EqualFold(e.Method, filterMethod) {
						continue
					}
					filtered = append(filtered, e)
				}
				entries = filtered
			}

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
				return printLogsJSON(entries, showBody)
			}
			return printLogsTable(entries, showBody)
		},
	}

	cmd.Flags().StringVarP(&sandboxName, "sandbox", "s", "", "Sandbox name (default: current directory)")
	cmd.Flags().IntVarP(&last, "last", "n", 0, "Show only last N entries")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output (not yet implemented)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	cmd.Flags().BoolVar(&showBody, "body", false, "Include request/response bodies")
	cmd.Flags().StringVar(&filterURL, "url", "", "Filter by URL (substring match)")
	cmd.Flags().StringVar(&filterMethod, "method", "", "Filter by HTTP method")

	return cmd
}

func readLogFile(path string) ([]proxy.RequestLog, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer func() { _ = gz.Close() }()

	var entries []proxy.RequestLog
	decoder := json.NewDecoder(gz)
	for {
		var entry proxy.RequestLog
		if err := decoder.Decode(&entry); err != nil {
			if err == io.EOF {
				break
			}
			// Skip malformed entries
			continue
		}
		entries = append(entries, entry)
	}

	return entries, nil
}

func printLogsJSON(entries []proxy.RequestLog, showBody bool) error {
	output := entries
	if !showBody {
		// Strip bodies for cleaner output
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

func printLogsTable(entries []proxy.RequestLog, showBody bool) error {
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

		duration := "-"
		if e.Duration > 0 {
			duration = e.Duration.Round(time.Millisecond).String()
		}

		url := e.URL
		if len(url) > 60 {
			url = url[:57] + "..."
		}

		if showBody {
			reqBody := truncateBody(e.RequestBody, 80)
			respBody := truncateBody(e.ResponseBody, 80)
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

func truncateBody(body []byte, maxLen int) string {
	if len(body) == 0 {
		return ""
	}
	s := string(body)
	// Replace newlines and tabs for single-line display
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\t", " ")
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

// parseDuration parses a human-friendly duration like "30d", "2w", "1h"
func parseDuration(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("duration too short")
	}

	// Try standard Go duration first
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}

	// Parse custom formats (days, weeks)
	unit := s[len(s)-1]
	valueStr := s[:len(s)-1]

	var value int
	if _, err := fmt.Sscanf(valueStr, "%d", &value); err != nil {
		return 0, fmt.Errorf("invalid number: %s", valueStr)
	}

	switch unit {
	case 'd':
		return time.Duration(value) * 24 * time.Hour, nil
	case 'w':
		return time.Duration(value) * 7 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unknown unit: %c (use h, d, or w)", unit)
	}
}
