// Package reclaim catalogues every place devsandbox writes state on the host
// for its own bookkeeping, each with the sweep that reclaims it.
//
// The catalogue is a list of places, not a list of moments. Components keep
// their sweep logic where it is and keep calling it from the launch path, in
// the order the launch needs; a Location only points at that logic so one
// command (`sandboxes prune`) can sweep every location, one report shape can
// say what a sweep did, and a reviewer can check a new state directory against
// the list. Launch-time call sites do not move here.
//
// # Import direction
//
// This package imports the owning packages (internal/sandbox,
// internal/sandbox/tools, internal/session, internal/egress,
// internal/herdrstate, internal/logrotate, internal/notice, internal/proxy).
// The owners must never import internal/reclaim: every Sweep is a plain
// function with plain arguments, so no owner needs the Location type, and that
// is what keeps the catalogue free of cycles. Nothing outside cmd/ imports
// this package, which TestNoInternalPackageImportsReclaim pins.
// If an owner ever needs Location, split the catalogue into
// internal/reclaim/catalog at that point rather than reaching in.
//
// # Reported-only locations
//
// A nil Sweep marks a location that is reported and never swept from prune:
// one bounded by its own writer, or one the launch already sweeps under
// guarantees prune cannot establish (primary designation, run-dir
// registration, sole occupancy). Such a location still belongs in the list so
// the list is complete, and Run answers (0, nil) for it.
//
// # State the host trusts
//
// Every location here records the owner of each entry - a pid in the name, a
// held lock, or a reference to state elsewhere whose absence proves the entry
// abandoned - or is reclaimed by age where no owner can be identified after a
// hard kill. The pid probe (internal/procstate) answers an uncertain result as
// alive, so a location keyed on the pid alone needs an age backstop or an
// accepted leak, named at its registration.
package reclaim

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"devsandbox/internal/egress"
	"devsandbox/internal/herdrstate"
	"devsandbox/internal/logrotate"
	"devsandbox/internal/notice"
	"devsandbox/internal/proxy"
	"devsandbox/internal/sandbox"
	"devsandbox/internal/sandbox/tools"
	"devsandbox/internal/session"
)

// Target names the roots a sweep runs against.
//
// SandboxBase is the configured sandbox base (sandbox.base_path or the
// default), never derived from HomeDir inside a sweep. SandboxRoot and
// SandboxHome are empty for host-scoped locations. They are distinct
// directories - SandboxHome is <SandboxRoot>/home - and deriving one from the
// other in a sweep would drift from internal/sandbox.NewConfig.
type Target struct {
	HomeDir     string
	SandboxBase string
	SandboxRoot string
	SandboxHome string
}

// Location is one place devsandbox writes state on the host.
type Location struct {
	// Name identifies the location in reports, e.g. "egress markers".
	Name string
	// PerSandbox marks a location that exists once per sandbox; its Sweep runs
	// once per sandbox with SandboxRoot and SandboxHome set.
	PerSandbox bool
	// Path names the directory for reporting and --dry-run.
	Path func(Target) string
	// Sweep reclaims the location and reports the entries it removed. It is
	// nil for a location that is reported only.
	Sweep func(Target) (int, error)
}

// Run sweeps the location against target and reports the entries removed.
//
// It refuses an incomplete target before anything acts on a path built from
// "": a per-sandbox location needs SandboxRoot and SandboxHome, a host-scoped
// one needs HomeDir. That check lives here so no adapter has to repeat it.
// SandboxBase is checked by the two locations that need it rather than
// demanded of every host location. A nil Sweep answers (0, nil).
func (l Location) Run(target Target) (int, error) {
	if l.PerSandbox {
		if target.SandboxRoot == "" || target.SandboxHome == "" {
			return 0, fmt.Errorf("reclaim %q: per-sandbox sweep needs SandboxRoot and SandboxHome", l.Name)
		}
	} else if target.HomeDir == "" {
		return 0, fmt.Errorf("reclaim %q: host sweep needs HomeDir", l.Name)
	}
	if l.Sweep == nil {
		return 0, nil
	}
	return l.Sweep(target)
}

// catalogue lists every location in a fixed order. Comments give the number
// each carries in the plan's discovery table so a reviewer can tie a row to
// its entry; host-scoped locations come first, then per-sandbox ones.
var catalogue = []Location{
	// 1. Egress markers: one directory per proxy-mode bwrap launch, named by
	// pid, holding the file the lockdown prologue creates right before it execs
	// the workload. The launch removes its own at exit; a kill leaves it. A
	// marker whose pid is dead goes on sight, a pre-pid lockdown-* name by age.
	// A pid answering EPERM is kept with no backstop: sweeping a live session's
	// marker makes its own exit 78 read as an aborted lockdown, and the leak is
	// one empty directory.
	{
		Name:  "egress markers",
		Path:  func(t Target) string { return egress.MarkerRoot(t.HomeDir) },
		Sweep: func(t Target) (int, error) { return egress.SweepMarkers(egress.MarkerRoot(t.HomeDir)) },
	},
	// 2. Session records: one JSON file per proxy-mode session, carrying the
	// owning pid. A record goes once its pid is dead, or once nothing has
	// written it for 30 days - the backstop for a pid the probe cannot confirm
	// dead, because while the record survives it holds its session name
	// against every later launch.
	{
		Name: "session records",
		Path: func(t Target) string { return session.DefaultDir(t.HomeDir) },
		Sweep: func(t Target) (int, error) {
			return session.NewStore(session.DefaultDir(t.HomeDir)).CleanStaleErr()
		},
	},
	// 3. Herdr pane records: one JSON file per herdr pane an agent was launched
	// from, naming the sandbox root that launch used, which the resume guard in
	// run-agent compares against. Nothing removed them before. A record goes
	// once the root it names no longer exists, and on no other signal: there
	// is no pid to probe, since the pane outlives the launch by design, and no
	// age backstop, because panes routinely stay open past any bound and
	// deleting a live pane's record silently disables that guard, all to save
	// a few hundred bytes. The launch creates the root before it writes the
	// record, so a record never names a root that does not exist yet.
	{
		Name:  "herdr pane records",
		Path:  func(t Target) string { return herdrstate.DefaultDir(t.HomeDir) },
		Sweep: func(t Target) (int, error) { return herdrstate.Prune(herdrstate.DefaultDir(t.HomeDir)) },
	},
	// 4. Wrapper log: one append-only file carrying every wrapper diagnostic
	// from every devsandbox invocation on this host. Nothing removed anything
	// from it before; it is bounded now by a rotation that runs before each
	// invocation's append handle opens, and the sweep is that same rotation,
	// so what it reclaims is the backups past the file limit. It has no owner
	// to record because it has no per-entry owner: it is one file, shared by
	// every launch and outliving all of them.
	{
		Name: "wrapper log",
		Path: func(t Target) string { return notice.DefaultLogPath(t.HomeDir) },
		Sweep: func(t Target) (int, error) {
			return logrotate.Rotate(notice.DefaultLogPath(t.HomeDir), logrotate.Options{})
		},
	},
	// 5. Interrupted removals: sandbox trees a --rm teardown renamed aside
	// under <SandboxBase>/.removing, named by the teardown's pid, that a kill
	// between the rename and the delete stranded. A tree goes once its pid is
	// dead, or once it has been staged for 30 days - the backstop for a pid
	// the probe cannot confirm dead, justified because a staged tree is an
	// entire sandbox, the largest leak in this list. Needs SandboxBase, which
	// the owner refuses empty rather than resolving against the working
	// directory.
	{
		Name:  "interrupted removals",
		Path:  func(t Target) string { return sandbox.StagingDir(t.SandboxBase) },
		Sweep: func(t Target) (int, error) { return sandbox.RemoveAbandonedStaging(t.SandboxBase) },
	},
	// 6. Orphaned shared temp: one directory per sandbox home under
	// ~/.cache/devsandbox/tmp, named by a one-way hash of the home, that $TMPDIR
	// points at inside the sandbox - the largest leak in this list. Removing a
	// sandbox now removes its directory (RemoveSandboxRoot, RemoveSandboxIfIdle);
	// this sweep is the backstop for removals that were interrupted and for
	// orphans with recorded ownership. The current base listing backfills owner
	// records; records also cover previous bases. Unknown hashes are kept, not
	// inferred orphaned from a partial listing. Removal requires a missing
	// owner root, an accessible owner base and nothing changed for 7 days.
	// The legacy revdiff-ipc root follows the same rule.
	{
		Name: "orphaned shared temp",
		Path: func(t Target) string { return tools.SharedTmpRoot(t.HomeDir) },
		Sweep: func(t Target) (int, error) {
			live, err := liveSandboxHomes(t.SandboxBase)
			if err != nil {
				// A base that is not on disk fails the sweep rather than
				// running it against an empty live set, but only once there is
				// something the sweep could have removed: on a host that has
				// never launched devsandbox both the base and the shared temp
				// root are absent, and telling that user their prune failed
				// reports a missing directory as a fault.
				if errors.Is(err, errSandboxBaseAbsent) && isEmptyDir(tools.SharedTmpRoot(t.HomeDir)) {
					return 0, nil
				}
				return 0, err
			}
			return tools.SweepOrphanSharedTmp(t.HomeDir, live)
		},
	},
	// Ownership survives base-path changes and is removed only when the named
	// sandbox and both its temporary directories are gone. No age backstop:
	// an old owner record still protects an idle sandbox.
	{
		Name:  "shared temp owners",
		Path:  func(t Target) string { return tools.SharedTmpOwnerRoot(t.HomeDir) },
		Sweep: func(t Target) (int, error) { return tools.SweepSharedTmpOwners(t.HomeDir) },
	},
	// 7. Run directories: one socket directory per running devsandbox process,
	// named by pid. Swept on every launch by cleanupStaleRunDirs before any
	// tool creates a socket, so a prune sweep would reclaim nothing a launch
	// does not; reported only.
	{
		Name:       "run directories",
		PerSandbox: true,
		Path:       func(t Target) string { return tools.RunDirRoot(t.SandboxHome) },
	},
	// 8. Session overlay dirs: per-session upper and work dirs for concurrent
	// launches. Swept by the primary launch once it is the sole occupant.
	// Sweeping from prune would need the exclusive lock held across a
	// multi-gigabyte delete, which can exhaust a concurrent launch's retry
	// budget for zero bytes gained; reported only.
	{
		Name:       "session overlay dirs",
		PerSandbox: true,
		Path:       func(t Target) string { return sandbox.SessionOverlayDir(t.SandboxHome) },
	},
	// 9. Live shared temp: the contents of one sandbox's shared temp directory.
	// The launch empties it on a cold start and prunes it by age when a sibling
	// session is live; from prune only the age branch runs, because a prune
	// process is not registered in the sandbox's run directory and so cannot
	// tell "no siblings" from "siblings that have not registered yet". Worth
	// sweeping all the same: the launch-time cleanup is gated on a tool that
	// needs the directory being enabled, so a sandbox whose tool was disabled
	// afterwards keeps its directory until something else reclaims it. Lives
	// under HomeDir, not SandboxRoot, keyed on the sandbox by hash.
	{
		Name:       "live shared temp",
		PerSandbox: true,
		Path:       func(t Target) string { return tools.SharedTmpPath(t.HomeDir, t.SandboxHome) },
		Sweep:      func(t Target) (int, error) { return tools.PruneSharedTmpStale(t.HomeDir, t.SandboxHome) },
	},
	// 10. Proxy request logs: bounded by their writer's size and file-count
	// rotation (internal/proxy.RotatingFileWriter); reported only.
	{
		Name:       "proxy request logs",
		PerSandbox: true,
		Path: func(t Target) string {
			return filepath.Join(t.SandboxRoot, proxy.LogBaseDirName, proxy.ProxyLogDirName)
		},
	},
	// 11. Internal error logs: sandbox.log, tools-errors.log, <engine>.log and
	// logging-errors.log, all opened O_APPEND by the host once per launch of
	// this sandbox. Each is rotated by internal/logging when it opens, which
	// bounds every one of them at the moment they are next written; reported
	// only, because a sweep here would rotate a log no process holds open and
	// reclaim nothing the next launch does not. Under SandboxRoot: SandboxHome
	// is bound read-write into the sandbox, so a host-written log there is on a
	// path sandboxed code can replace.
	{
		Name:       "internal error logs",
		PerSandbox: true,
		Path: func(t Target) string {
			return filepath.Join(t.SandboxRoot, proxy.LogBaseDirName, proxy.InternalLogDirName)
		},
	},
}

// Locations returns every catalogued location in a stable order. The slice is
// a copy, so a caller cannot alter the catalogue.
func Locations() []Location {
	return slices.Clone(catalogue)
}

// Usage reports how many direct entries a location holds and how many bytes
// they occupy. A path that does not exist reports (0, 0, nil): that is the
// normal case for a location on a host that has never used the feature. A
// path naming a single file reports one entry, plus one for each rotated
// backup beside it. The byte count is delegated to sandbox.GetSandboxSize
// rather than walking the tree a second time.
func Usage(path string) (entries int, size int64, err error) {
	info, err := os.Stat(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, 0, fmt.Errorf("stat %s: %w", path, err)
	}
	if err == nil && info.IsDir() {
		list, err := os.ReadDir(path)
		if err != nil {
			return 0, 0, fmt.Errorf("read %s: %w", path, err)
		}
		size, err := sandbox.GetSandboxSize(path)
		if err != nil {
			return 0, 0, fmt.Errorf("size %s: %w", path, err)
		}
		return len(list), size, nil
	}

	// A file location may be a rotated log, whose backups sit beside it as
	// path.1 ... path.N and hold the same location's bytes. They are counted
	// with the live file, because the sweep for such a location is the
	// rotation itself: it renames the live file away and creates nothing, so a
	// report that stats only path says the location holds nothing in the one
	// moment it has just reclaimed the most - and under-reports it by a whole
	// backup set at every other moment. A directory cannot have them: its
	// entries are already counted above.
	if err == nil {
		entries, size = 1, info.Size()
	}
	for _, backup := range logrotate.BackupPaths(path) {
		bi, err := os.Stat(backup)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return 0, 0, fmt.Errorf("stat %s: %w", backup, err)
		}
		entries++
		size += bi.Size()
	}
	return entries, size, nil
}

// errSandboxBaseAbsent reports a base directory that is not there to be
// listed, which is not the same answer as a base holding no sandboxes.
var errSandboxBaseAbsent = errors.New("sandbox base does not exist")

// liveSandboxHomes returns the sandbox home of every sandbox on disk under
// base, spelled exactly as the launch spells it, so the orphan sweep hashes
// the same string the launch hashed. It reads the disk listing rather than
// ListAllSandboxes, whose Docker entries carry a container name in
// SandboxRoot. An empty base is refused rather than reading the working
// directory. Ownership records protect homes under other bases.
//
// A base that is not on disk is refused for the same reason, and it has to be
// checked here because ListSandboxes reads it as "no sandboxes yet" and
// answers (nil, nil). That answer is indistinguishable from a base sitting on
// a volume that is not mounted right now - and the shared temp directories
// live under the home, never under the base, so they are all present while
// every sandbox that owns one is invisible. Eliminating against that empty
// listing names every one of them an orphan.
func liveSandboxHomes(base string) ([]string, error) {
	if base == "" {
		return nil, errors.New("reclaim \"orphaned shared temp\": sandbox base is empty")
	}
	if _, err := os.Stat(base); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("reclaim %q: %w: %s", "orphaned shared temp", errSandboxBaseAbsent, base)
		}
		return nil, fmt.Errorf("reclaim \"orphaned shared temp\": stat sandbox base: %w", err)
	}
	sandboxes, err := sandbox.ListSandboxes(base)
	if err != nil {
		return nil, fmt.Errorf("reclaim \"orphaned shared temp\": list sandboxes: %w", err)
	}
	homes := make([]string, 0, len(sandboxes))
	for _, m := range sandboxes {
		homes = append(homes, sandbox.SandboxHomePath(m.SandboxRoot))
	}
	return homes, nil
}

// isEmptyDir reports whether path holds no entries. A path that cannot be read
// - including one that exists but is unreadable - answers false, so an unknown
// directory never stands in for an empty one when the answer decides whether a
// refused sweep is a fault worth reporting.
func isEmptyDir(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return errors.Is(err, fs.ErrNotExist)
	}
	return len(entries) == 0
}
