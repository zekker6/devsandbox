package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"devsandbox/internal/sandbox/tools"
)

// isTTY reports whether stdout is connected to a terminal.
var isTTY = sync.OnceValue(func() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
})

// color wraps text in ANSI color codes when stdout is a TTY.
// Returns plain text otherwise.
func color(code, text string) string {
	if !isTTY() {
		return text
	}
	return code + text + "\033[0m"
}

func newToolsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tools",
		Short: "Inspect available tools and their configuration",
		Long: `Inspect tools available in the sandbox.

Shows which tools are detected, their bindings (filesystem mounts),
environment variables, and shell initialization commands.`,
		Example: `  devsandbox tools list              # List available tools
  devsandbox tools list --all        # Include unavailable tools
  devsandbox tools info mise         # Show details for mise
  devsandbox tools info --all        # Show details for all tools
  devsandbox tools check             # Verify tool requirements`,
	}

	cmd.AddCommand(newToolsListCmd())
	cmd.AddCommand(newToolsInfoCmd())
	cmd.AddCommand(newToolsCheckCmd())

	return cmd
}

// ToolInfo represents tool information for JSON output
type ToolInfo struct {
	Name        string        `json:"name"`
	Available   bool          `json:"available"`
	Description string        `json:"description"`
	BinaryPath  string        `json:"binary_path,omitempty"`
	Bindings    []BindingInfo `json:"bindings,omitempty"`
	Environment []EnvVarInfo  `json:"environment,omitempty"`
	ShellInit   string        `json:"shell_init,omitempty"`
	HasSetup    bool          `json:"has_setup"`
}

// BindingInfo represents a filesystem binding
type BindingInfo struct {
	Source   string `json:"source"`
	Dest     string `json:"dest"`
	ReadOnly bool   `json:"read_only"`
	Optional bool   `json:"optional"`
	Exists   bool   `json:"exists"`
}

// EnvVarInfo represents an environment variable
type EnvVarInfo struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	FromHost bool   `json:"from_host"`
}

func newToolsListCmd() *cobra.Command {
	var (
		showAll    bool
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all tools",
		Long:  "List all registered tools with their availability status",
		Example: `  devsandbox tools list
  devsandbox tools list --all
  devsandbox tools list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return err
			}

			allTools := tools.All()
			var toolInfos []ToolInfo

			for _, tool := range allTools {
				available := tool.Available(homeDir)
				if !showAll && !available {
					continue
				}

				info := ToolInfo{
					Name:        tool.Name(),
					Available:   available,
					Description: tool.Description(),
				}
				toolInfos = append(toolInfos, info)
			}

			if jsonOutput {
				encoder := json.NewEncoder(os.Stdout)
				encoder.SetIndent("", "  ")
				return encoder.Encode(toolInfos)
			}

			if len(toolInfos) == 0 {
				fmt.Println("No tools found.")
				return nil
			}

			table := tablewriter.NewWriter(os.Stdout)
			table.Header("NAME", "STATUS", "DESCRIPTION")

			for _, info := range toolInfos {
				status := "available"
				if !info.Available {
					status = "missing"
				}
				_ = table.Append(info.Name, status, info.Description)
			}

			return table.Render()
		},
	}

	cmd.Flags().BoolVar(&showAll, "all", false, "Show all tools (including unavailable)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}

func newToolsInfoCmd() *cobra.Command {
	var (
		showAll    bool
		jsonOutput bool
		resolve    bool
	)

	cmd := &cobra.Command{
		Use:   "info [tool-name]",
		Short: "Show detailed tool information",
		Long:  "Show bindings, environment variables, and shell init for a tool",
		Example: `  devsandbox tools info mise
  devsandbox tools info git --resolve
  devsandbox tools info --all
  devsandbox tools info --all --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return err
			}

			// Determine sandbox home (use a placeholder for display)
			sandboxHome := filepath.Join(homeDir, ".local/share/devsandbox/<project>/home")

			var toolsToShow []tools.Tool

			if showAll {
				toolsToShow = tools.All()
			} else if len(args) > 0 {
				tool := tools.Get(args[0])
				if tool == nil {
					return fmt.Errorf("unknown tool: %s", args[0])
				}
				toolsToShow = []tools.Tool{tool}
			} else {
				return fmt.Errorf("specify a tool name or use --all")
			}

			var toolInfos []ToolInfo

			for _, tool := range toolsToShow {
				info := buildToolInfo(tool, homeDir, sandboxHome, resolve)
				toolInfos = append(toolInfos, info)
			}

			if jsonOutput {
				encoder := json.NewEncoder(os.Stdout)
				encoder.SetIndent("", "  ")
				return encoder.Encode(toolInfos)
			}

			for i, info := range toolInfos {
				if i > 0 {
					fmt.Println()
					fmt.Println(strings.Repeat("-", 60))
					fmt.Println()
				}
				printToolInfo(info, homeDir, resolve)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&showAll, "all", false, "Show info for all tools")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	cmd.Flags().BoolVar(&resolve, "resolve", false, "Show resolved paths instead of ~ notation")

	return cmd
}

func newToolsCheckCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "check [tool-names...]",
		Short: "Verify tool requirements",
		Long:  "Check tool availability and verify binding paths exist",
		Example: `  devsandbox tools check
  devsandbox tools check mise git
  devsandbox tools check --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return err
			}

			sandboxHome := filepath.Join(homeDir, ".local/share/devsandbox/<project>/home")

			var toolsToCheck []tools.Tool

			if len(args) > 0 {
				for _, name := range args {
					tool := tools.Get(name)
					if tool == nil {
						return fmt.Errorf("unknown tool: %s", name)
					}
					toolsToCheck = append(toolsToCheck, tool)
				}
			} else {
				toolsToCheck = tools.All()
			}

			type CheckResult struct {
				Name       string        `json:"name"`
				Available  bool          `json:"available"`
				BinaryPath string        `json:"binary_path,omitempty"`
				Bindings   []BindingInfo `json:"bindings,omitempty"`
				Issues     []string      `json:"issues,omitempty"`
			}

			var results []CheckResult
			available := 0
			total := len(toolsToCheck)

			for _, tool := range toolsToCheck {
				result := CheckResult{
					Name:      tool.Name(),
					Available: tool.Available(homeDir),
				}

				// Use ToolWithCheck if available for richer info
				if checker, ok := tool.(tools.ToolWithCheck); ok {
					checkResult := checker.Check(homeDir)
					result.Available = checkResult.Available
					result.BinaryPath = checkResult.BinaryPath
					result.Issues = checkResult.Issues
				}

				if result.Available {
					available++
				}

				// Check bindings
				bindings := tool.Bindings(homeDir, sandboxHome)
				for _, b := range bindings {
					bi := BindingInfo{
						Source:   b.Source,
						Dest:     b.Dest,
						ReadOnly: b.ReadOnly,
						Optional: b.Optional,
					}
					_, err := os.Stat(b.Source)
					bi.Exists = err == nil

					if !bi.Exists && !b.Optional {
						result.Issues = append(result.Issues, fmt.Sprintf("missing required path: %s", b.Source))
					}

					result.Bindings = append(result.Bindings, bi)
				}

				results = append(results, result)
			}

			if jsonOutput {
				encoder := json.NewEncoder(os.Stdout)
				encoder.SetIndent("", "  ")
				return encoder.Encode(results)
			}

			fmt.Println("Checking tools...")
			fmt.Println()

			for _, r := range results {
				if r.Available {
					fmt.Printf("%s %s", color("\033[32m", "✓"), r.Name)
					if r.BinaryPath != "" {
						fmt.Printf(" (%s)", r.BinaryPath)
					}
					fmt.Println()
				} else {
					fmt.Printf("%s %s (not available)\n", color("\033[31m", "✗"), r.Name)
				}

				// Show binding status for available tools
				if r.Available && len(r.Bindings) > 0 {
					for _, b := range r.Bindings {
						status := color("\033[32m", "✓")
						note := ""
						if !b.Exists {
							if b.Optional {
								status = color("\033[33m", "○")
								note = " (optional, missing)"
							} else {
								status = color("\033[31m", "✗")
								note = " (missing!)"
							}
						}
						src := shortenPath(b.Source, homeDir)
						fmt.Printf("    %s %s%s\n", status, src, note)
					}
				}

				if len(r.Issues) > 0 {
					for _, issue := range r.Issues {
						fmt.Printf("    %s %s\n", color("\033[33m", "!"), issue)
					}
				}
			}

			fmt.Println()
			fmt.Printf("Summary: %d/%d tools available\n", available, total)

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}

func buildToolInfo(tool tools.Tool, homeDir, sandboxHome string, resolve bool) ToolInfo {
	info := ToolInfo{
		Name:        tool.Name(),
		Available:   tool.Available(homeDir),
		Description: tool.Description(),
	}

	// Use ToolWithCheck if available for richer info
	if checker, ok := tool.(tools.ToolWithCheck); ok {
		checkResult := checker.Check(homeDir)
		info.BinaryPath = checkResult.BinaryPath
	}

	// Get bindings
	bindings := tool.Bindings(homeDir, sandboxHome)
	for _, b := range bindings {
		bi := BindingInfo{
			Source:   b.Source,
			Dest:     b.Dest,
			ReadOnly: b.ReadOnly,
			Optional: b.Optional,
		}
		_, err := os.Stat(b.Source)
		bi.Exists = err == nil

		if !resolve {
			bi.Source = shortenPath(bi.Source, homeDir)
			bi.Dest = shortenPath(bi.Dest, homeDir)
		}

		info.Bindings = append(info.Bindings, bi)
	}

	// Get environment variables
	envVars := tool.Environment(homeDir, sandboxHome)
	for _, e := range envVars {
		ev := EnvVarInfo{
			Name:     e.Name,
			Value:    e.Value,
			FromHost: e.FromHost,
		}
		if !resolve {
			ev.Value = shortenPath(ev.Value, homeDir)
		}
		info.Environment = append(info.Environment, ev)
	}

	// Get shell init
	info.ShellInit = tool.ShellInit("bash")

	// Check if has setup
	_, info.HasSetup = tool.(tools.ToolWithSetup)

	return info
}

func printToolInfo(info ToolInfo, _ string, _ bool) {
	status := color("\033[32m", "available")
	if !info.Available {
		status = color("\033[31m", "missing")
	}

	fmt.Printf("Tool: %s\n", info.Name)
	fmt.Printf("Status: %s\n", status)
	fmt.Printf("Description: %s\n", info.Description)

	if info.BinaryPath != "" {
		fmt.Printf("Binary: %s\n", info.BinaryPath)
	}

	if info.HasSetup {
		fmt.Println()
		fmt.Println("Setup: Has setup phase (generates config files)")
	}

	if len(info.Bindings) > 0 {
		fmt.Println()
		fmt.Println("Bindings:")
		for _, b := range info.Bindings {
			mode := "read-only"
			if !b.ReadOnly {
				mode = "read-write"
			}
			optional := ""
			if b.Optional {
				optional = ", optional"
			}
			exists := ""
			if !b.Exists {
				exists = " [missing]"
			}

			dest := b.Dest
			if dest == "" || dest == b.Source {
				fmt.Printf("  %-35s (%s%s)%s\n", b.Source, mode, optional, exists)
			} else {
				fmt.Printf("  %-35s → %s (%s%s)%s\n", b.Source, dest, mode, optional, exists)
			}
		}
	}

	if len(info.Environment) > 0 {
		fmt.Println()
		fmt.Println("Environment Variables:")
		for _, e := range info.Environment {
			if e.FromHost {
				fmt.Printf("  %s (from host)\n", e.Name)
			} else {
				fmt.Printf("  %s=%s\n", e.Name, e.Value)
			}
		}
	} else {
		fmt.Println()
		fmt.Println("Environment Variables: (none)")
	}

	if info.ShellInit != "" {
		fmt.Println()
		fmt.Println("Shell Init:")
		// Indent each line
		for _, line := range strings.Split(info.ShellInit, "\n") {
			if line != "" {
				fmt.Printf("  %s\n", line)
			}
		}
	}
}

func shortenPath(path, homeDir string) string {
	if strings.HasPrefix(path, homeDir) {
		return "~" + path[len(homeDir):]
	}
	return path
}
