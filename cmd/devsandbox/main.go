package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/spf13/cobra"

	"devsandbox/internal/bwrap"
	"devsandbox/internal/config"
	"devsandbox/internal/logging"
	"devsandbox/internal/network"
	"devsandbox/internal/proxy"
	"devsandbox/internal/sandbox"
	"devsandbox/internal/sandbox/mounts"
	"devsandbox/internal/sandbox/tools"
	"devsandbox/internal/version"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "devsandbox [command...]",
		Short: "Secure sandbox for development tools",
		Long: `devsandbox - Bubblewrap sandbox for running untrusted dev tools

Security Model:
  - Current directory: read/write
  - mise-managed tools: available (node, python, bun, etc.)
  - Network: enabled (required for package managers, agents)
  - SSH: BLOCKED (no ~/.ssh access)
  - Git: read-only (can view history, cannot push)
  - .env files: BLOCKED (overlaid with /dev/null)
  - Home directory: sandboxed per-project in ~/.local/share/devsandbox/<project>/

Proxy Mode (--proxy):
  - All HTTP/HTTPS traffic routed through local proxy
  - MITM proxy with auto-generated CA certificate
  - Network isolated via pasta (requires passt package)
  - Request logs: ~/.local/share/devsandbox/<project>/logs/proxy/`,
		Example: `  devsandbox                      # Interactive shell
  devsandbox npm install          # Install packages
  devsandbox --proxy npm install  # With proxy (traffic inspection)
  devsandbox claude --dangerously-skip-permissions
  devsandbox bun run dev`,
		Version:               version.Version,
		Args:                  cobra.ArbitraryArgs,
		DisableFlagsInUseLine: true,
		SilenceUsage:          true,
		SilenceErrors:         true,
		RunE:                  runSandbox,
	}

	rootCmd.Flags().SetInterspersed(false)

	rootCmd.Flags().Bool("info", false, "Show sandbox configuration")
	rootCmd.Flags().Bool("proxy", false, "Enable proxy mode (route traffic through MITM proxy)")
	rootCmd.Flags().Int("proxy-port", proxy.DefaultProxyPort, "Proxy server port")

	// Filter flags
	rootCmd.Flags().String("filter-default", "", "Default filter action for unmatched requests: allow, block, or ask")
	rootCmd.Flags().StringSlice("allow-domain", nil, "Allow domain pattern (can be repeated)")
	rootCmd.Flags().StringSlice("block-domain", nil, "Block domain pattern (can be repeated)")

	// Add subcommands
	rootCmd.AddCommand(newSandboxesCmd())
	rootCmd.AddCommand(newDoctorCmd())
	rootCmd.AddCommand(newConfigCmd())
	rootCmd.AddCommand(newLogsCmd())
	rootCmd.AddCommand(newToolsCmd())
	rootCmd.AddCommand(newProxyCmd())
	rootCmd.AddCommand(newTrustCmd())

	rootCmd.SetVersionTemplate(fmt.Sprintf("devsandbox %s (built: %s)\n", version.FullVersion(), version.Date))

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func runSandbox(cmd *cobra.Command, args []string) error {
	showInfo, _ := cmd.Flags().GetBool("info")
	proxyEnabled, _ := cmd.Flags().GetBool("proxy")
	proxyPort, _ := cmd.Flags().GetInt("proxy-port")
	filterDefault, _ := cmd.Flags().GetString("filter-default")
	allowDomains, _ := cmd.Flags().GetStringSlice("allow-domain")
	blockDomains, _ := cmd.Flags().GetStringSlice("block-domain")

	// Load configuration file with project-specific overrides
	appCfg, _, _, err := config.LoadConfig()
	if err != nil {
		return err
	}

	// Create sandbox config with options from config file
	opts := &sandbox.Options{
		BasePath: appCfg.Sandbox.BasePath,
	}

	cfg, err := sandbox.NewConfig(opts)
	if err != nil {
		return err
	}

	// Apply config file defaults, then CLI overrides
	// Config file defaults
	if appCfg.Proxy.IsEnabled() {
		cfg.ProxyEnabled = true
	}
	if appCfg.Proxy.Port != 0 {
		cfg.ProxyPort = appCfg.Proxy.Port
	}

	// CLI flags override config file (check if flag was explicitly set)
	if cmd.Flags().Changed("proxy") {
		cfg.ProxyEnabled = proxyEnabled
	}
	if cmd.Flags().Changed("proxy-port") {
		cfg.ProxyPort = proxyPort
	}

	// Pass overlay and tools settings to sandbox config
	cfg.OverlayEnabled = appCfg.Overlay.IsEnabled()
	cfg.ToolsConfig = appCfg.Tools
	cfg.ConfigVisibility = string(appCfg.Sandbox.GetConfigVisibility())

	// Initialize custom mounts engine
	cfg.MountsConfig = mounts.NewEngine(appCfg.Sandbox.Mounts, cfg.HomeDir)

	if showInfo {
		printInfo(cfg)
		return nil
	}

	if err := bwrap.CheckInstalled(); err != nil {
		return err
	}

	if err := cfg.EnsureSandboxDirs(); err != nil {
		return err
	}

	// Handle proxy mode
	var proxyServer *proxy.Server
	var netProvider network.Provider

	if cfg.ProxyEnabled {
		// Proxy mode requires pasta for network isolation and traffic enforcement.
		// Without pasta, applications could bypass the proxy entirely.
		netProvider, err = network.SelectProvider()
		if err != nil {
			return fmt.Errorf("proxy mode requires pasta: %w\nRun 'devsandbox doctor' for installation instructions", err)
		}

		cfg.NetworkIsolated = netProvider.NetworkIsolated()

		// Set up proxy
		proxyCfg := proxy.NewConfig(cfg.SandboxRoot, proxyPort)
		proxyCfg.LogReceivers = appCfg.Logging.Receivers
		proxyCfg.LogAttributes = appCfg.Logging.Attributes

		// Build filter configuration
		proxyCfg.Filter = buildFilterConfig(appCfg, cmd, filterDefault, allowDomains, blockDomains)

		proxyServer, err = proxy.NewServer(proxyCfg)
		if err != nil {
			return fmt.Errorf("failed to create proxy server: %w", err)
		}

		// Start proxy server
		if err := proxyServer.Start(); err != nil {
			return fmt.Errorf("failed to start proxy server: %w", err)
		}

		// Set up cleanup with proper synchronization
		var cleanupOnce sync.Once
		cleanup := func() {
			cleanupOnce.Do(func() {
				_ = proxyServer.Stop()
			})
		}

		// Ensure cleanup on normal exit
		defer cleanup()

		// Handle signals for graceful shutdown
		// We use a done channel to signal the main goroutine to exit after cleanup
		sigChan := make(chan os.Signal, 1)
		doneChan := make(chan struct{})
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sigChan
			cleanup()
			close(doneChan)
		}()

		// Check for signal-initiated shutdown after sandbox exits
		defer func() {
			select {
			case <-doneChan:
				// Signal received, exit cleanly (cleanup already done)
				os.Exit(0)
			default:
				// Normal exit, cleanup handled by defer
			}
		}()

		// Get actual port (may differ from requested if port was busy)
		actualPort := proxyServer.Port()
		cfg.ProxyPort = actualPort

		// Set gateway IP - pasta maps 10.0.2.2 to host's 127.0.0.1
		cfg.GatewayIP = netProvider.GatewayIP()
		cfg.ProxyCAPath = proxyCfg.CACertPath

		fmt.Fprintf(os.Stderr, "Proxy server started on 127.0.0.1:%d (provider: %s, gateway: %s)\n", actualPort, netProvider.Name(), cfg.GatewayIP)
		if actualPort != proxyPort {
			fmt.Fprintf(os.Stderr, "Note: Using port %d (requested port %d was busy)\n", actualPort, proxyPort)
		}
		fmt.Fprintf(os.Stderr, "CA certificate: %s\n", proxyCfg.CACertPath)

		// Show filter status
		if proxyCfg.Filter != nil && proxyCfg.Filter.IsEnabled() {
			if proxyCfg.Filter.DefaultAction == proxy.FilterActionAsk {
				fmt.Fprintf(os.Stderr, "Filter: ask mode (default action for unmatched requests)\n")
				fmt.Fprintf(os.Stderr, "\nRun in another terminal to approve/deny requests:\n")
				fmt.Fprintf(os.Stderr, "  devsandbox proxy monitor\n\n")
				fmt.Fprintf(os.Stderr, "Requests without response within 30s will be rejected.\n")
			} else {
				fmt.Fprintf(os.Stderr, "Filter: %d rules, default action: %s\n", len(proxyCfg.Filter.Rules), proxyCfg.Filter.DefaultAction)
			}
		}
	}

	builder := sandbox.NewBuilder(cfg)

	// Build sandbox arguments
	// Note: Order matters - later bindings override earlier ones for the same path
	builder.AddBaseArgs() // Creates /tmp as tmpfs
	builder.AddSystemBindings()
	builder.AddNetworkBindings()
	builder.AddLocaleBindings()
	builder.AddCABindings()
	builder.AddCustomMounts() // Custom mounts BEFORE sandbox home (for home paths)
	builder.AddSandboxHome()
	builder.AddProjectBindings()
	builder.AddTools()              // After project bindings so tools can override (e.g., .git read-only)
	builder.AddProxyCACertificate() // Must come after AddBaseArgs (needs /tmp tmpfs)
	builder.AddEnvironment()

	// Check for errors from building (e.g., critical tool setup failures)
	if err := builder.Err(); err != nil {
		return fmt.Errorf("failed to build sandbox: %w", err)
	}

	bwrapArgs := builder.Build()
	shellCmd := sandbox.BuildShellCommand(cfg, args)

	// Start active tools (e.g., docker proxy)
	startActiveTools, cleanupActiveTools := createActiveToolsRunner(cfg)
	defer cleanupActiveTools()
	hasActiveTools, err := startActiveTools(context.Background())
	if err != nil {
		return err
	}

	debug := os.Getenv("DEVSANDBOX_DEBUG") != ""
	if debug {
		fmt.Fprintln(os.Stderr, "=== Sandbox Debug ===")
		fmt.Fprintln(os.Stderr, "bwrap \\")
		for _, arg := range bwrapArgs {
			fmt.Fprintf(os.Stderr, "    %s \\\n", arg)
		}
		fmt.Fprintf(os.Stderr, "    -- %v\n", shellCmd)
		fmt.Fprintln(os.Stderr, "===================")
	}

	// Execute the sandbox
	if cfg.ProxyEnabled {
		// Use pasta to create isolated network namespace
		// pasta wraps bwrap and provides network connectivity via gateway IP
		// All traffic must go through pasta's virtual interface -> our proxy
		return bwrap.ExecWithPasta(bwrapArgs, shellCmd)
	}

	// Non-proxy mode
	if hasActiveTools {
		// Use ExecRun to keep parent process alive for ActiveTools (e.g., docker proxy)
		return bwrap.ExecRun(bwrapArgs, shellCmd)
	}

	// No ActiveTools: use syscall.Exec (replaces current process, more efficient)
	return bwrap.Exec(bwrapArgs, shellCmd)
}

func printInfo(cfg *sandbox.Config) {
	// Extract mise config from ToolsConfig
	miseWritable, misePersistent := getMiseConfig(cfg)

	fmt.Println("Sandbox Configuration:")
	fmt.Printf("  Project:      %s\n", cfg.ProjectName)
	fmt.Printf("  Project Dir:  %s\n", cfg.ProjectDir)
	fmt.Printf("  Sandbox Home: %s\n", cfg.SandboxHome)
	fmt.Printf("  Shell:        %s (%s)\n", cfg.Shell, cfg.ShellPath)
	fmt.Println()
	fmt.Println("Mounted Paths:")
	fmt.Println("  /usr, /lib, /lib64, /bin (read-only system)")
	fmt.Printf("  %s (read-write)\n", cfg.ProjectDir)
	if cfg.OverlayEnabled && miseWritable {
		mode := "tmpoverlay"
		if misePersistent {
			mode = "overlay"
		}
		fmt.Printf("  ~/.local/share/mise (%s, writable)\n", mode)
	} else {
		fmt.Println("  ~/.config/mise, ~/.local/share/mise (read-only tools)")
	}
	fmt.Printf("  Shell config for %s (read-only)\n", cfg.Shell)
	fmt.Println("  ~/.config/nvim, ~/.local/share/nvim (read-only editor)")
	fmt.Println()
	fmt.Println("Blocked Paths:")
	fmt.Println("  ~/.ssh, ~/.aws, ~/.azure, ~/.gcloud (not mounted)")
	fmt.Println("  .env, .env.* files (hidden, project secrets)")

	if cfg.MountsConfig != nil && len(cfg.MountsConfig.Rules()) > 0 {
		fmt.Println()
		fmt.Println("Custom Mounts:")
		for _, rule := range cfg.MountsConfig.Rules() {
			fmt.Printf("  %s (%s)\n", rule.Pattern, rule.Mode)
		}
	}

	if cfg.ProxyEnabled {
		fmt.Println()
		fmt.Println("Proxy Mode:")
		fmt.Printf("  Port:     %d\n", cfg.ProxyPort)
		fmt.Printf("  Log Dir:  %s/logs/proxy/\n", cfg.SandboxRoot)
		fmt.Printf("  CA Path:  %s\n", cfg.ProxyCAPath)
		fmt.Printf("  Gateway:  %s\n", cfg.GatewayIP)
	}

	if cfg.OverlayEnabled && miseWritable {
		fmt.Println()
		fmt.Println("Overlay Mode:")
		fmt.Printf("  Mise Writable:   %v\n", miseWritable)
		fmt.Printf("  Mise Persistent: %v\n", misePersistent)
		if misePersistent {
			fmt.Printf("  Overlay Dir:     %s/overlay/\n", cfg.SandboxHome)
		}
	}
}

// getMiseConfig extracts mise configuration from ToolsConfig.
func getMiseConfig(cfg *sandbox.Config) (writable, persistent bool) {
	if cfg.ToolsConfig == nil {
		return false, false
	}
	miseSection, ok := cfg.ToolsConfig["mise"]
	if !ok {
		return false, false
	}
	m, ok := miseSection.(map[string]any)
	if !ok {
		return false, false
	}
	if v, ok := m["writable"].(bool); ok {
		writable = v
	}
	if v, ok := m["persistent"].(bool); ok {
		persistent = v
	}
	return
}

// buildFilterConfig builds filter configuration from config file and CLI flags.
// CLI flags override config file settings.
func buildFilterConfig(appCfg *config.Config, cmd *cobra.Command, filterDefault string, allowDomains, blockDomains []string) *proxy.FilterConfig {
	filterCfg := proxy.DefaultFilterConfig()

	// Apply config file settings
	if appCfg.Proxy.Filter.DefaultAction != "" {
		filterCfg.DefaultAction = proxy.FilterAction(appCfg.Proxy.Filter.DefaultAction)
	}
	if appCfg.Proxy.Filter.AskTimeout > 0 {
		filterCfg.AskTimeout = appCfg.Proxy.Filter.AskTimeout
	}
	filterCfg.CacheDecisions = appCfg.Proxy.Filter.CacheDecisions

	// Convert config file rules
	for _, r := range appCfg.Proxy.Filter.Rules {
		filterCfg.Rules = append(filterCfg.Rules, proxy.FilterRule{
			Pattern: r.Pattern,
			Action:  proxy.FilterAction(r.Action),
			Scope:   proxy.FilterScope(r.Scope),
			Type:    proxy.PatternType(r.Type),
			Reason:  r.Reason,
		})
	}

	// CLI override for default action
	if cmd.Flags().Changed("filter-default") && filterDefault != "" {
		filterCfg.DefaultAction = proxy.FilterAction(filterDefault)
	}

	// Add CLI allow domains as rules
	for _, domain := range allowDomains {
		filterCfg.Rules = append(filterCfg.Rules, proxy.FilterRule{
			Pattern: domain,
			Action:  proxy.FilterActionAllow,
			Scope:   proxy.FilterScopeHost,
		})
	}

	// Add CLI block domains as rules
	for _, domain := range blockDomains {
		filterCfg.Rules = append(filterCfg.Rules, proxy.FilterRule{
			Pattern: domain,
			Action:  proxy.FilterActionBlock,
			Scope:   proxy.FilterScopeHost,
		})
	}

	// Auto-enable filtering if domains provided but no default action specified
	if filterCfg.DefaultAction == "" {
		if len(allowDomains) > 0 && len(blockDomains) == 0 {
			// Whitelist behavior: block unmatched
			filterCfg.DefaultAction = proxy.FilterActionBlock
		} else if len(blockDomains) > 0 && len(allowDomains) == 0 {
			// Blacklist behavior: allow unmatched
			filterCfg.DefaultAction = proxy.FilterActionAllow
		} else if len(allowDomains) > 0 && len(blockDomains) > 0 {
			// Mixed rules, use whitelist behavior (more restrictive)
			filterCfg.DefaultAction = proxy.FilterActionBlock
		}
	}

	return filterCfg
}

// createActiveToolsRunner creates an active tools runner with the sandbox configuration.
// Returns a start function and a cleanup function.
// The start function returns true if any tools were started.
func createActiveToolsRunner(cfg *sandbox.Config) (start func(ctx context.Context) (bool, error), cleanup func()) {
	// Create error logger for active tools
	logDir := filepath.Join(cfg.SandboxHome, proxy.LogBaseDirName, proxy.InternalLogDirName)
	errorLogger, err := logging.NewErrorLogger(filepath.Join(logDir, "tools-errors.log"))
	if err != nil {
		// If we can't create the logger, return a no-op runner
		// The start function will succeed but tools won't have logging
		errorLogger = nil
	}

	toolsCfg := tools.ActiveToolsConfig{
		HomeDir:        cfg.HomeDir,
		SandboxHome:    cfg.SandboxHome,
		OverlayEnabled: cfg.OverlayEnabled,
		ProjectDir:     cfg.ProjectDir,
		ToolsConfig:    cfg.ToolsConfig,
	}

	return tools.NewActiveToolsRunner(toolsCfg, errorLogger)
}
