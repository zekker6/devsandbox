package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"devsandbox/internal/config"
	"devsandbox/internal/notice"
	"devsandbox/internal/reclaim"
	"devsandbox/internal/sandbox"
	"devsandbox/internal/session"
	"devsandbox/internal/worktree"
)

func newSandboxesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandboxes",
		Short: "Manage sandbox instances",
		Long:  "List, inspect, and prune sandbox instances stored in ~/.local/share/devsandbox/",
	}

	cmd.AddCommand(newListCmd())
	cmd.AddCommand(newPruneCmd())

	return cmd
}

func newListCmd() *cobra.Command {
	var (
		jsonOutput bool
		sortBy     string
		noSize     bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all sandboxes",
		Long:  "List all sandbox instances with their metadata",
		Example: `  devsandbox sandboxes list
  devsandbox sandboxes list --json
  devsandbox sandboxes list --sort used
  devsandbox sandboxes list --no-size`,
		RunE: func(cmd *cobra.Command, args []string) error {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return err
			}

			// The same base prune works from: a listing that reads the default
			// base while prune reads the configured one tells the user a
			// sandbox does not exist and then removes it.
			baseDir, err := configuredSandboxBase(homeDir)
			if err != nil {
				return err
			}
			sandboxes, err := sandbox.ListAllSandboxes(baseDir)
			if err != nil {
				return err
			}

			if len(sandboxes) == 0 {
				fmt.Println("No sandboxes found.")
				return nil
			}

			// Check active status for each sandbox
			for _, s := range sandboxes {
				s.Active = sandbox.IsSessionActive(s.SandboxRoot)
			}

			// Calculate sizes (default: on)
			if !noSize {
				// Query Docker volume sizes once if any Docker sandboxes exist
				var dockerVolumeSizes map[string]int64
				hasDocker := false
				for _, s := range sandboxes {
					if s.Isolation == sandbox.IsolationDocker {
						hasDocker = true
						break
					}
				}
				if hasDocker {
					dockerVolumeSizes = sandbox.GetDockerVolumeSizes()
				}

				for _, s := range sandboxes {
					if s.Isolation == sandbox.IsolationDocker {
						// Try to get size from Docker volume data
						volumes := sandbox.GetContainerVolumes(s.SandboxRoot)
						for _, vol := range volumes {
							if size, ok := dockerVolumeSizes[vol]; ok {
								s.SizeBytes += size
							}
						}
					} else {
						size, err := sandbox.GetSandboxSize(s.SandboxRoot)
						if err != nil {
							notice.Warn("failed to calculate size for %s: %v", s.Name, err)
						}
						s.SizeBytes = size
					}
				}
			}

			// Sort
			sandbox.SortSandboxes(sandboxes, sandbox.SortBy(sortBy))

			if jsonOutput {
				return printJSON(sandboxes)
			}

			return printTable(sandboxes, !noSize)
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	cmd.Flags().StringVar(&sortBy, "sort", "name", "Sort by: name, created, used, size")
	cmd.Flags().BoolVar(&noSize, "no-size", false, "Skip size calculation (faster)")

	return cmd
}

func newPruneCmd() *cobra.Command {
	var (
		all       bool
		keep      int
		olderThan string
		orphaned  bool
		dryRun    bool
		force     bool
		volumes   bool
	)

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove stale sandboxes and reclaim host-owned state",
		Long: `Remove sandbox instances based on various criteria.

Without any flags, only orphaned sandboxes (where the original project
directory no longer exists) are removed. Pass --orphaned to restrict any
other selector (--keep, --older-than, --all) to orphaned sandboxes only.

prune also reports every location devsandbox writes state to on the host -
egress markers, session records, herdr pane records, the wrapper log,
interrupted removals, orphaned shared temp directories - and reclaims the ones
whose owner is gone, plus the shared temp directory of each sandbox that
remains. Those sweeps run even when there is nothing to prune, and are not
behind the confirmation prompt, which gates removing sandboxes only. --keep and
--older-than select sandboxes; they do not change any location's own rule.`,
		Example: `  devsandbox sandboxes prune                       # Remove orphaned only
  devsandbox sandboxes prune --orphaned             # Same, explicit
  devsandbox sandboxes prune --all --volumes        # Remove all sandboxes and volumes
  devsandbox sandboxes prune --keep 5               # Keep 5 most recently used
  devsandbox sandboxes prune --older-than 30d       # Remove unused for 30 days
  devsandbox sandboxes prune --orphaned --older-than 30d  # Orphaned and unused for 30d
  devsandbox sandboxes prune --dry-run              # Report every location, remove nothing`,
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return err
			}

			baseDir, err := configuredSandboxBase(homeDir)
			if err != nil {
				return err
			}

			// Every flag is validated before the first sweep runs. The sweeps
			// below delete state and reach every return past them, so parsing
			// this where it is used would have a mistyped duration reclaim
			// every location and then report the command as failed.
			var duration time.Duration
			if olderThan != "" {
				duration, err = parseDuration(olderThan)
				if err != nil {
					return fmt.Errorf("invalid duration %q: %w", olderThan, err)
				}
			}

			out := cmd.OutOrStdout()
			locations := reclaim.Locations()

			// The worktrees registered under the sandboxes about to be removed,
			// read before the host-scoped sweep below. That sweep reclaims the
			// session records, and a sandbox selected for pruning is inactive by
			// definition - so its records name dead pids and the sweep takes
			// every one of them. Reading the store afterwards finds nothing and
			// the worktrees stay registered in their repository. Use this snapshot
			// only for worktree cleanup: a session name can be reused after the
			// sweep, including while confirmation waits. The host sweep alone
			// owns stale session-record removal and reporting.
			sessionStore, sessErr := session.DefaultStore()
			if sessErr != nil {
				notice.Warn("session store unavailable; worktree cleanup skipped: %v", sessErr)
			}
			var sessionSnapshot []*session.Session
			if sessionStore != nil {
				sessionSnapshot, err = sessionStore.List()
				if err != nil {
					notice.Warn("list sessions; worktree cleanup skipped: %v", err)
				}
			}

			// Host-scoped locations first, and whatever the sandbox selection
			// turns out to be: they have no sandbox to depend on, and on a host
			// with nothing to prune - the common case - a sweep placed after the
			// early returns below would never run.
			hostTarget := reclaim.Target{HomeDir: homeDir, SandboxBase: baseDir}
			reclaimErr := runReclaim(out, hostTarget, locations, dryRun)

			reclaimRemaining := func(skip []*sandbox.Metadata) error {
				roots, err := remainingSandboxRoots(baseDir, skip)
				if err != nil {
					return err
				}
				var errs []error
				for _, root := range roots {
					target := reclaim.Target{
						HomeDir:     homeDir,
						SandboxBase: baseDir,
						SandboxRoot: root,
						SandboxHome: sandbox.SandboxHomePath(root),
					}
					errs = append(errs, runReclaim(out, target, locations, dryRun))
				}
				return errors.Join(errs...)
			}

			// Both sweeps reach every return below, rather than each return
			// remembering to join them: the host-scoped one already ran, and
			// the per-sandbox one is not something the confirmation prompt
			// gates - the prompt asks about removing sandboxes. reclaimSkip is
			// the set this run takes away, empty until that is decided, so an
			// aborted run still reports every sandbox that is left.
			var reclaimSkip []*sandbox.Metadata
			defer func() {
				retErr = errors.Join(reclaimErr, retErr, reclaimRemaining(reclaimSkip))
			}()

			sandboxes, err := sandbox.ListAllSandboxes(baseDir)
			if err != nil {
				return err
			}

			if len(sandboxes) == 0 {
				fmt.Println("No sandboxes found.")
				return nil
			}

			// Check active status for each sandbox
			for _, s := range sandboxes {
				s.Active = sandbox.IsSessionActive(s.SandboxRoot)
			}

			opts := sandbox.PruneOptions{
				All:       all,
				Keep:      keep,
				OlderThan: duration,
				Orphaned:  orphaned,
				DryRun:    dryRun,
			}

			toPrune := sandbox.SelectForPruning(sandboxes, opts)

			if len(toPrune) == 0 {
				fmt.Println("No sandboxes to prune.")
				return nil
			}

			// Calculate sizes for display
			var dockerVolumeSizes map[string]int64
			hasDocker := false
			for _, s := range toPrune {
				if s.Isolation == sandbox.IsolationDocker {
					hasDocker = true
					break
				}
			}
			if hasDocker {
				dockerVolumeSizes = sandbox.GetDockerVolumeSizes()
			}

			var totalSize int64
			for _, s := range toPrune {
				if s.Isolation == sandbox.IsolationDocker {
					vols := sandbox.GetContainerVolumes(s.SandboxRoot)
					for _, vol := range vols {
						if size, ok := dockerVolumeSizes[vol]; ok {
							s.SizeBytes += size
						}
					}
				} else {
					size, err := sandbox.GetSandboxSize(s.SandboxRoot)
					if err != nil {
						notice.Warn("failed to calculate size for %s: %v", s.Name, err)
					}
					s.SizeBytes = size
				}
				totalSize += s.SizeBytes
			}

			// Show what will be removed
			fmt.Printf("Sandboxes to remove (%d):\n\n", len(toPrune))
			for _, s := range toPrune {
				status := ""
				if s.Orphaned {
					status = " [orphaned]"
				}
				if s.Isolation == sandbox.IsolationDocker && s.State != "" {
					status = status + " [" + s.State + "]"
				}
				isoType := string(s.Isolation)
				if isoType == "" {
					isoType = "bwrap"
				}
				fmt.Printf("  %s (%s)%s\n", s.Name, isoType, status)
				fmt.Printf("    Project: %s\n", s.ProjectDir)
				fmt.Printf("    Last used: %s\n", s.LastUsed.Format("2006-01-02 15:04"))
				if s.SizeBytes > 0 {
					fmt.Printf("    Size: %s\n", sandbox.FormatSize(s.SizeBytes))
				}
				fmt.Println()
			}
			if totalSize > 0 {
				fmt.Printf("Total: %s\n\n", sandbox.FormatSize(totalSize))
			}

			if dryRun {
				fmt.Println("Dry run - no sandboxes were removed.")
				reclaimSkip = toPrune
				return nil
			}

			// Confirm unless --force
			if !force {
				fmt.Print("Remove these sandboxes? [y/N] ")
				reader := bufio.NewReader(os.Stdin)
				response, err := reader.ReadString('\n')
				if err != nil {
					return err
				}
				response = strings.TrimSpace(strings.ToLower(response))
				if response != "y" && response != "yes" {
					fmt.Println("Aborted.")
					return nil
				}
			}

			// Remove sandboxes (handles both bwrap and docker)
			reclaimSkip = toPrune
			var removed, failed int
			wtMgr := worktree.NewManager()
			for _, s := range toPrune {
				// Remove any worktrees registered under this sandbox root before
				// wiping its on-disk state. Best-effort: warnings only.
				for _, sess := range session.FilterForSandbox(sessionSnapshot, s.SandboxRoot) {
					if sess.Worktree != nil && sess.Worktree.RepoRoot != "" {
						if err := wtMgr.Remove(cmd.Context(), sess.Worktree.RepoRoot, sess.Worktree.Path); err != nil {
							notice.Warn("worktree cleanup for %s: %v", sess.Name, err)
						}
					}
				}
				if err := sandbox.RemoveSandboxByType(s, volumes); err != nil {
					notice.Error("Failed to remove %s: %v", s.Name, err)
					failed++
				} else {
					removed++
				}
			}

			fmt.Printf("Removed %d sandbox(es)", removed)
			if failed > 0 {
				fmt.Printf(", %d failed", failed)
			}
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Remove all sandboxes")
	cmd.Flags().BoolVar(&volumes, "volumes", false, "Also remove associated Docker volumes")
	cmd.Flags().IntVar(&keep, "keep", 0, "Keep N most recently used sandboxes")
	cmd.Flags().StringVar(&olderThan, "older-than", "", "Remove sandboxes not used in duration (e.g., 30d, 2w)")
	cmd.Flags().BoolVar(&orphaned, "orphaned", false, "Restrict pruning to orphaned sandboxes only")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report what would be removed, and every state location, without removing anything")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation prompt")

	return cmd
}

// configuredSandboxBase returns the base directory sandboxes live under,
// honoring sandbox.base_path the way a launch does (main.go's
// sandbox.NewConfig). A prune that assumed the default base never saw the
// sandboxes of a user who set the key, and would name every one of them an
// orphan to the shared-temp sweep.
//
// `sandboxes list`, `sandboxes prune`, `scratchpad list` and `scratchpad rm`
// resolve their base here rather than through sandbox.SandboxBasePath. The two
// scratchpad commands need it because the default base reported every
// scratchpad as stateless, so both left its sandbox tree behind and skipped the
// active-session guard that state gates. The other host-level commands -
// `doctor`, `logs`, `overlay`, and the proxy and filter helpers - still read
// the default base and so see nothing on a host that set the key. That is a
// pre-existing gap this function does not close; do not read the list above as
// covering them.
//
// A config that cannot be read fails the command rather than degrading to the
// default base, for the same reason: the "orphaned shared temp" sweep finds an
// orphan by elimination against the sandboxes under this base, so a base that
// is merely plausible has it delete the shared temp of every sandbox that
// really exists - and prune would print "No sandboxes found." while doing it.
//
// The project-local .devsandbox.toml is skipped. The base is a host-level
// setting and prune is a host-level command: reading the working directory's
// config would let one project's override decide which sandboxes the whole
// host is judged against, and would put a trust prompt in front of a prune run
// from a project whose config is untrusted. An `[[include]]` cannot set the
// key either (config.applyIncludes pins it), which is what makes the answer
// here independent of the directory prune is run from - an include is selected
// by the working directory, and this command is not run from the project's.
func configuredSandboxBase(homeDir string) (string, error) {
	appCfg, _, _, err := config.LoadConfigWithOptions(&config.LoadOptions{SkipLocalConfig: true})
	if err != nil {
		return "", fmt.Errorf("failed to load config: %w", err)
	}
	if appCfg.Sandbox.BasePath != "" {
		return appCfg.Sandbox.BasePath, nil
	}
	return sandbox.SandboxBasePath(homeDir), nil
}

// runReclaim sweeps every location in locs that target can address and reports
// what each one holds afterwards.
//
// The target decides which half of the catalogue runs: one naming a sandbox
// selects the per-sandbox locations, one naming none selects the host-scoped
// ones. The caller passes the whole catalogue either way, so a location can
// never be paired with a target that cannot address it.
//
// A location that fails is named in the report and in the returned error, and
// the remaining ones still run: each location is independent, and a prune that
// stopped at the first failure would leave the rest of the host unreclaimed. It
// is still reported with what it reclaimed and what it holds, because a sweep
// that fails has usually failed on one entry out of many.
func runReclaim(w io.Writer, target reclaim.Target, locs []reclaim.Location, dryRun bool) error {
	perSandbox := target.SandboxRoot != ""
	header := "Host-owned state:"
	if perSandbox {
		header = fmt.Sprintf("Sandbox state (%s):", filepath.Base(target.SandboxRoot))
	}

	var (
		printed bool
		errs    []error
	)
	for _, loc := range locs {
		if loc.PerSandbox != perSandbox {
			continue
		}
		if !printed {
			_, _ = fmt.Fprintf(w, "%s\n", header)
			printed = true
		}

		// Every sweep in the catalogue reports the entries it removed
		// alongside a joined error, so a failure is nearly always partial: the
		// count and the location's remaining size are what the report exists
		// to give, and dropping them for one stuck entry says the sweep took
		// nothing when it took all but one.
		reclaimed := 0
		var sweepErr error
		if !dryRun {
			n, err := loc.Run(target)
			reclaimed = n
			if err != nil {
				sweepErr = err
				errs = append(errs, fmt.Errorf("reclaim %s: %w", loc.Name, err))
			}
		}

		path := loc.Path(target)
		entries, size, err := reclaim.Usage(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("reclaim %s: %w", loc.Name, err))
			_, _ = fmt.Fprintf(w, "  %s: %v\n", loc.Name, err)
			continue
		}

		detail := fmt.Sprintf("%d %s, %s", entries, pluralEntries(entries), sandbox.FormatSize(size))
		if reclaimed > 0 {
			detail += fmt.Sprintf(", reclaimed %d", reclaimed)
		}
		if sweepErr != nil {
			detail += fmt.Sprintf(", not fully reclaimed: %v", sweepErr)
		}
		_, _ = fmt.Fprintf(w, "  %s: %s (%s)\n", loc.Name, detail, path)
	}
	if printed {
		_, _ = fmt.Fprintln(w)
	}

	return errors.Join(errs...)
}

func pluralEntries(n int) string {
	if n == 1 {
		return "entry"
	}
	return "entries"
}

// remainingSandboxRoots lists the state roots of the sandboxes left under
// baseDir, skipping the ones this run selected for removal - a removal that
// failed leaves its sandbox on disk, and reporting a sandbox prune just failed
// to remove says nothing useful.
//
// The listing comes from disk rather than ListAllSandboxes: that one carries a
// container name in SandboxRoot for Docker sandboxes, which is not a path, so
// every per-sandbox location built from it would name a directory that does not
// exist.
func remainingSandboxRoots(baseDir string, skip []*sandbox.Metadata) ([]string, error) {
	onDisk, err := sandbox.ListSandboxes(baseDir)
	if err != nil {
		return nil, err
	}
	skipped := make(map[string]bool, len(skip))
	for _, s := range skip {
		skipped[s.SandboxRoot] = true
	}
	roots := make([]string, 0, len(onDisk))
	for _, s := range onDisk {
		if skipped[s.SandboxRoot] {
			continue
		}
		roots = append(roots, s.SandboxRoot)
	}
	return roots, nil
}

// formatSandboxStatus renders the status column: the sandbox's lifecycle state
// plus anything notable about how its last session ended. An OOM kill goes here
// because it is the one outcome the sandbox cannot report itself — the process is
// gone and its session file with it, so the listing is where the user finds out.
func formatSandboxStatus(s *sandbox.Metadata) string {
	var parts []string
	if s.Orphaned {
		parts = append(parts, "orphaned")
	}
	if s.Active {
		parts = append(parts, "active")
	}
	// For Docker containers, show the container state
	if s.Isolation == sandbox.IsolationDocker && s.State != "" {
		parts = append(parts, s.State)
	}
	if oom := s.LastOOM.Status(); oom != "" {
		parts = append(parts, oom)
	}
	return strings.Join(parts, ", ")
}

func printJSON(sandboxes []*sandbox.Metadata) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(sandboxes)
}

func printTable(sandboxes []*sandbox.Metadata, showSize bool) error {
	table := tablewriter.NewWriter(os.Stdout)

	if showSize {
		table.Header("NAME", "TYPE", "PROJECT DIR", "CREATED", "LAST USED", "SIZE", "STATUS")
	} else {
		table.Header("NAME", "TYPE", "PROJECT DIR", "CREATED", "LAST USED", "STATUS")
	}

	for _, s := range sandboxes {
		status := formatSandboxStatus(s)

		projectDir := s.ProjectDir
		if len(projectDir) > 40 {
			projectDir = "..." + projectDir[len(projectDir)-37:]
		}

		isoType := string(s.Isolation)
		if isoType == "" {
			isoType = "bwrap"
		}

		sizeStr := sandbox.FormatSize(s.SizeBytes)
		if s.Isolation == sandbox.IsolationDocker && s.SizeBytes == 0 {
			sizeStr = "-"
		}

		if showSize {
			_ = table.Append(
				s.Name,
				isoType,
				projectDir,
				s.CreatedAt.Format("2006-01-02"),
				s.LastUsed.Format("2006-01-02"),
				sizeStr,
				status,
			)
		} else {
			_ = table.Append(
				s.Name,
				isoType,
				projectDir,
				s.CreatedAt.Format("2006-01-02"),
				s.LastUsed.Format("2006-01-02"),
				status,
			)
		}
	}

	return table.Render()
}

// parseDuration parses a human-friendly duration like "30d", "2w", "1h"
func parseDuration(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("duration too short")
	}

	// Try standard Go duration first
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}

	// Parse custom formats (days, weeks)
	unit := s[len(s)-1]
	valueStr := s[:len(s)-1]

	var value int
	if _, err := fmt.Sscanf(valueStr, "%d", &value); err != nil {
		return 0, fmt.Errorf("invalid number: %s", valueStr)
	}

	switch unit {
	case 'd':
		return time.Duration(value) * 24 * time.Hour, nil
	case 'w':
		return time.Duration(value) * 7 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unknown unit: %c (use h, d, or w)", unit)
	}
}
