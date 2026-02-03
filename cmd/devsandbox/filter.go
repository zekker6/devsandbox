package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"devsandbox/internal/config"
	"devsandbox/internal/proxy"
	"devsandbox/internal/sandbox"
)

func newFilterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "filter",
		Short: "Manage HTTP filter configuration",
		Long:  `Generate, show, and manage HTTP filter rules from proxy logs.`,
	}

	cmd.AddCommand(newFilterGenerateCmd())
	cmd.AddCommand(newFilterShowCmd())

	return cmd
}

func newFilterGenerateCmd() *cobra.Command {
	var (
		fromLogs      string
		project       string
		minRequests   int
		outputFile    string
		defaultAction string
	)

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate filter configuration from proxy logs",
		Long: `Analyze proxy logs and generate filter rules based on observed traffic.

Examples:
  # Generate whitelist rules (block unmatched) from specific log directory
  devsandbox proxy filter generate --from-logs ~/.local/share/devsandbox/myproject/logs/proxy/

  # Generate for current project
  devsandbox proxy filter generate

  # Generate blacklist rules (allow unmatched)
  devsandbox proxy filter generate --default-action allow
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFilterGenerate(fromLogs, project, minRequests, outputFile, defaultAction)
		},
	}

	cmd.Flags().StringVar(&fromLogs, "from-logs", "", "Path to proxy log directory")
	cmd.Flags().StringVar(&project, "project", "", "Project name (uses current directory if not set)")
	cmd.Flags().IntVar(&minRequests, "min-requests", 1, "Minimum requests to include a domain")
	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "Output file (default: stdout)")
	cmd.Flags().StringVar(&defaultAction, "default-action", "block", "Default action for unmatched requests: block (whitelist) or allow (blacklist)")

	return cmd
}

func newFilterShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show current filter configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, _, err := config.LoadConfig()
			if err != nil {
				return err
			}

			if cfg.Proxy.Filter.DefaultAction == "" {
				fmt.Println("No filter configuration found (filtering disabled).")
				return nil
			}

			fmt.Printf("Default Action: %s\n", cfg.Proxy.Filter.DefaultAction)
			if cfg.Proxy.Filter.AskTimeout > 0 {
				fmt.Printf("Ask Timeout: %d seconds\n", cfg.Proxy.Filter.AskTimeout)
			}
			if cfg.Proxy.Filter.CacheDecisions != nil {
				fmt.Printf("Cache Decisions: %v\n", *cfg.Proxy.Filter.CacheDecisions)
			}
			fmt.Println()
			fmt.Printf("Rules (%d):\n", len(cfg.Proxy.Filter.Rules))
			for i, rule := range cfg.Proxy.Filter.Rules {
				fmt.Printf("  %d. [%s] %s (scope: %s)\n", i+1, rule.Action, rule.Pattern, rule.Scope)
				if rule.Reason != "" {
					fmt.Printf("      Reason: %s\n", rule.Reason)
				}
			}

			return nil
		},
	}
}

// DomainStats holds statistics for a domain.
type DomainStats struct {
	Domain       string
	RequestCount int
	Methods      map[string]int
	StatusCodes  map[int]int
	Paths        map[string]int
}

func runFilterGenerate(fromLogs, project string, minRequests int, outputFile, defaultAction string) error {
	logDir, err := resolveLogDir(fromLogs, project)
	if err != nil {
		return err
	}

	if _, err := os.Stat(logDir); os.IsNotExist(err) {
		return fmt.Errorf("log directory not found: %s", logDir)
	}

	stats, err := analyzeProxyLogs(logDir)
	if err != nil {
		return fmt.Errorf("failed to analyze logs: %w", err)
	}
	if len(stats) == 0 {
		fmt.Fprintln(os.Stderr, "No requests found in logs.")
		return nil
	}

	var filtered []DomainStats
	for _, s := range stats {
		if s.RequestCount >= minRequests {
			filtered = append(filtered, s)
		}
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].RequestCount > filtered[j].RequestCount
	})

	output := generateFilterConfig(filtered, defaultAction)
	return writeOutput(output, outputFile)
}

func resolveLogDir(fromLogs, project string) (string, error) {
	if fromLogs != "" {
		return fromLogs, nil
	}

	appCfg, _, projectDir, err := config.LoadConfig()
	if err != nil {
		return "", err
	}

	basePath := appCfg.Sandbox.BasePath
	if basePath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		basePath = sandbox.SandboxBasePath(home)
	}

	if project == "" {
		project = sandbox.GenerateSandboxName(projectDir)
	}

	return filepath.Join(basePath, project, "logs", "proxy"), nil
}

func writeOutput(output, outputFile string) error {
	if outputFile == "" {
		fmt.Println(output)
		return nil
	}
	if err := os.WriteFile(outputFile, []byte(output), 0o644); err != nil {
		return fmt.Errorf("failed to write output: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Filter configuration written to: %s\n", outputFile)
	return nil
}

func analyzeProxyLogs(logDir string) ([]DomainStats, error) {
	domainMap := make(map[string]*DomainStats)

	// Find all log files
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasPrefix(entry.Name(), "requests") {
			continue
		}

		logPath := filepath.Join(logDir, entry.Name())
		if err := processLogFile(logPath, domainMap); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to process %s: %v\n", entry.Name(), err)
		}
	}

	// Convert to slice
	var result []DomainStats
	for _, stats := range domainMap {
		result = append(result, *stats)
	}

	return result, nil
}

func processLogFile(path string, domainMap map[string]*DomainStats) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var reader *bufio.Scanner
	if strings.HasSuffix(path, ".gz") {
		gr, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer func() { _ = gr.Close() }()
		reader = bufio.NewScanner(gr)
	} else {
		reader = bufio.NewScanner(f)
	}

	for reader.Scan() {
		line := reader.Text()
		if line == "" {
			continue
		}

		var entry proxy.RequestLog
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		// Extract domain from URL
		parsedURL, err := url.Parse(entry.URL)
		if err != nil {
			continue
		}

		domain := parsedURL.Host
		// Remove port
		if idx := strings.LastIndex(domain, ":"); idx > 0 {
			domain = domain[:idx]
		}

		// Update stats
		stats, ok := domainMap[domain]
		if !ok {
			stats = &DomainStats{
				Domain:      domain,
				Methods:     make(map[string]int),
				StatusCodes: make(map[int]int),
				Paths:       make(map[string]int),
			}
			domainMap[domain] = stats
		}

		stats.RequestCount++
		stats.Methods[entry.Method]++
		if entry.StatusCode > 0 {
			stats.StatusCodes[entry.StatusCode]++
		}

		// Track unique paths (truncate to first 2 segments)
		pathKey := truncatePath(parsedURL.Path)
		stats.Paths[pathKey]++
	}

	return reader.Err()
}

func truncatePath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) > 2 {
		return "/" + strings.Join(parts[:2], "/") + "/..."
	}
	return path
}

func generateFilterConfig(stats []DomainStats, defaultAction string) string {
	var sb strings.Builder

	sb.WriteString("# Generated filter configuration\n")
	sb.WriteString("# Review and adjust rules as needed\n\n")
	sb.WriteString("[proxy.filter]\n")
	fmt.Fprintf(&sb, "default_action = %q\n", defaultAction)

	sb.WriteString("\n# Rules generated from proxy logs\n")

	// Determine rule action based on default action
	// If default is "block" (whitelist), rules should "allow"
	// If default is "allow" (blacklist), rules should "block"
	action := "allow"
	if defaultAction == "allow" {
		action = "block"
	}

	for _, s := range stats {
		sb.WriteString("\n[[proxy.filter.rules]]\n")

		// Try to create a glob pattern for subdomains
		pattern := s.Domain
		parts := strings.Split(s.Domain, ".")
		if len(parts) > 2 {
			// e.g., api.github.com -> *.github.com
			pattern = "*." + strings.Join(parts[len(parts)-2:], ".")
			fmt.Fprintf(&sb, "# Original: %s (%d requests)\n", s.Domain, s.RequestCount)
		} else {
			fmt.Fprintf(&sb, "# %d requests\n", s.RequestCount)
		}

		fmt.Fprintf(&sb, "pattern = %q\n", pattern)
		fmt.Fprintf(&sb, "action = %q\n", action)
	}

	return sb.String()
}
