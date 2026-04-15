package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"devsandbox/internal/overlay"
	"devsandbox/internal/sandbox"
	"devsandbox/internal/sandbox/tools"
)

type overlayMigrateFlags struct {
	sandbox      string
	allSandboxes bool
	path         string
	tool         string
	primaryOnly  bool
	apply        bool
	setMode      string
	force        bool
	yes          bool
}

func newOverlayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "overlay",
		Short: "Manage sandbox overlay upper directories",
	}
	cmd.AddCommand(newOverlayMigrateCmd())
	return cmd
}

func newOverlayMigrateCmd() *cobra.Command {
	f := &overlayMigrateFlags{}
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Promote overlay upper contents to the host path",
		Long: `Promote accumulated overlay writes from a sandbox's upper directory
back to the real host path. Defaults to a dry-run; pass --apply to write.

Operates on one sandbox (--sandbox NAME) or all sandboxes (--all-sandboxes).
Select what to migrate by host path (--path) or by tool name (--tool).`,
		Example: `  devsandbox overlay migrate --sandbox wip-graph --path ~/.claude/projects
  devsandbox overlay migrate --all-sandboxes --tool claude --apply
  devsandbox overlay migrate --sandbox foo --path ~/.claude/projects --apply --set-mode readwrite`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOverlayMigrate(cmd, f)
		},
	}
	cmd.Flags().StringVar(&f.sandbox, "sandbox", "", "Single sandbox name")
	cmd.Flags().BoolVar(&f.allSandboxes, "all-sandboxes", false, "Operate on all sandboxes")
	cmd.Flags().StringVar(&f.path, "path", "", "Host path to migrate (e.g. ~/.claude/projects)")
	cmd.Flags().StringVar(&f.tool, "tool", "", "Tool name; expands to that tool's overlay bindings")
	cmd.Flags().BoolVar(&f.primaryOnly, "primary-only", false, "Ignore session uppers")
	cmd.Flags().BoolVar(&f.apply, "apply", false, "Actually perform the migration (default is dry-run)")
	cmd.Flags().StringVar(&f.setMode, "set-mode", "", "After migrate, set tool mount_mode in .devsandbox.toml")
	cmd.Flags().BoolVar(&f.force, "force", false, "Bypass running-sandbox safety check")
	cmd.Flags().BoolVar(&f.yes, "yes", false, "Skip --set-mode multi-sandbox confirmation prompt")
	return cmd
}

func runOverlayMigrate(cmd *cobra.Command, f *overlayMigrateFlags) error {
	// Scope flag validation.
	if f.sandbox != "" && f.allSandboxes {
		return fmt.Errorf("--sandbox and --all-sandboxes are mutually exclusive")
	}
	if f.sandbox == "" && !f.allSandboxes {
		return fmt.Errorf("one of --sandbox or --all-sandboxes is required")
	}
	// Selection flag validation.
	if f.path != "" && f.tool != "" {
		return fmt.Errorf("--path and --tool are mutually exclusive")
	}
	if f.path == "" && f.tool == "" {
		return fmt.Errorf("one of --path or --tool is required")
	}
	// --set-mode targets a specific tool's config section, so --tool is required
	// to know which [tools.<name>] section to edit.
	if f.setMode != "" && f.tool == "" {
		return fmt.Errorf("--set-mode requires --tool")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	// Resolve target sandboxes.
	sandboxNames, err := resolveSandboxes(homeDir, f)
	if err != nil {
		return err
	}
	if len(sandboxNames) == 0 {
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), "No sandboxes to migrate."); err != nil {
			return err
		}
		return nil
	}

	// Resolve target host paths.
	hostPaths, err := resolveHostPaths(homeDir, f)
	if err != nil {
		return err
	}

	// Liveness check.
	if !f.force {
		live := checkRunningSandboxes(homeDir, sandboxNames)
		if len(live) > 0 {
			return fmt.Errorf("sandboxes are running: %v (use --force to override)", live)
		}
	}

	// Build aggregated plan.
	plan := overlay.Plan{
		BySandbox: map[string][]overlay.Operation{},
	}
	for _, name := range sandboxNames {
		sandboxHome := filepath.Join(sandbox.SandboxBasePath(homeDir), name, "home")
		for _, hp := range hostPaths {
			srcs, err := overlay.LocateUppers(sandboxHome, hp, f.primaryOnly)
			if err != nil {
				return fmt.Errorf("locate uppers (sandbox=%s path=%s): %w", name, hp, err)
			}
			if len(srcs) == 0 {
				if _, err := fmt.Fprintf(cmd.OutOrStderr(), "sandbox=%s: no overlay upper for %s, skipping\n", name, hp); err != nil {
					return err
				}
				continue
			}
			// Stamp SandboxID + label.
			for i := range srcs {
				srcs[i].SandboxID = name
				if srcs[i].Kind == overlay.UpperPrimary {
					srcs[i].SourceLabel = fmt.Sprintf("sandbox=%s:primary", name)
				} else {
					srcs[i].SourceLabel = fmt.Sprintf("sandbox=%s:session/%s", name, srcs[i].SessionID)
				}
			}
			p, err := overlay.BuildPlan(srcs, hp)
			if err != nil {
				return fmt.Errorf("build plan (sandbox=%s path=%s): %w", name, hp, err)
			}
			plan.Operations = append(plan.Operations, p.Operations...)
			plan.BySandbox[name] = append(plan.BySandbox[name], p.Operations...)
			plan.HostPath = hp
		}
	}

	// Print preview.
	if err := overlay.FormatPreview(cmd.OutOrStdout(), plan, false); err != nil {
		return err
	}
	if !f.apply {
		return nil
	}

	if err := overlay.Apply(plan); err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Apply succeeded."); err != nil {
		return err
	}

	if f.setMode != "" {
		if err := applySetMode(cmd, homeDir, sandboxNames, f.tool, f.setMode, f.yes); err != nil {
			return err
		}
	}
	return nil
}

// applySetMode finds the .devsandbox.toml for each sandbox (from its metadata
// ProjectDir) and sets the tool's mount_mode. Prompts for confirmation when
// multiple config files would change and --yes was not passed.
func applySetMode(cmd *cobra.Command, homeDir string, sandboxNames []string, toolName, mode string, yes bool) error {
	var configs []string
	for _, name := range sandboxNames {
		root := filepath.Join(sandbox.SandboxBasePath(homeDir), name)
		meta, err := sandbox.LoadMetadata(root)
		if err != nil {
			if _, werr := fmt.Fprintf(cmd.OutOrStderr(), "sandbox=%s: cannot load metadata (%v), skipping --set-mode\n", name, err); werr != nil {
				return werr
			}
			continue
		}
		if meta.ProjectDir == "" {
			continue
		}
		configs = append(configs, filepath.Join(meta.ProjectDir, ".devsandbox.toml"))
	}

	if len(configs) == 0 {
		return nil
	}

	if len(configs) > 1 && !yes {
		if _, err := fmt.Fprintf(cmd.OutOrStderr(), "Will update %d .devsandbox.toml files:\n", len(configs)); err != nil {
			return err
		}
		for _, c := range configs {
			if _, err := fmt.Fprintf(cmd.OutOrStderr(), "  %s\n", c); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprint(cmd.OutOrStderr(), "Continue? [y/N] "); err != nil {
			return err
		}
		reader := bufio.NewReader(os.Stdin)
		resp, readErr := reader.ReadString('\n')
		if readErr != nil {
			return readErr
		}
		resp = strings.ToLower(strings.TrimSpace(resp))
		if resp != "y" && resp != "yes" {
			return fmt.Errorf("aborted")
		}
	}

	var errs []string
	for _, c := range configs {
		if err := overlay.SetToolMode(c, toolName, mode); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", c, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("--set-mode errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

// resolveSandboxes returns the list of sandbox names to operate on.
// For --all-sandboxes, enumerates every directory under the sandbox base.
func resolveSandboxes(homeDir string, f *overlayMigrateFlags) ([]string, error) {
	if !f.allSandboxes {
		return []string{f.sandbox}, nil
	}
	base := sandbox.SandboxBasePath(homeDir)
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list sandboxes: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// resolveHostPaths returns the host paths to migrate.
// For --tool, looks up the tool's bindings and filters to ones that resolve
// to MountOverlay under the split policy (CategoryCache/Data/State).
// MountBind and MountTmpOverlay bindings are silently skipped — nothing to
// migrate for them.
func resolveHostPaths(homeDir string, f *overlayMigrateFlags) ([]string, error) {
	if f.path != "" {
		return []string{expandHome(homeDir, f.path)}, nil
	}
	tool := tools.Get(f.tool)
	if tool == nil {
		return nil, fmt.Errorf("unknown tool %q", f.tool)
	}
	// sandboxHome is only used by tool.Bindings to place sandbox-side dest
	// paths — we care about host-side Source paths, so an empty string is fine.
	bindings := tool.Bindings(homeDir, "")
	var paths []string
	for _, b := range bindings {
		if !isMigratableBinding(b) {
			continue
		}
		paths = append(paths, b.Source)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("tool %q has no migratable overlay bindings", f.tool)
	}
	return paths, nil
}

// isMigratableBinding returns true when a binding resolves to an overlay
// with a persistent upper dir under the split policy.
func isMigratableBinding(b tools.Binding) bool {
	// Explicit bind/tmpoverlay types are skipped (tmpoverlay silently;
	// plain bind needs no migration).
	if b.Type == tools.MountBind || b.Type == tools.MountTmpOverlay {
		return false
	}
	switch b.Category {
	case tools.CategoryCache, tools.CategoryData, tools.CategoryState:
		return true
	}
	return false
}

func expandHome(homeDir, p string) string {
	if p == "~" {
		return homeDir
	}
	if len(p) >= 2 && p[:2] == "~/" {
		return filepath.Join(homeDir, p[2:])
	}
	return p
}

func checkRunningSandboxes(homeDir string, names []string) []string {
	var live []string
	for _, name := range names {
		root := filepath.Join(sandbox.SandboxBasePath(homeDir), name)
		if sandbox.IsSessionActive(root) {
			live = append(live, name)
		}
	}
	return live
}
