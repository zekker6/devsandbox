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
// internal/herdrstate, internal/logrotate, internal/proxy). The owners must
// never import internal/reclaim: every Sweep is a plain function with plain
// arguments, so no owner needs the Location type, and that is what keeps the
// catalogue free of cycles. Nothing outside cmd/ should import this package.
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
// Every location here records the owner of each entry, a pid in the name or a
// held lock, or is reclaimed by age where no owner can be identified after a
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
	// 10. Proxy request logs: bounded by their writer's size and file-count
	// rotation (internal/proxy.RotatingFileWriter); reported only.
	{
		Name:       "proxy request logs",
		PerSandbox: true,
		Path: func(t Target) string {
			return filepath.Join(t.SandboxRoot, proxy.LogBaseDirName, proxy.ProxyLogDirName)
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
// path naming a single file reports one entry. The byte count is delegated to
// sandbox.GetSandboxSize rather than walking the tree a second time.
func Usage(path string) (entries int, size int64, err error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("stat %s: %w", path, err)
	}
	entries = 1
	if info.IsDir() {
		list, err := os.ReadDir(path)
		if err != nil {
			return 0, 0, fmt.Errorf("read %s: %w", path, err)
		}
		entries = len(list)
	}
	size, err = sandbox.GetSandboxSize(path)
	if err != nil {
		return 0, 0, fmt.Errorf("size %s: %w", path, err)
	}
	return entries, size, nil
}
