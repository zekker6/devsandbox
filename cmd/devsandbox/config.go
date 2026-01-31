package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"devsandbox/internal/config"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage configuration",
		Long: `View and manage devsandbox configuration.

Configuration file location: ~/.config/devsandbox/config.toml
(or $XDG_CONFIG_HOME/devsandbox/config.toml)`,
	}

	cmd.AddCommand(newConfigShowCmd())
	cmd.AddCommand(newConfigPathCmd())
	cmd.AddCommand(newConfigInitCmd())

	return cmd
}

func newConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			fmt.Printf("Config file: %s\n\n", config.ConfigPath())

			fmt.Println("[proxy]")
			fmt.Printf("  enabled = %v\n", cfg.Proxy.Enabled)
			fmt.Printf("  port = %d\n", cfg.Proxy.Port)
			fmt.Println()

			fmt.Println("[sandbox]")
			basePath := cfg.Sandbox.BasePath
			if basePath == "" {
				basePath = "(default)"
			}
			fmt.Printf("  base_path = %s\n", basePath)

			return nil
		},
	}
}

func newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Show configuration file path",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(config.ConfigPath())
		},
	}
}

func newConfigInitCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create default configuration file",
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := config.ConfigPath()

			// Check if config already exists
			if _, err := os.Stat(configPath); err == nil && !force {
				return fmt.Errorf("config file already exists at %s\nUse --force to overwrite", configPath)
			}

			// Create config directory
			configDir := filepath.Dir(configPath)
			if err := os.MkdirAll(configDir, 0o755); err != nil {
				return fmt.Errorf("failed to create config directory: %w", err)
			}

			// Write default config
			if err := os.WriteFile(configPath, []byte(config.GenerateDefault()), 0o644); err != nil {
				return fmt.Errorf("failed to write config file: %w", err)
			}

			fmt.Printf("Created config file at %s\n", configPath)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing config file")

	return cmd
}
