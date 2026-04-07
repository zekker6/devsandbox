package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"devsandbox/internal/sandbox"
)

// parseScratchpadArgs splits positional args into (scratchpad name, passthrough
// command).
//
// Rule: if the first arg passes sandbox.ValidateScratchpadName, it is
// consumed as the scratchpad name and the rest is the command. Otherwise
// the default scratchpad is used and all positionals are treated as the
// command — this is how command-only invocations like
// "devsandbox scratchpad ../escape" or "devsandbox scratchpad -not-a-flag"
// fall through cleanly.
//
// To run a command in the default scratchpad, name it explicitly:
//
//	devsandbox scratchpad default npm install
//
// This avoids the ambiguity of "devsandbox scratchpad npm install" — where
// "npm" is a valid scratchpad name — by making the intent explicit.
func parseScratchpadArgs(args []string) (name string, cmdArgs []string, err error) {
	if len(args) > 0 {
		if verr := sandbox.ValidateScratchpadName(args[0]); verr == nil {
			return args[0], args[1:], nil
		}
	}
	return sandbox.DefaultScratchpadName, args, nil
}

func newScratchpadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "scratchpad [name] [command...]",
		Aliases: []string{"sp"},
		Short:   "Run a sandbox in a managed, clean scratchpad directory (alias: sp)",
		Long: `Run a sandbox in a managed, clean working directory.

Scratchpads are empty directories created and managed by devsandbox under
~/.local/share/devsandbox-scratchpads/. State is preserved between runs so
you can come back to the same workspace. Use 'devsandbox scratchpad list' to
see them and 'devsandbox scratchpad rm <name>' to clean up.

Local .devsandbox.toml files are never loaded inside a scratchpad — the
baseline is always global config only.`,
		Example: `  devsandbox scratchpad                         # default scratchpad, interactive shell
  devsandbox scratchpad foo                     # named scratchpad "foo", interactive shell
  devsandbox scratchpad foo claude              # named scratchpad "foo", run command
  devsandbox scratchpad default npm install     # default scratchpad, run command
  devsandbox scratchpad --rm foo bun init       # ephemeral run in a named scratchpad`,
		Args:                  cobra.ArbitraryArgs,
		DisableFlagsInUseLine: true,
		SilenceUsage:          true,
		SilenceErrors:         true,
		Annotations: map[string]string{
			"scratchpad": "true",
		},
		RunE: runScratchpad,
	}

	cmd.Flags().SetInterspersed(false)
	addSandboxFlags(cmd)

	cmd.AddCommand(newScratchpadListCmd())
	cmd.AddCommand(newScratchpadRmCmd())

	return cmd
}

func runScratchpad(cmd *cobra.Command, args []string) error {
	name, cmdArgs, err := parseScratchpadArgs(args)
	if err != nil {
		return err
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to determine home directory: %w", err)
	}

	workDir := sandbox.ScratchpadDir(homeDir, name)
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return fmt.Errorf("failed to create scratchpad dir %q: %w", workDir, err)
	}

	if err := os.Chdir(workDir); err != nil {
		return fmt.Errorf("failed to enter scratchpad dir %q: %w", workDir, err)
	}

	fmt.Fprintf(os.Stderr, "Scratchpad: %s (%s)\n", name, workDir)

	return runSandbox(cmd, cmdArgs)
}

// ScratchpadInfo is the shape used by `scratchpad list --json`.
type ScratchpadInfo struct {
	Name        string `json:"name"`
	WorkingDir  string `json:"working_dir"`
	SandboxName string `json:"sandbox_name"`
	HasState    bool   `json:"has_state"`
	SizeBytes   int64  `json:"size_bytes"`
}

func newScratchpadListCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all scratchpads",
		Long:  "List scratchpad working directories and whether they have persistent sandbox state.",
		RunE: func(cmd *cobra.Command, args []string) error {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return err
			}

			scratchpads, err := collectScratchpads(homeDir)
			if err != nil {
				return err
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(scratchpads)
			}

			if len(scratchpads) == 0 {
				fmt.Println("No scratchpads found.")
				return nil
			}

			for _, s := range scratchpads {
				stateMark := ""
				if !s.HasState {
					stateMark = " (no state)"
				}
				fmt.Printf("%-20s  %s  %s%s\n",
					s.Name,
					sandbox.FormatSize(s.SizeBytes),
					s.WorkingDir,
					stateMark,
				)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

// collectScratchpads enumerates scratchpad working directories and
// cross-references sandbox state. Missing base dir is not an error.
func collectScratchpads(homeDir string) ([]ScratchpadInfo, error) {
	baseDir := sandbox.ScratchpadBasePath(homeDir)
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read scratchpad base dir: %w", err)
	}

	sandboxBase := sandbox.SandboxBasePath(homeDir)

	var result []ScratchpadInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		basename := entry.Name()
		if !strings.HasPrefix(basename, sandbox.ScratchpadPrefix) {
			continue
		}
		name := strings.TrimPrefix(basename, sandbox.ScratchpadPrefix)
		workDir := filepath.Join(baseDir, basename)
		sandboxName := sandbox.GenerateSandboxName(workDir)
		sandboxRoot := filepath.Join(sandboxBase, sandboxName)

		hasState := false
		if info, err := os.Stat(sandboxRoot); err == nil && info.IsDir() {
			hasState = true
		}

		size, _ := sandbox.GetSandboxSize(workDir)

		result = append(result, ScratchpadInfo{
			Name:        name,
			WorkingDir:  workDir,
			SandboxName: sandboxName,
			HasState:    hasState,
			SizeBytes:   size,
		})
	}
	return result, nil
}

func newScratchpadRmCmd() *cobra.Command {
	var (
		all       bool
		keepState bool
		force     bool
	)

	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove a scratchpad and its sandbox state",
		Long: `Remove a scratchpad working directory and its matching sandbox state.

By default removes both the working dir under
~/.local/share/devsandbox-scratchpads/ and the sandbox state under
~/.local/share/devsandbox/. Use --keep-state to keep the sandbox state
(home overlay, tool caches) while wiping the working dir.

Refuses to remove scratchpads with an active session.`,
		Example: `  devsandbox scratchpad rm foo
  devsandbox scratchpad rm foo --keep-state
  devsandbox scratchpad rm --all --force`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return fmt.Errorf("--all cannot be combined with a name argument")
			}
			if !all && len(args) != 1 {
				return fmt.Errorf("exactly one name argument is required (or pass --all)")
			}

			homeDir, err := os.UserHomeDir()
			if err != nil {
				return err
			}

			var targets []ScratchpadInfo
			if all {
				targets, err = collectScratchpads(homeDir)
				if err != nil {
					return err
				}
			} else {
				name := args[0]
				if err := sandbox.ValidateScratchpadName(name); err != nil {
					return err
				}
				workDir := sandbox.ScratchpadDir(homeDir, name)
				sandboxName := sandbox.GenerateSandboxName(workDir)
				sandboxRoot := filepath.Join(sandbox.SandboxBasePath(homeDir), sandboxName)
				hasWork := false
				if info, err := os.Stat(workDir); err == nil && info.IsDir() {
					hasWork = true
				}
				hasState := false
				if info, err := os.Stat(sandboxRoot); err == nil && info.IsDir() {
					hasState = true
				}
				if !hasWork && !hasState {
					return fmt.Errorf("scratchpad %q not found", name)
				}
				size, _ := sandbox.GetSandboxSize(workDir)
				targets = []ScratchpadInfo{{
					Name:        name,
					WorkingDir:  workDir,
					SandboxName: sandboxName,
					HasState:    hasState,
					SizeBytes:   size,
				}}
			}

			if len(targets) == 0 {
				fmt.Println("No scratchpads to remove.")
				return nil
			}

			// Print plan and confirm unless --force.
			fmt.Printf("Scratchpads to remove (%d):\n\n", len(targets))
			for _, t := range targets {
				fmt.Printf("  %s\n", t.Name)
				fmt.Printf("    Working dir: %s (%s)\n", t.WorkingDir, sandbox.FormatSize(t.SizeBytes))
				if keepState {
					fmt.Printf("    State:       %s (kept)\n", t.SandboxName)
				} else if t.HasState {
					fmt.Printf("    State:       %s\n", t.SandboxName)
				} else {
					fmt.Printf("    State:       (none)\n")
				}
				fmt.Println()
			}

			if !force {
				fmt.Print("Remove these scratchpads? [y/N] ")
				reader := bufio.NewReader(os.Stdin)
				response, readErr := reader.ReadString('\n')
				if readErr != nil {
					return readErr
				}
				response = strings.TrimSpace(strings.ToLower(response))
				if response != "y" && response != "yes" {
					fmt.Println("Aborted.")
					return nil
				}
			}

			var removed, failed, skipped int
			for _, t := range targets {
				sandboxRoot := filepath.Join(sandbox.SandboxBasePath(homeDir), t.SandboxName)

				// Refuse if the session is active.
				if t.HasState && sandbox.IsSessionActive(sandboxRoot) {
					fmt.Fprintf(os.Stderr, "Skipping %q: active session\n", t.Name)
					skipped++
					continue
				}

				// Remove sandbox state unless --keep-state.
				if !keepState && t.HasState {
					m, loadErr := sandbox.LoadMetadata(sandboxRoot)
					if loadErr != nil {
						// Fall back to plain directory removal if metadata is missing.
						if rmErr := sandbox.RemoveSandbox(sandboxRoot); rmErr != nil {
							fmt.Fprintf(os.Stderr, "Failed to remove sandbox state for %q: %v\n", t.Name, rmErr)
							failed++
							continue
						}
					} else {
						if rmErr := sandbox.RemoveSandboxByType(m, true); rmErr != nil {
							fmt.Fprintf(os.Stderr, "Failed to remove sandbox state for %q: %v\n", t.Name, rmErr)
							failed++
							continue
						}
					}
				}

				// Remove the working directory.
				if err := os.RemoveAll(t.WorkingDir); err != nil {
					fmt.Fprintf(os.Stderr, "Failed to remove working dir for %q: %v\n", t.Name, err)
					failed++
					continue
				}

				removed++
			}

			fmt.Printf("Removed %d scratchpad(s)", removed)
			if skipped > 0 {
				fmt.Printf(", %d skipped", skipped)
			}
			if failed > 0 {
				fmt.Printf(", %d failed", failed)
			}
			fmt.Println()

			if failed > 0 {
				return fmt.Errorf("%d scratchpad(s) failed to remove", failed)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Remove all scratchpads")
	cmd.Flags().BoolVar(&keepState, "keep-state", false, "Keep the sandbox state; only remove the working dir")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation prompt")

	return cmd
}
