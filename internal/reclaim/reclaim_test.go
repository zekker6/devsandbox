package reclaim

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"devsandbox/internal/herdrstate"
	"devsandbox/internal/sandbox"
	"devsandbox/internal/sandbox/tools"
	"devsandbox/internal/session"
)

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// fullTarget returns a target with every root set and each root distinct, so
// a path built from the wrong field lands outside the expected tree.
func fullTarget() Target {
	return Target{
		HomeDir:     filepath.Join(string(filepath.Separator), "home", "user"),
		SandboxBase: filepath.Join(string(filepath.Separator), "srv", "sandboxes"),
		SandboxRoot: filepath.Join(string(filepath.Separator), "srv", "sandboxes", "proj-0a1b2c3d"),
		SandboxHome: filepath.Join(string(filepath.Separator), "srv", "sandboxes", "proj-0a1b2c3d", "home"),
	}
}

func TestUsage(t *testing.T) {
	t.Run("directory counts direct entries and every byte beneath", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "a"), 3)
		writeFile(t, filepath.Join(dir, "b"), 5)
		writeFile(t, filepath.Join(dir, "sub", "deep", "c"), 7)

		entries, size, err := Usage(dir)
		if err != nil {
			t.Fatalf("Usage: %v", err)
		}
		if entries != 3 {
			t.Errorf("entries = %d, want 3 (two files and one subdirectory)", entries)
		}
		if size != 15 {
			t.Errorf("size = %d, want 15 (bytes counted through the subtree)", size)
		}
	})

	t.Run("single file is one entry of its own size", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "wrapper.log")
		writeFile(t, path, 42)

		entries, size, err := Usage(path)
		if err != nil {
			t.Fatalf("Usage: %v", err)
		}
		if entries != 1 || size != 42 {
			t.Errorf("Usage = (%d, %d), want (1, 42)", entries, size)
		}
	})

	t.Run("empty directory", func(t *testing.T) {
		entries, size, err := Usage(t.TempDir())
		if err != nil {
			t.Fatalf("Usage: %v", err)
		}
		if entries != 0 || size != 0 {
			t.Errorf("Usage = (%d, %d), want (0, 0)", entries, size)
		}
	})

	t.Run("missing path is not an error", func(t *testing.T) {
		entries, size, err := Usage(filepath.Join(t.TempDir(), "never", "created"))
		if err != nil {
			t.Fatalf("Usage on a missing path: %v", err)
		}
		if entries != 0 || size != 0 {
			t.Errorf("Usage = (%d, %d), want (0, 0)", entries, size)
		}
	})

	t.Run("unreadable directory reports the error", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		dir := filepath.Join(t.TempDir(), "locked")
		if err := os.Mkdir(dir, 0o000); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

		if _, _, err := Usage(dir); err == nil {
			t.Error("Usage on an unreadable directory returned nil, want an error")
		}
	})
}

func TestLocationRun_RefusesIncompleteTarget(t *testing.T) {
	called := false
	sweep := func(Target) (int, error) {
		called = true
		return 1, nil
	}
	perSandbox := Location{Name: "per-sandbox", PerSandbox: true, Sweep: sweep}
	host := Location{Name: "host", Sweep: sweep}

	full := fullTarget()
	noHome := full
	noHome.SandboxHome = ""
	noRoot := full
	noRoot.SandboxRoot = ""
	noHomeDir := full
	noHomeDir.HomeDir = ""

	tests := []struct {
		name string
		loc  Location
		tgt  Target
	}{
		{"per-sandbox without SandboxHome", perSandbox, noHome},
		{"per-sandbox without SandboxRoot", perSandbox, noRoot},
		{"per-sandbox with nothing set", perSandbox, Target{}},
		{"host without HomeDir", host, noHomeDir},
		{"host with nothing set", host, Target{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called = false
			n, err := tt.loc.Run(tt.tgt)
			if err == nil {
				t.Fatal("Run returned nil error, want a refusal")
			}
			if !strings.Contains(err.Error(), tt.loc.Name) {
				t.Errorf("error %q does not name the location %q", err, tt.loc.Name)
			}
			if n != 0 {
				t.Errorf("removed = %d, want 0", n)
			}
			if called {
				t.Error("Sweep was called on an incomplete target")
			}
		})
	}
}

func TestLocationRun_CompleteTarget(t *testing.T) {
	full := fullTarget()

	t.Run("per-sandbox does not need HomeDir", func(t *testing.T) {
		tgt := full
		tgt.HomeDir = ""
		var got Target
		loc := Location{Name: "per-sandbox", PerSandbox: true, Sweep: func(t Target) (int, error) {
			got = t
			return 3, nil
		}}
		n, err := loc.Run(tgt)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if n != 3 {
			t.Errorf("removed = %d, want 3 passed through from Sweep", n)
		}
		if got != tgt {
			t.Errorf("Sweep saw %+v, want the target Run was given %+v", got, tgt)
		}
	})

	t.Run("host does not need sandbox roots", func(t *testing.T) {
		tgt := Target{HomeDir: full.HomeDir}
		loc := Location{Name: "host", Sweep: func(Target) (int, error) { return 2, nil }}
		n, err := loc.Run(tgt)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if n != 2 {
			t.Errorf("removed = %d, want 2", n)
		}
	})

	t.Run("sweep error is passed through", func(t *testing.T) {
		want := errors.New("sweep failed")
		loc := Location{Name: "host", Sweep: func(Target) (int, error) { return 1, want }}
		n, err := loc.Run(full)
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
		if n != 1 {
			t.Errorf("removed = %d, want the partial count 1", n)
		}
	})

	t.Run("nil Sweep reports nothing removed", func(t *testing.T) {
		for _, loc := range []Location{
			{Name: "reported per-sandbox", PerSandbox: true},
			{Name: "reported host"},
		} {
			n, err := loc.Run(full)
			if err != nil {
				t.Errorf("%s: Run = %v, want nil", loc.Name, err)
			}
			if n != 0 {
				t.Errorf("%s: removed = %d, want 0", loc.Name, n)
			}
		}
	})
}

// Sets process environment, so it must not call t.Parallel().
func TestLocations_Invariants(t *testing.T) {
	// Locations rooted in the state directory honor $XDG_STATE_HOME, which
	// can point anywhere; with it unset they fall back under the home the
	// Target names, which is what the containment check below assumes.
	t.Setenv("XDG_STATE_HOME", "")

	locs := Locations()
	if len(locs) == 0 {
		t.Fatal("Locations() is empty")
	}

	seen := make(map[string]bool, len(locs))
	full := fullTarget()
	for i, loc := range locs {
		if loc.Name == "" {
			t.Errorf("location %d has an empty Name", i)
			continue
		}
		if seen[loc.Name] {
			t.Errorf("location %q is registered twice", loc.Name)
		}
		seen[loc.Name] = true
		if loc.Path == nil {
			t.Errorf("%q has a nil Path", loc.Name)
			continue
		}

		// A per-sandbox path must be rooted in the sandbox it was given - or,
		// for the one location that lives under the home and keys the sandbox
		// by hash (live shared temp, pinned to its exact path by
		// TestLocations_LiveSharedTmp), in HomeDir - and a host path in the
		// home or the configured base: a path that lands elsewhere was built
		// from the wrong Target field.
		path := loc.Path(full)
		if !filepath.IsAbs(path) {
			t.Errorf("%q: Path %q is not absolute", loc.Name, path)
		}
		if loc.PerSandbox {
			if !within(path, full.SandboxRoot) && !within(path, full.HomeDir) {
				t.Errorf("%q: per-sandbox Path %q is outside SandboxRoot %q and HomeDir %q", loc.Name, path, full.SandboxRoot, full.HomeDir)
			}
		} else if !within(path, full.HomeDir) && !within(path, full.SandboxBase) {
			t.Errorf("%q: host Path %q is outside HomeDir %q and SandboxBase %q", loc.Name, path, full.HomeDir, full.SandboxBase)
		}
	}
}

func TestLocations_StableOrderAndCopy(t *testing.T) {
	first := names(Locations())
	second := names(Locations())
	if strings.Join(first, "\n") != strings.Join(second, "\n") {
		t.Errorf("order differs between calls:\n%v\n%v", first, second)
	}

	got := Locations()
	sweepWasNil := got[0].Sweep == nil
	got[0].Name = "mutated"
	got[0].Sweep = func(Target) (int, error) { return 0, errors.New("mutated") }
	again := Locations()
	if again[0].Name != first[0] {
		t.Errorf("mutating the returned slice renamed the catalogue entry to %q", again[0].Name)
	}
	if (again[0].Sweep == nil) != sweepWasNil {
		t.Error("mutating the returned slice replaced the catalogue entry's Sweep")
	}
}

// TestLocations_ReportedOnlyRunTouchesNothing runs every reported-only location
// against a real sandbox tree and asserts it leaves the tree alone: a nil Sweep
// must never act.
func TestLocations_ReportedOnlyRunTouchesNothing(t *testing.T) {
	base := t.TempDir()
	tgt := Target{
		HomeDir:     filepath.Join(base, "home"),
		SandboxBase: filepath.Join(base, "sandboxes"),
		SandboxRoot: filepath.Join(base, "sandboxes", "proj-0a1b2c3d"),
		SandboxHome: filepath.Join(base, "sandboxes", "proj-0a1b2c3d", "home"),
	}

	for _, loc := range Locations() {
		if loc.Sweep != nil {
			continue
		}
		path := loc.Path(tgt)
		writeFile(t, filepath.Join(path, "entry"), 9)

		n, err := loc.Run(tgt)
		if err != nil {
			t.Errorf("%q: Run = %v, want nil", loc.Name, err)
		}
		if n != 0 {
			t.Errorf("%q: removed = %d, want 0", loc.Name, n)
		}
		entries, size, err := Usage(path)
		if err != nil {
			t.Fatalf("%q: Usage: %v", loc.Name, err)
		}
		if entries != 1 || size != 9 {
			t.Errorf("%q: after Run, Usage = (%d, %d), want (1, 9): a reported-only location acted", loc.Name, entries, size)
		}
	}
}

// reapedPID returns the pid of a process that has exited and been waited for,
// so the kernel no longer knows it.
func reapedPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait for helper process: %v", err)
	}
	return pid
}

func locationNamed(t *testing.T, name string) Location {
	t.Helper()
	for _, loc := range Locations() {
		if loc.Name == name {
			return loc
		}
	}
	t.Fatalf("no location named %q in %v", name, names(Locations()))
	return Location{}
}

// TestLocations_EgressMarkers runs location 1 against a real marker root and
// asserts it sweeps through the owner's function: the marker of a dead launch
// goes, a live launch's stays, and the count reports what went.
//
// Sets process environment, so it must not call t.Parallel().
func TestLocations_EgressMarkers(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	loc := locationNamed(t, "egress markers")
	if loc.PerSandbox {
		t.Fatal("egress markers is registered per sandbox, want host-scoped")
	}
	tgt := Target{HomeDir: filepath.Join(t.TempDir(), "home")}

	root := loc.Path(tgt)
	if want := filepath.Join(state, "devsandbox", "egress"); root != want {
		t.Fatalf("Path = %q, want %q", root, want)
	}
	dead := filepath.Join(root, strconv.Itoa(reapedPID(t)))
	live := filepath.Join(root, strconv.Itoa(os.Getpid()))
	writeFile(t, filepath.Join(dead, "applied"), 0)
	writeFile(t, filepath.Join(live, "applied"), 0)

	n, err := loc.Run(tgt)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}
	if _, err := os.Stat(dead); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("marker of a dead launch survived: stat = %v", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("marker of a live launch was removed: stat = %v", err)
	}
}

func names(locs []Location) []string {
	out := make([]string, len(locs))
	for i, l := range locs {
		out[i] = l.Name
	}
	return out
}

// within reports whether path is dir or lies beneath it, lexically.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// TestLocations_SessionRecords runs location 2 against a real store and
// asserts it sweeps through the owner's function: a host with no store is
// nothing to do, the record of a dead session goes, a live one stays, and the
// catalogue's path is the store's own.
//
// Sets process environment, so it must not call t.Parallel().
func TestLocations_SessionRecords(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	loc := locationNamed(t, "session records")
	if loc.PerSandbox {
		t.Fatal("session records is registered per sandbox, want host-scoped")
	}
	tgt := Target{HomeDir: filepath.Join(t.TempDir(), "home")}

	dir := loc.Path(tgt)
	if want := filepath.Join(state, "devsandbox", "sessions"); dir != want {
		t.Fatalf("Path = %q, want %q", dir, want)
	}
	n, err := loc.Run(tgt)
	if err != nil || n != 0 {
		t.Fatalf("Run on a host with no store = (%d, %v), want (0, nil)", n, err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	store := session.NewStore(dir)
	if got := store.Dir(); got != dir {
		t.Errorf("store.Dir() = %q, want the catalogue path %q", got, dir)
	}
	for name, pid := range map[string]int{"dead": reapedPID(t), "live": os.Getpid()} {
		sess := &session.Session{Name: name, PID: pid, StartedAt: time.Now(), WorkDir: t.TempDir()}
		if err := store.Register(sess); err != nil {
			t.Fatalf("Register %s: %v", name, err)
		}
	}

	n, err = loc.Run(tgt)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}
	if _, err := store.Get("dead"); err == nil {
		t.Error("record of a dead session survived")
	}
	if _, err := store.Get("live"); err != nil {
		t.Errorf("record of a live session was removed: %v", err)
	}
}

// TestLocations_HerdrPaneRecords runs location 3 against a real store and
// asserts it sweeps through the owner's function: a host with no store is
// nothing to do, the record of a pane whose sandbox is gone goes, one whose
// sandbox still exists stays, and the catalogue's path is the store's own.
//
// Sets process environment, so it must not call t.Parallel().
func TestLocations_HerdrPaneRecords(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	loc := locationNamed(t, "herdr pane records")
	if loc.PerSandbox {
		t.Fatal("herdr pane records is registered per sandbox, want host-scoped")
	}
	tgt := Target{HomeDir: filepath.Join(t.TempDir(), "home")}

	dir := loc.Path(tgt)
	if want := filepath.Join(state, "devsandbox", "herdr-panes"); dir != want {
		t.Fatalf("Path = %q, want %q", dir, want)
	}
	n, err := loc.Run(tgt)
	if err != nil || n != 0 {
		t.Fatalf("Run on a host with no store = (%d, %v), want (0, nil)", n, err)
	}

	store := herdrstate.NewStore(dir)
	if got := store.Dir(); got != dir {
		t.Errorf("store.Dir() = %q, want the catalogue path %q", got, dir)
	}
	projectDir := t.TempDir()
	base := filepath.Join(t.TempDir(), "sandboxes")
	liveRoot := filepath.Join(base, "live-0a1b2c3d")
	if err := os.MkdirAll(liveRoot, 0o755); err != nil {
		t.Fatalf("create live sandbox root: %v", err)
	}
	const gonePane, livePane = "w1F:p1C", "w1F:p2D"
	for paneID, root := range map[string]string{
		gonePane: filepath.Join(base, "gone-0a1b2c3d"),
		livePane: liveRoot,
	} {
		rec := herdrstate.Record{PaneID: paneID, Agent: "claude", ProjectDir: projectDir, SandboxRoot: root}
		if err := store.Save(rec); err != nil {
			t.Fatalf("Save %s: %v", paneID, err)
		}
	}

	n, err = loc.Run(tgt)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}
	if _, err := store.Load(gonePane); !errors.Is(err, herdrstate.ErrNotFound) {
		t.Errorf("record of a pane whose sandbox is gone survived: Load = %v", err)
	}
	if _, err := store.Load(livePane); err != nil {
		t.Errorf("record of a pane whose sandbox exists was removed: %v", err)
	}
}

// TestLocations_InterruptedRemovals runs location 5 against a real sandbox
// base and asserts it sweeps through the owner's function: a base with no
// staging directory is nothing to do, the tree of a dead teardown goes, a live
// teardown's stays, and an empty SandboxBase is refused rather than resolved
// against the working directory.
func TestLocations_InterruptedRemovals(t *testing.T) {
	loc := locationNamed(t, "interrupted removals")
	if loc.PerSandbox {
		t.Fatal("interrupted removals is registered per sandbox, want host-scoped")
	}
	base := filepath.Join(t.TempDir(), "sandboxes")
	tgt := Target{HomeDir: filepath.Join(t.TempDir(), "home"), SandboxBase: base}

	staging := loc.Path(tgt)
	if want := filepath.Join(base, ".removing"); staging != want {
		t.Fatalf("Path = %q, want %q", staging, want)
	}
	n, err := loc.Run(tgt)
	if err != nil || n != 0 {
		t.Fatalf("Run on a base with no staging directory = (%d, %v), want (0, nil)", n, err)
	}

	dead := filepath.Join(staging, "proj-0a1b2c3d-"+strconv.Itoa(reapedPID(t)))
	live := filepath.Join(staging, "proj-0a1b2c3d-"+strconv.Itoa(os.Getpid()))
	writeFile(t, filepath.Join(dead, "home", "file"), 1)
	writeFile(t, filepath.Join(live, "home", "file"), 1)

	n, err = loc.Run(tgt)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}
	if _, err := os.Stat(dead); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("tree of a dead teardown survived: stat = %v", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("tree of a live teardown was removed: stat = %v", err)
	}

	noBase := tgt
	noBase.SandboxBase = ""
	n, err = loc.Run(noBase)
	if err == nil {
		t.Fatal("Run with an empty SandboxBase returned nil, want a refusal")
	}
	if n != 0 {
		t.Errorf("removed = %d, want 0", n)
	}
}

// backdate sets every entry under root to when, so a sweep reads it as stale.
func backdate(t *testing.T, root string, when time.Time) {
	t.Helper()
	err := filepath.WalkDir(root, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, when, when)
	})
	if err != nil {
		t.Fatalf("backdate %s: %v", root, err)
	}
}

// TestLocations_OrphanedSharedTmp runs location 6 against a real sandbox
// base and asserts the live set is built from Target.SandboxBase with each
// home spelled as the launch spells it: the directory of a sandbox that
// exists is kept however old, the directory of one that does not goes once
// stale, a recent orphan stays, the catalogue names the root the launch
// binds under, and an empty base is refused rather than naming every
// directory an orphan.
func TestLocations_OrphanedSharedTmp(t *testing.T) {
	loc := locationNamed(t, "orphaned shared temp")
	if loc.PerSandbox {
		t.Fatal("orphaned shared temp is registered per sandbox, want host-scoped")
	}
	homeDir := filepath.Join(t.TempDir(), "home")
	base := filepath.Join(t.TempDir(), "sandboxes")
	tgt := Target{HomeDir: homeDir, SandboxBase: base}
	if got, want := loc.Path(tgt), tools.SharedTmpRoot(homeDir); got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
	n, err := loc.Run(tgt)
	if err != nil || n != 0 {
		t.Fatalf("Run on a host with no shared temp = (%d, %v), want (0, nil)", n, err)
	}

	// A sandbox on disk with no metadata file: the disk listing is what
	// decides, and it lists every directory under the base.
	liveRoot := filepath.Join(base, "live-0a1b2c3d")
	if err := os.MkdirAll(sandbox.SandboxHomePath(liveRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	live := tools.SharedTmpPath(homeDir, sandbox.SandboxHomePath(liveRoot))
	orphan := tools.SharedTmpPath(homeDir, sandbox.SandboxHomePath(filepath.Join(base, "gone-0a1b2c3d")))
	recent := tools.SharedTmpPath(homeDir, sandbox.SandboxHomePath(filepath.Join(base, "new-0a1b2c3d")))
	for _, dir := range []string{live, orphan, recent} {
		writeFile(t, filepath.Join(dir, "go-build", "a.o"), 1)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	backdate(t, live, old)
	backdate(t, orphan, old)

	n, err = loc.Run(tgt)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("shared temp directory of a removed sandbox survived: stat = %v", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("shared temp directory of a live sandbox was removed as an orphan: %v", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("recent shared temp directory was removed: %v", err)
	}

	noBase := tgt
	noBase.SandboxBase = ""
	backdate(t, recent, old)
	n, err = loc.Run(noBase)
	if err == nil {
		t.Fatal("Run with an empty SandboxBase returned nil, want a refusal")
	}
	if n != 0 {
		t.Errorf("removed = %d, want 0", n)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("a refused run removed a directory: %v", err)
	}
}

// TestLocations_LiveSharedTmp runs location 9 against a real directory and
// asserts the catalogue names the directory the launch binds - hashed from
// SandboxHome, under HomeDir - and sweeps by age alone: a stale entry goes, a
// fresh one stays with no sibling registered, the directory itself stays, and
// a target without HomeDir is refused by the owner, since Run checks only the
// sandbox roots for a per-sandbox location.
func TestLocations_LiveSharedTmp(t *testing.T) {
	loc := locationNamed(t, "live shared temp")
	if !loc.PerSandbox {
		t.Fatal("live shared temp is registered host-scoped, want per sandbox")
	}
	base := t.TempDir()
	root := filepath.Join(base, "sandboxes", "proj-0a1b2c3d")
	tgt := Target{
		HomeDir:     filepath.Join(base, "home"),
		SandboxBase: filepath.Join(base, "sandboxes"),
		SandboxRoot: root,
		SandboxHome: sandbox.SandboxHomePath(root),
	}
	dir := loc.Path(tgt)
	if want := tools.SharedTmpPath(tgt.HomeDir, tgt.SandboxHome); dir != want {
		t.Fatalf("Path = %q, want %q", dir, want)
	}
	n, err := loc.Run(tgt)
	if err != nil || n != 0 {
		t.Fatalf("Run on a sandbox with no shared temp = (%d, %v), want (0, nil)", n, err)
	}

	stale := filepath.Join(dir, "go-build-old")
	fresh := filepath.Join(dir, "go-build-new")
	writeFile(t, filepath.Join(stale, "a.o"), 1)
	writeFile(t, filepath.Join(fresh, "a.o"), 1)
	backdate(t, stale, time.Now().Add(-8*24*time.Hour))

	n, err = loc.Run(tgt)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale entry survived: stat = %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh entry removed with no sibling registered: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the directory itself was removed: %v", err)
	}

	noHome := tgt
	noHome.HomeDir = ""
	n, err = loc.Run(noHome)
	if err == nil {
		t.Fatal("Run with an empty HomeDir returned nil, want a refusal")
	}
	if n != 0 {
		t.Errorf("removed = %d, want 0", n)
	}
}
