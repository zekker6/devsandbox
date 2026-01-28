package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"devsandbox/internal/bwrap"
	"devsandbox/internal/sandbox"
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
  - Home directory: sandboxed per-project in ~/.sandboxes/<project>/`,
		Example: `  devsandbox                      # Interactive shell
  devsandbox npm install          # Install packages
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

	rootCmd.SetVersionTemplate(fmt.Sprintf("devsandbox v%s\n", appVersion))

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func runSandbox(cmd *cobra.Command, args []string) error {
	showInfo, _ := cmd.Flags().GetBool("info")

	cfg, err := sandbox.NewConfig()
	if err != nil {
		return err
	}

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
}
