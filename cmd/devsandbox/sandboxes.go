package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

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
			sandboxes, err := sandbox.ListAllSandboxes(baseDir)
			if err != nil {
				return err
			}

			if len(sandboxes) == 0 {
				fmt.Println("No sandboxes found.")
				return nil
			}

			// Check active status for each sandbox
			for _, s := range sandboxes {
				s.Active = sandbox.IsSessionActive(s.SandboxRoot)
			}

			// Calculate sizes (default: on)
			if !noSize {
				for _, s := range sandboxes {
					if s.Isolation == sandbox.IsolationDocker {
						// Docker volumes don't have easy size calculation
						s.SizeBytes = 0
					} else {
						size, err := sandbox.GetSandboxSize(s.SandboxRoot)
						if err != nil {
							fmt.Fprintf(os.Stderr, "Warning: failed to calculate size for %s: %v\n", s.Name, err)
						}
						s.SizeBytes = size
					}
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
			sandboxes, err := sandbox.ListAllSandboxes(baseDir)
			if err != nil {
				return err
			}

			if len(sandboxes) == 0 {
				fmt.Println("No sandboxes found.")
				return nil
			}

			// Check active status for each sandbox
			for _, s := range sandboxes {
				s.Active = sandbox.IsSessionActive(s.SandboxRoot)
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
				if s.Isolation == sandbox.IsolationDocker {
					s.SizeBytes = 0
				} else {
					size, err := sandbox.GetSandboxSize(s.SandboxRoot)
					if err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to calculate size for %s: %v\n", s.Name, err)
					}
					s.SizeBytes = size
					totalSize += size
				}
			}

			// Show what will be removed
			fmt.Printf("Sandboxes to remove (%d):\n\n", len(toPrune))
			for _, s := range toPrune {
				status := ""
				if s.Orphaned {
					status = " [orphaned]"
				}
				if s.Isolation == sandbox.IsolationDocker && s.State != "" {
					status = status + " [" + s.State + "]"
				}
				isoType := string(s.Isolation)
				if isoType == "" {
					isoType = "bwrap"
				}
				fmt.Printf("  %s (%s)%s\n", s.Name, isoType, status)
				fmt.Printf("    Project: %s\n", s.ProjectDir)
				fmt.Printf("    Last used: %s\n", s.LastUsed.Format("2006-01-02 15:04"))
				if s.Isolation != sandbox.IsolationDocker {
					fmt.Printf("    Size: %s\n", sandbox.FormatSize(s.SizeBytes))
				}
				fmt.Println()
			}
			if totalSize > 0 {
				fmt.Printf("Total: %s\n\n", sandbox.FormatSize(totalSize))
			}

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

			// Remove sandboxes (handles both bwrap and docker)
			var removed, failed int
			for _, s := range toPrune {
				if err := sandbox.RemoveSandboxByType(s); err != nil {
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
		table.Header("NAME", "TYPE", "PROJECT DIR", "CREATED", "LAST USED", "SIZE", "STATUS")
	} else {
		table.Header("NAME", "TYPE", "PROJECT DIR", "CREATED", "LAST USED", "STATUS")
	}

	for _, s := range sandboxes {
		status := ""
		if s.Orphaned {
			status = "orphaned"
		}
		if s.Active {
			if status != "" {
				status = status + ", active"
			} else {
				status = "active"
			}
		}
		// For Docker containers, show the container state
		if s.Isolation == sandbox.IsolationDocker && s.State != "" {
			if status != "" {
				status = status + ", " + s.State
			} else {
				status = s.State
			}
		}

		projectDir := s.ProjectDir
		if len(projectDir) > 40 {
			projectDir = "..." + projectDir[len(projectDir)-37:]
		}

		isoType := string(s.Isolation)
		if isoType == "" {
			isoType = "bwrap"
		}

		sizeStr := sandbox.FormatSize(s.SizeBytes)
		if s.Isolation == sandbox.IsolationDocker {
			sizeStr = "-" // Docker volumes don't have easy size
		}

		if showSize {
			_ = table.Append(
				s.Name,
				isoType,
				projectDir,
				s.CreatedAt.Format("2006-01-02"),
				s.LastUsed.Format("2006-01-02"),
				sizeStr,
				status,
			)
		} else {
			_ = table.Append(
				s.Name,
				isoType,
				projectDir,
				s.CreatedAt.Format("2006-01-02"),
				s.LastUsed.Format("2006-01-02"),
				status,
			)
		}
	}

	return table.Render()
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
