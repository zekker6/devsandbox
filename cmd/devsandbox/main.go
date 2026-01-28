package main

import (
	"fmt"
	"os"
	"os/signal"
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
  - Home directory: sandboxed per-project in ~/.sandboxes/<project>/

Proxy Mode (--proxy):
  - All HTTP/HTTPS traffic routed through local proxy
  - MITM proxy with auto-generated CA certificate
  - Network isolated via pasta/slirp4netns`,
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
	rootCmd.Flags().Bool("proxy-log", false, "Log all HTTP/HTTPS requests through proxy")

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
	proxyLog, _ := cmd.Flags().GetBool("proxy-log")

	cfg, err := sandbox.NewConfig()
	if err != nil {
		return err
	}

	// Configure proxy settings
	cfg.ProxyEnabled = proxyEnabled
	cfg.ProxyPort = proxyPort
	cfg.ProxyLog = proxyLog

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
		// Check for network provider
		netProvider, err = network.SelectProvider()
		if err != nil {
			return fmt.Errorf("proxy mode requires pasta or slirp4netns: %w", err)
		}

		// Set up proxy
		proxyCfg := proxy.NewConfig(cfg.SandboxBase(), proxyPort, proxyLog)
		proxyServer, err = proxy.NewServer(proxyCfg)
		if err != nil {
			return fmt.Errorf("failed to create proxy server: %w", err)
		}

		// Start proxy server
		if err := proxyServer.Start(); err != nil {
			return fmt.Errorf("failed to start proxy server: %w", err)
		}

		// Set up cleanup on signals
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sigChan
			proxyServer.Stop()
			if netProvider != nil {
				netProvider.Stop()
			}
			os.Exit(0)
		}()

		cfg.GatewayIP = netProvider.GatewayIP()
		cfg.ProxyCAPath = proxyCfg.CACertPath

		fmt.Fprintf(os.Stderr, "Proxy server started on :%d (provider: %s)\n", proxyPort, netProvider.Name())
		fmt.Fprintf(os.Stderr, "CA certificate: %s\n", proxyCfg.CACertPath)
	}

	builder := sandbox.NewBuilder(cfg)
	builder.AddBaseArgs()
	builder.AddSystemBindings()
	builder.AddNetworkBindings()
	builder.AddLocaleBindings()
	builder.AddCABindings()
	builder.AddSandboxHome()
	builder.AddToolBindings()
	builder.AddGitConfig()
	builder.AddProjectBindings()
	builder.AddAIToolBindings()
	builder.AddProxyCACertificate()
	builder.AddEnvironment()

	bwrapArgs := builder.Build()
	shellCmd := sandbox.BuildShellCommand(cfg, args)

	debug := os.Getenv("SANDBOX_DEBUG") != ""
	if debug {
		fmt.Fprintln(os.Stderr, "=== Sandbox Debug ===")
		fmt.Fprintln(os.Stderr, "bwrap \\")
		for _, arg := range bwrapArgs {
			fmt.Fprintf(os.Stderr, "    %s \\\n", arg)
		}
		fmt.Fprintf(os.Stderr, "    -- %v\n", shellCmd)
		fmt.Fprintln(os.Stderr, "===================")
	}

	// Note: In proxy mode, we would need to launch the sandbox in a network namespace
	// and then start pasta/slirp4netns to connect it. This requires bwrap --unshare-net
	// followed by pasta --netns /proc/$PID/ns/net, which is complex to orchestrate.
	//
	// For now, proxy mode sets up the proxy server and environment variables.
	// Full network namespace isolation would require additional orchestration.

	return bwrap.Exec(bwrapArgs, shellCmd)
}

func printInfo(cfg *sandbox.Config) {
	fmt.Println("Sandbox Configuration:")
	fmt.Printf("  Project:      %s\n", cfg.ProjectName)
	fmt.Printf("  Project Dir:  %s\n", cfg.ProjectDir)
	fmt.Printf("  Sandbox Home: %s\n", cfg.SandboxHome)
	fmt.Println()
	fmt.Println("Mounted Paths:")
	fmt.Println("  /usr, /lib, /lib64, /bin (read-only system)")
	fmt.Printf("  %s (read-write)\n", cfg.ProjectDir)
	fmt.Println("  ~/.config/mise, ~/.local/share/mise (read-only tools)")
	fmt.Println("  ~/.config/fish (read-only shell config)")
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
		fmt.Printf("  Logging:  %v\n", cfg.ProxyLog)
		fmt.Printf("  CA Path:  %s\n", cfg.ProxyCAPath)
		fmt.Printf("  Gateway:  %s\n", cfg.GatewayIP)
	}
}
