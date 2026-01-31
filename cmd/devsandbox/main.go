package main

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/spf13/cobra"

	"devsandbox/internal/bwrap"
	"devsandbox/internal/network"
	"devsandbox/internal/proxy"
	"devsandbox/internal/sandbox"
)

// Set via ldflags at build time
var (
	version = "dev"
	date    = "unknown"
)

const appVersion = "1.0.0"

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
  - Request logs: ~/.local/share/devsandbox/<project>/proxy-logs/`,
		Example: `  devsandbox                      # Interactive shell
  devsandbox npm install          # Install packages
  devsandbox --proxy npm install  # With proxy (traffic inspection)
  devsandbox claude --dangerously-skip-permissions
  devsandbox bun run dev`,
		Version:               appVersion,
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

	// Add subcommands
	rootCmd.AddCommand(newSandboxesCmd())
	rootCmd.AddCommand(newDoctorCmd())

	rootCmd.SetVersionTemplate(fmt.Sprintf("devsandbox v%s (commit: %s, built: %s)\n", appVersion, version, date))

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func runSandbox(cmd *cobra.Command, args []string) error {
	showInfo, _ := cmd.Flags().GetBool("info")
	proxyEnabled, _ := cmd.Flags().GetBool("proxy")
	proxyPort, _ := cmd.Flags().GetInt("proxy-port")

	cfg, err := sandbox.NewConfig()
	if err != nil {
		return err
	}

	// Configure proxy settings
	cfg.ProxyEnabled = proxyEnabled
	cfg.ProxyPort = proxyPort

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

		// Handle signals for graceful shutdown
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sigChan
			cleanup()
			os.Exit(0)
		}()

		// Ensure cleanup on normal exit too
		defer cleanup()

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
	}

	builder := sandbox.NewBuilder(cfg)

	// Build sandbox arguments
	// Note: Order matters - tmpfs for /tmp must be created before mounting CA cert to /tmp
	builder.AddBaseArgs() // Creates /tmp as tmpfs
	builder.AddSystemBindings()
	builder.AddNetworkBindings()
	builder.AddLocaleBindings()
	builder.AddCABindings()
	builder.AddSandboxHome()
	builder.AddToolBindings()
	builder.AddGitConfig()
	builder.AddProjectBindings()
	builder.AddAIToolBindings()
	builder.AddProxyCACertificate() // Must come after AddBaseArgs (needs /tmp tmpfs)
	builder.AddEnvironment()

	bwrapArgs := builder.Build()
	shellCmd := sandbox.BuildShellCommand(cfg, args)

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

	// Non-proxy mode: use syscall.Exec (replaces current process)
	return bwrap.Exec(bwrapArgs, shellCmd)
}

func printInfo(cfg *sandbox.Config) {
	fmt.Println("Sandbox Configuration:")
	fmt.Printf("  Project:      %s\n", cfg.ProjectName)
	fmt.Printf("  Project Dir:  %s\n", cfg.ProjectDir)
	fmt.Printf("  Sandbox Home: %s\n", cfg.SandboxHome)
	fmt.Printf("  Shell:        %s (%s)\n", cfg.Shell, cfg.ShellPath)
	fmt.Println()
	fmt.Println("Mounted Paths:")
	fmt.Println("  /usr, /lib, /lib64, /bin (read-only system)")
	fmt.Printf("  %s (read-write)\n", cfg.ProjectDir)
	fmt.Println("  ~/.config/mise, ~/.local/share/mise (read-only tools)")
	fmt.Printf("  Shell config for %s (read-only)\n", cfg.Shell)
	fmt.Println("  ~/.config/nvim, ~/.local/share/nvim (read-only editor)")
	fmt.Println()
	fmt.Println("Blocked Paths:")
	fmt.Println("  ~/.ssh (no SSH access)")
	fmt.Println("  ~/.gitconfig (no git credentials)")
	fmt.Println("  ~/.aws, ~/.azure, ~/.gcloud (no cloud credentials)")
	fmt.Println("  .env, .env.* files (secrets blocked)")

	if cfg.ProxyEnabled {
		fmt.Println()
		fmt.Println("Proxy Mode:")
		fmt.Printf("  Port:     %d\n", cfg.ProxyPort)
		fmt.Printf("  Log Dir:  %s/proxy-logs/\n", cfg.SandboxRoot)
		fmt.Printf("  CA Path:  %s\n", cfg.ProxyCAPath)
		fmt.Printf("  Gateway:  %s\n", cfg.GatewayIP)
	}
}
