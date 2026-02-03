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
			cfg, _, _, err := config.LoadConfig()
			if err != nil {
				return err
			}

			fmt.Printf("Config file: %s\n\n", config.ConfigPath())

			fmt.Println("[proxy]")
			fmt.Printf("  enabled = %v\n", cfg.Proxy.Enabled)
			fmt.Printf("  port = %d\n", cfg.Proxy.Port)
			fmt.Println()

			// Show filter config if set
			if cfg.Proxy.Filter.DefaultAction != "" {
				fmt.Println("[proxy.filter]")
				fmt.Printf("  default_action = %s\n", cfg.Proxy.Filter.DefaultAction)
				if cfg.Proxy.Filter.AskTimeout > 0 {
					fmt.Printf("  ask_timeout = %d\n", cfg.Proxy.Filter.AskTimeout)
				}
				if cfg.Proxy.Filter.CacheDecisions != nil {
					fmt.Printf("  cache_decisions = %v\n", *cfg.Proxy.Filter.CacheDecisions)
				}
				fmt.Println()

				if len(cfg.Proxy.Filter.Rules) > 0 {
					for i, rule := range cfg.Proxy.Filter.Rules {
						fmt.Printf("[[proxy.filter.rules]] #%d\n", i+1)
						fmt.Printf("  pattern = %q\n", rule.Pattern)
						fmt.Printf("  action = %s\n", rule.Action)
						if rule.Scope != "" {
							fmt.Printf("  scope = %s\n", rule.Scope)
						}
						if rule.Type != "" {
							fmt.Printf("  type = %s\n", rule.Type)
						}
						if rule.Reason != "" {
							fmt.Printf("  reason = %q\n", rule.Reason)
						}
					}
					fmt.Println()
				}
			}

			fmt.Println("[sandbox]")
			basePath := cfg.Sandbox.BasePath
			if basePath == "" {
				basePath = "(default)"
			}
			fmt.Printf("  base_path = %s\n", basePath)
			fmt.Println()

			// Show custom mounts config
			fmt.Println("[sandbox.mounts]")
			if len(cfg.Sandbox.Mounts.Rules) == 0 {
				fmt.Println("  # No custom mount rules configured")
			} else {
				fmt.Println("  rules:")
				for i, rule := range cfg.Sandbox.Mounts.Rules {
					mode := rule.Mode
					if mode == "" {
						mode = "readonly"
					}
					fmt.Printf("    %d. %s (%s)\n", i+1, rule.Pattern, mode)
				}
			}
			fmt.Println()

			fmt.Println("[overlay]")
			fmt.Printf("  enabled = %v\n", cfg.Overlay.IsEnabled())
			fmt.Println()

			// Print tool configurations dynamically
			for toolName, toolCfg := range cfg.Tools {
				fmt.Printf("[tools.%s]\n", toolName)
				if m, ok := toolCfg.(map[string]any); ok {
					for k, v := range m {
						fmt.Printf("  %s = %v\n", k, v)
					}
				}
				fmt.Println()
			}

			fmt.Println("[logging]")
			if len(cfg.Logging.Receivers) == 0 {
				fmt.Println("  receivers = (none)")
			} else {
				for i, r := range cfg.Logging.Receivers {
					fmt.Printf("  [[receivers]] #%d\n", i+1)
					fmt.Printf("    type = %s\n", r.Type)
					if r.Address != "" {
						fmt.Printf("    address = %s\n", r.Address)
					}
					if r.Endpoint != "" {
						fmt.Printf("    endpoint = %s\n", r.Endpoint)
					}
					if r.Facility != "" {
						fmt.Printf("    facility = %s\n", r.Facility)
					}
					if r.Tag != "" {
						fmt.Printf("    tag = %s\n", r.Tag)
					}
				}
			}

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

			if !force {
				if _, err := os.Stat(configPath); err == nil {
					return fmt.Errorf("config file already exists at %s\nUse --force to overwrite", configPath)
				}
			}

			if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
				return fmt.Errorf("failed to create config directory: %w", err)
			}
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
