package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"devsandbox/internal/config"
	"devsandbox/internal/embed"
	"devsandbox/internal/isolator"
	"devsandbox/internal/logging"
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
		Long: `devsandbox - Secure sandbox for running untrusted dev tools (bwrap and Docker backends)

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
  - bwrap: network isolated via pasta (requires passt package)
  - docker: proxy bound to per-session Docker network
  - Request logs: ~/.local/share/devsandbox/<project>/logs/proxy/`,
		Example: `  devsandbox                      # Interactive shell
  devsandbox npm install          # Install packages
  devsandbox --proxy npm install  # With proxy (traffic inspection)
  devsandbox --rm npm install     # Ephemeral: remove sandbox state after exit
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

	// Tool flags
	rootCmd.Flags().String("git-mode", "", "Override git tool mode for this session (readonly, readwrite, disabled)")

	// Filter flags
	rootCmd.Flags().String("filter-default", "", "Default filter action for unmatched requests: allow, block, or ask")
	rootCmd.Flags().StringSlice("allow-domain", nil, "Allow domain pattern (can be repeated)")
	rootCmd.Flags().StringSlice("block-domain", nil, "Block domain pattern (can be repeated)")

	// Isolation backend flag
	rootCmd.Flags().String("isolation", "", "Isolation backend: auto, bwrap, docker")

	// Sandbox lifecycle flag
	rootCmd.Flags().Bool("rm", false, "Remove sandbox state after exit (ephemeral mode)")

	// Add subcommands
	rootCmd.AddCommand(newSandboxesCmd())
	rootCmd.AddCommand(newDoctorCmd())
	rootCmd.AddCommand(newConfigCmd())
	rootCmd.AddCommand(newLogsCmd())
	rootCmd.AddCommand(newToolsCmd())
	rootCmd.AddCommand(newProxyCmd())
	rootCmd.AddCommand(newTrustCmd())
	rootCmd.AddCommand(newImageCmd())

	versionTpl := fmt.Sprintf("devsandbox %s (built: %s)\n", version.FullVersion(), version.Date)
	if runtime.GOOS == "linux" {
		versionTpl += fmt.Sprintf("  bwrap: %s  pasta: %s\n", embed.BwrapVersion, embed.PastaVersion)
	}
	rootCmd.SetVersionTemplate(versionTpl)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func runSandbox(cmd *cobra.Command, args []string) (retErr error) {
	// Track proxy result at function scope so the deferred signal check
	// can suppress errors from signaled child processes.
	var proxyRes *proxyResult
	defer func() {
		if proxyRes != nil && proxyRes.signaled.Load() {
			retErr = nil
		}
	}()

	showInfo, _ := cmd.Flags().GetBool("info")
	proxyEnabled, _ := cmd.Flags().GetBool("proxy")
	proxyPort, _ := cmd.Flags().GetInt("proxy-port")
	filterDefault, _ := cmd.Flags().GetString("filter-default")
	allowDomains, _ := cmd.Flags().GetStringSlice("allow-domain")
	blockDomains, _ := cmd.Flags().GetStringSlice("block-domain")

	// Load configuration file with project-specific overrides
	appCfg, _, projectDir, err := config.LoadConfig()
	if err != nil {
		return err
	}

	// Apply embedded binary setting before any embed.BwrapPath/PastaPath calls
	if !appCfg.Sandbox.IsUseEmbeddedEnabled() {
		embed.Disabled = true
	}

	// Determine isolation backend
	isolationFlag, _ := cmd.Flags().GetString("isolation")
	isolation := appCfg.Sandbox.GetIsolation()
	if cmd.Flags().Changed("isolation") {
		isolation = config.IsolationBackend(isolationFlag)
	}

	// Sandbox lifecycle
	rmFlag, _ := cmd.Flags().GetBool("rm")
	keepContainer := appCfg.Sandbox.Docker.IsKeepContainerEnabled()
	if cmd.Flags().Changed("rm") && rmFlag {
		keepContainer = false
	}

	// Build isolator with functional options
	iso, err := isolator.MustNew(isolator.Backend(isolation),
		isolator.WithDockerConfig(
			appCfg.Sandbox.Docker.Dockerfile,
			config.ConfigDir(),
			appCfg.Sandbox.Docker.Resources.Memory,
			appCfg.Sandbox.Docker.Resources.CPUs,
			keepContainer,
		),
	)
	if err != nil {
		return err
	}

	// Create sandbox config
	cfg, err := sandbox.NewConfig(&sandbox.Options{BasePath: appCfg.Sandbox.BasePath})
	if err != nil {
		return err
	}

	// Apply config file defaults, then CLI overrides
	if appCfg.Proxy.IsEnabled() {
		cfg.ProxyEnabled = true
	}
	if appCfg.Proxy.Port != 0 {
		cfg.ProxyPort = appCfg.Proxy.Port
	}
	if cmd.Flags().Changed("proxy") {
		cfg.ProxyEnabled = proxyEnabled
	}
	if cmd.Flags().Changed("proxy-port") {
		cfg.ProxyPort = proxyPort
	}

	// CLI override for git mode
	if cmd.Flags().Changed("git-mode") {
		gitMode, _ := cmd.Flags().GetString("git-mode")
		if !tools.ValidGitMode(gitMode) {
			return fmt.Errorf("invalid --git-mode value %q: must be readonly, readwrite, or disabled", gitMode)
		}
		if appCfg.Tools == nil {
			appCfg.Tools = make(map[string]any)
		}
		gitCfg, ok := appCfg.Tools["git"].(map[string]any)
		if !ok {
			gitCfg = make(map[string]any)
		}
		gitCfg["mode"] = gitMode
		appCfg.Tools["git"] = gitCfg
	}

	cfg.OverlayEnabled = appCfg.Overlay.IsEnabled()
	cfg.ToolsConfig = appCfg.Tools
	cfg.ConfigVisibility = string(appCfg.Sandbox.GetConfigVisibility())
	cfg.MountsConfig = mounts.NewEngine(appCfg.Sandbox.Mounts, cfg.HomeDir)
	cfg.Isolation = iso.IsolationType()

	if showInfo {
		printInfo(cfg)
		return nil
	}

	if err := cfg.EnsureSandboxDirs(); err != nil {
		return err
	}

	// When --rm is set, remove sandbox state after exit (both backends).
	if rmFlag {
		defer func() {
			if err := sandbox.RemoveSandbox(cfg.SandboxRoot); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to remove sandbox: %v\n", err)
			}
		}()
	}

	// PrepareNetwork: backend-specific network setup before proxy starts
	var netInfo *isolator.NetworkInfo
	if cfg.ProxyEnabled {
		netInfo, err = iso.PrepareNetwork(cmd.Context(), cfg.ProjectDir)
		if err != nil {
			return err
		}
	}
	defer func() { _ = iso.Cleanup() }()

	// Set up logging infrastructure (shared between proxy and sandbox)
	logDir := filepath.Join(cfg.SandboxHome, proxy.LogBaseDirName, proxy.InternalLogDirName)
	sandboxLogger, err := logging.NewErrorLogger(filepath.Join(logDir, "sandbox.log"))
	if err != nil {
		sandboxLogger = nil
	}

	var logDispatcher *logging.Dispatcher
	if len(appCfg.Logging.Receivers) > 0 {
		logDispatcher, err = logging.NewDispatcherFromConfig(
			appCfg.Logging.Receivers, appCfg.Logging.Attributes, logDir,
		)
		if err != nil {
			return fmt.Errorf("failed to create log dispatcher: %w", err)
		}
		defer func() { _ = logDispatcher.Close() }()
	}

	// Start proxy if enabled
	var proxyServer *proxy.Server
	if cfg.ProxyEnabled {
		pCfg := proxy.NewConfig(cfg.SandboxRoot, proxyPort)
		pCfg.Dispatcher = logDispatcher
		pCfg.LogReceivers = appCfg.Logging.Receivers
		pCfg.LogAttributes = appCfg.Logging.Attributes
		pCfg.CredentialInjectors = proxy.BuildCredentialInjectors(appCfg.Proxy.Credentials)
		pCfg.Filter = buildFilterConfig(appCfg, cmd, filterDefault, allowDomains, blockDomains)
		pCfg.Redaction = buildRedactionConfig(&appCfg.Proxy.Redaction)
		pCfg.ProjectDir = projectDir

		if netInfo != nil {
			pCfg.BindAddress = netInfo.BindAddress
		}

		proxyRes, err = startProxyServer(pCfg)
		if err != nil {
			return err
		}
		defer deferProxyCleanup(proxyRes)

		cfg.ProxyPort = proxyRes.port
		proxyServer = proxyRes.server

		fmt.Fprintf(os.Stderr, "Proxy server started on %s:%d\n", pCfg.GetBindAddress(), proxyRes.port)
		if proxyRes.port != proxyPort {
			fmt.Fprintf(os.Stderr, "Note: Using port %d (requested port %d was busy)\n", proxyRes.port, proxyPort)
		}
		fmt.Fprintf(os.Stderr, "CA certificate: %s\n", proxyRes.caPath)

		if pCfg.Filter != nil && pCfg.Filter.IsEnabled() {
			if pCfg.Filter.DefaultAction == proxy.FilterActionAsk {
				fmt.Fprintf(os.Stderr, "Filter: ask mode (default action for unmatched requests)\n")
				fmt.Fprintf(os.Stderr, "\nRun in another terminal to approve/deny requests:\n")
				fmt.Fprintf(os.Stderr, "  devsandbox proxy monitor\n\n")
				fmt.Fprintf(os.Stderr, "Requests without response within 30s will be rejected.\n")
			} else {
				fmt.Fprintf(os.Stderr, "Filter: %d rules, default action: %s\n", len(pCfg.Filter.Rules), pCfg.Filter.DefaultAction)
			}
		}

		if pCfg.Redaction != nil && pCfg.Redaction.IsEnabled() {
			fmt.Fprintf(os.Stderr, "Redaction: %d rules, default action: %s\n",
				len(pCfg.Redaction.Rules), pCfg.Redaction.GetDefaultAction())
		}
	}

	// Start active tools (e.g., docker proxy)
	startActiveTools, cleanupActiveTools := createActiveToolsRunner(cfg)
	defer cleanupActiveTools()
	hasActiveTools, err := startActiveTools(cmd.Context())
	if err != nil {
		return err
	}

	// Acquire session lock
	lockFile, err := sandbox.AcquireSessionLock(cfg.SandboxRoot)
	if err != nil {
		return fmt.Errorf("failed to acquire session lock: %w", err)
	}
	defer func() { _ = lockFile.Close() }()

	// Build RunConfig and delegate to the isolator
	var proxyCAPath string
	if proxyRes != nil {
		proxyCAPath = proxyRes.caPath
	}

	runCfg := &isolator.RunConfig{
		SandboxCfg:     cfg,
		AppCfg:         appCfg,
		Command:        args,
		Interactive:    term.IsTerminal(int(os.Stdin.Fd())),
		RemoveOnExit:   rmFlag,
		HasActiveTools: hasActiveTools,
		ProxyServer:    proxyServer,
		ProxyCAPath:    proxyCAPath,
		ProxyPort:      cfg.ProxyPort,
		SandboxLogger:  sandboxLogger,
		LogDispatcher:  logDispatcher,
	}

	return iso.Run(cmd.Context(), runCfg)
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

// buildRedactionConfig converts config types to proxy redaction types.
func buildRedactionConfig(cfg *config.ProxyRedactionConfig) *proxy.RedactionConfig {
	if cfg == nil {
		return nil
	}
	redCfg := &proxy.RedactionConfig{
		Enabled:       cfg.Enabled,
		DefaultAction: proxy.RedactionAction(cfg.DefaultAction),
	}
	for _, r := range cfg.Rules {
		rule := proxy.RedactionRule{
			Name:    r.Name,
			Action:  proxy.RedactionAction(r.Action),
			Pattern: r.Pattern,
		}
		if r.Source != nil {
			rule.Source = &proxy.RedactionSource{
				Value:      r.Source.Value,
				Env:        r.Source.Env,
				File:       r.Source.File,
				EnvFileKey: r.Source.EnvFileKey,
			}
		}
		redCfg.Rules = append(redCfg.Rules, rule)
	}
	return redCfg
}

// proxyResult holds the running proxy server and its cleanup/signal handling.
type proxyResult struct {
	server  *proxy.Server
	cleanup func()
	port    int
	caPath  string
	// signaled is set when a signal triggered shutdown (accessed from goroutine).
	signaled atomic.Bool
}

// startProxyServer creates, starts, and returns a proxy server with signal-based cleanup.
func startProxyServer(pCfg *proxy.Config) (*proxyResult, error) {
	server, err := proxy.NewServer(pCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create proxy server: %w", err)
	}

	if err := server.Start(); err != nil {
		return nil, fmt.Errorf("failed to start proxy server: %w", err)
	}

	result := &proxyResult{
		server: server,
		port:   server.Port(),
		caPath: pCfg.CACertPath,
	}

	var cleanupOnce sync.Once
	result.cleanup = func() {
		cleanupOnce.Do(func() {
			_ = server.Stop()
		})
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		signal.Stop(sigChan)
		result.signaled.Store(true)
		result.cleanup()
	}()

	return result, nil
}

// deferProxyCleanup stops the proxy server. If shutdown was triggered by a
// signal, it sets an exit code that the caller should check after all other
// defers have run (via proxyResult.signaled).
// Call as: defer deferProxyCleanup(result)
func deferProxyCleanup(result *proxyResult) {
	result.cleanup()
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
