package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"devsandbox/internal/config"
)

func newTrustCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Manage trusted local configurations",
		Long: `Manage trusted .devsandbox.toml files.

Local config files must be trusted before they are applied.
Trust is verified by SHA256 hash - if the file changes, you'll be prompted again.`,
	}

	cmd.AddCommand(newTrustListCmd())
	cmd.AddCommand(newTrustAddCmd())
	cmd.AddCommand(newTrustRemoveCmd())

	return cmd
}

func newTrustListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all trusted configurations",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := config.LoadTrustStore(config.TrustStorePath())
			if err != nil {
				return fmt.Errorf("failed to load trust store: %w", err)
			}

			if len(store.Trusted) == 0 {
				fmt.Println("No trusted configurations.")
				return nil
			}

			fmt.Println("Trusted configurations:")
			fmt.Println()
			for _, tc := range store.Trusted {
				fmt.Printf("  %s\n", tc.Path)
				fmt.Printf("    Added: %s\n", tc.Added.Local().Format("2006-01-02 15:04:05"))
				fmt.Println()
			}

			return nil
		},
	}
}

func newTrustAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add [path]",
		Short: "Trust a local configuration",
		Long: `Trust the .devsandbox.toml file in the specified directory (or current directory).

This is useful for non-interactive environments (CI, scripts) where you need
to pre-approve local configs without interactive prompts.

Examples:
  devsandbox trust add              # Trust config in current directory
  devsandbox trust add /path/to/project  # Trust config in specified directory`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var projectDir string
			var err error

			if len(args) > 0 {
				projectDir, err = filepath.Abs(args[0])
				if err != nil {
					return fmt.Errorf("failed to resolve path: %w", err)
				}
			} else {
				projectDir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("failed to get current directory: %w", err)
				}
			}

			// Check if local config exists
			localConfigPath := filepath.Join(projectDir, config.LocalConfigFile)
			if _, err := os.Stat(localConfigPath); os.IsNotExist(err) {
				return fmt.Errorf("no %s found in %s", config.LocalConfigFile, projectDir)
			}

			// Compute hash
			hash, err := config.HashFile(localConfigPath)
			if err != nil {
				return fmt.Errorf("failed to hash config file: %w", err)
			}

			// Load trust store
			store, err := config.LoadTrustStore(config.TrustStorePath())
			if err != nil {
				return fmt.Errorf("failed to load trust store: %w", err)
			}

			// Check if already trusted with same hash
			existing := store.GetTrusted(projectDir)
			if existing != nil && existing.Hash == hash {
				fmt.Printf("Already trusted: %s\n", projectDir)
				return nil
			}

			// Add trust
			store.AddTrust(projectDir, hash)

			if err := store.Save(); err != nil {
				return fmt.Errorf("failed to save trust store: %w", err)
			}

			if existing != nil {
				fmt.Printf("Updated trust for %s\n", projectDir)
			} else {
				fmt.Printf("Trusted %s\n", projectDir)
			}
			return nil
		},
	}
}

func newTrustRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove [path]",
		Short: "Remove trust for a directory",
		Long: `Remove trust for the .devsandbox.toml file in the specified directory (or current directory).

Examples:
  devsandbox trust remove              # Remove trust for current directory
  devsandbox trust remove /path/to/project  # Remove trust for specified directory`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var projectDir string
			var err error

			if len(args) > 0 {
				projectDir, err = filepath.Abs(args[0])
				if err != nil {
					return fmt.Errorf("failed to resolve path: %w", err)
				}
			} else {
				projectDir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("failed to get current directory: %w", err)
				}
			}

			store, err := config.LoadTrustStore(config.TrustStorePath())
			if err != nil {
				return fmt.Errorf("failed to load trust store: %w", err)
			}

			if !store.RemoveTrust(projectDir) {
				fmt.Printf("No trust entry for %s\n", projectDir)
				return nil
			}

			if err := store.Save(); err != nil {
				return fmt.Errorf("failed to save trust store: %w", err)
			}

			fmt.Printf("Removed trust for %s\n", projectDir)
			return nil
		},
	}
}
