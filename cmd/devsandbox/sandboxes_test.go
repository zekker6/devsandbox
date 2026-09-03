package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devsandbox/internal/reclaim"
	"devsandbox/internal/sandbox"
	"devsandbox/internal/sandbox/tools"
	"devsandbox/internal/session"
)

// The listing is where an OOM kill becomes visible: the sandbox is gone and its
// session file with it, so a status column that dropped the record would leave the
// user with the same silent disappearance the record exists to explain.
func TestFormatSandboxStatus(t *testing.T) {
	tests := []struct {
		name string
		meta *sandbox.Metadata
		want string
	}{
		{
			name: "a plain idle sandbox has no status",
			meta: &sandbox.Metadata{},
			want: "",
		},
		{
			name: "a killed sandbox is marked",
			meta: &sandbox.Metadata{LastOOM: &sandbox.OOMRecord{Kills: 1, Fatal: true}},
			want: "oom-killed",
		},
		{
			name: "kills inside a surviving sandbox are counted",
			meta: &sandbox.Metadata{LastOOM: &sandbox.OOMRecord{Kills: 2}},
			want: "oom-kills(2)",
		},
		{
			name: "a running session that lost a process shows both",
			meta: &sandbox.Metadata{Active: true, LastOOM: &sandbox.OOMRecord{Kills: 1}},
			want: "active, oom-kills(1)",
		},
		{
			name: "the pre-existing states are unchanged",
			meta: &sandbox.Metadata{
				Orphaned:  true,
				Active:    true,
				Isolation: sandbox.IsolationDocker,
				State:     "exited",
			},
			want: "orphaned, active, exited",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatSandboxStatus(tt.meta); got != tt.want {
				t.Errorf("formatSandboxStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

// countingLocation builds a location whose sweep records the targets it was
// run against, so a test can tell which half of the catalogue a target
// selected and how often each entry ran.
func countingLocation(name string, perSandbox bool, seen *[]reclaim.Target) reclaim.Location {
	return reclaim.Location{
		Name:       name,
		PerSandbox: perSandbox,
		Path: func(t reclaim.Target) string {
			if perSandbox {
				return t.SandboxRoot
			}
			return t.HomeDir
		},
		Sweep: func(t reclaim.Target) (int, error) {
			*seen = append(*seen, t)
			return 1, nil
		},
	}
}

// The catalogue is passed whole to every call, so the target has to be what
// decides which locations run: a per-sandbox sweep run against a host target
// would build its paths from an empty SandboxRoot.
func TestRunReclaimSelectsLocationsByTarget(t *testing.T) {
	home := t.TempDir()
	var hostSeen, perSeen []reclaim.Target
	locs := []reclaim.Location{
		countingLocation("host one", false, &hostSeen),
		countingLocation("per one", true, &perSeen),
		countingLocation("host two", false, &hostSeen),
	}

	var buf bytes.Buffer
	hostTarget := reclaim.Target{HomeDir: home, SandboxBase: filepath.Join(home, "base")}
	if err := runReclaim(&buf, hostTarget, locs, false); err != nil {
		t.Fatalf("runReclaim(host) = %v, want nil", err)
	}
	for _, name := range []string{"alpha-1234abcd", "beta-5678efgh"} {
		root := filepath.Join(home, "base", name)
		target := hostTarget
		target.SandboxRoot = root
		target.SandboxHome = sandbox.SandboxHomePath(root)
		if err := runReclaim(&buf, target, locs, false); err != nil {
			t.Fatalf("runReclaim(%s) = %v, want nil", name, err)
		}
	}

	if len(hostSeen) != 2 {
		t.Errorf("host sweeps ran %d time(s), want 2 (once each, on the host target only)", len(hostSeen))
	}
	for _, target := range hostSeen {
		if target.SandboxRoot != "" {
			t.Errorf("host sweep ran against sandbox root %q, want none", target.SandboxRoot)
		}
	}
	if len(perSeen) != 2 {
		t.Errorf("per-sandbox sweep ran %d time(s), want 2 (once per sandbox)", len(perSeen))
	}
	for _, target := range perSeen {
		if target.SandboxRoot == "" || target.SandboxHome == "" {
			t.Errorf("per-sandbox sweep ran against %+v, want SandboxRoot and SandboxHome set", target)
		}
	}
	out := buf.String()
	for _, name := range []string{"host one", "host two", "per one"} {
		if !strings.Contains(out, name) {
			t.Errorf("report is missing %q:\n%s", name, out)
		}
	}
}

// Each location is independent, so a prune that stopped at the first failure
// would leave every location after it unreclaimed - and the failure has to
// reach both the report and the exit status rather than being swallowed.
func TestRunReclaimReportsFailureAndContinues(t *testing.T) {
	home := t.TempDir()
	var laterSeen []reclaim.Target
	locs := []reclaim.Location{
		{
			Name:  "broken",
			Path:  func(t reclaim.Target) string { return t.HomeDir },
			Sweep: func(reclaim.Target) (int, error) { return 0, errors.New("disk on fire") },
		},
		countingLocation("later", false, &laterSeen),
	}

	var buf bytes.Buffer
	err := runReclaim(&buf, reclaim.Target{HomeDir: home}, locs, false)
	if err == nil {
		t.Fatal("runReclaim() = nil, want the failing location reported as an error")
	}
	if !strings.Contains(err.Error(), "broken") || !strings.Contains(err.Error(), "disk on fire") {
		t.Errorf("error %q names neither the location nor the cause", err)
	}
	if len(laterSeen) != 1 {
		t.Errorf("location after the failing one ran %d time(s), want 1", len(laterSeen))
	}
	out := buf.String()
	if !strings.Contains(out, "broken") || !strings.Contains(out, "disk on fire") {
		t.Errorf("report does not name the failure:\n%s", out)
	}
	if !strings.Contains(out, "later") {
		t.Errorf("report is missing the location after the failing one:\n%s", out)
	}
}

// --dry-run is the one way to see what a location holds without touching it,
// so it has to report every location and sweep none of them.
func TestRunReclaimDryRunSweepsNothing(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "marker"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	var seen []reclaim.Target
	locs := []reclaim.Location{
		countingLocation("host one", false, &seen),
		countingLocation("host two", false, &seen),
	}

	var buf bytes.Buffer
	if err := runReclaim(&buf, reclaim.Target{HomeDir: home}, locs, true); err != nil {
		t.Fatalf("runReclaim() = %v, want nil", err)
	}
	if len(seen) != 0 {
		t.Errorf("dry run called %d sweep(s), want 0", len(seen))
	}
	out := buf.String()
	for _, name := range []string{"host one", "host two"} {
		if !strings.Contains(out, name) {
			t.Errorf("dry run report is missing %q:\n%s", name, out)
		}
	}
	if !strings.Contains(out, "1 entry") {
		t.Errorf("dry run report does not show what the location holds:\n%s", out)
	}
	if !strings.Contains(out, home) {
		t.Errorf("dry run report does not name the path:\n%s", out)
	}
}

// The report has to be testable without capturing the process's own stdout,
// which is what pins the writer as an argument rather than os.Stdout.
func TestRunReclaimWritesToInjectedWriter(t *testing.T) {
	home := t.TempDir()
	var seen []reclaim.Target
	locs := []reclaim.Location{countingLocation("host one", false, &seen)}

	stdout := os.Stdout
	captured, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	os.Stdout = captured
	defer func() { os.Stdout = stdout }()

	var buf bytes.Buffer
	if err := runReclaim(&buf, reclaim.Target{HomeDir: home}, locs, true); err != nil {
		t.Fatalf("runReclaim() = %v, want nil", err)
	}
	os.Stdout = stdout

	if err := captured.Close(); err != nil {
		t.Fatalf("close temp: %v", err)
	}
	onStdout, err := os.ReadFile(captured.Name())
	if err != nil {
		t.Fatalf("read temp: %v", err)
	}
	if len(onStdout) != 0 {
		t.Errorf("runReclaim wrote %q to os.Stdout, want everything on the injected writer", onStdout)
	}
	if !strings.Contains(buf.String(), "host one") {
		t.Errorf("injected writer got %q, want the report", buf.String())
	}
}

// A prune on a host with nothing to prune is the common case, and the leak
// that motivated the catalogue lives in the host-scoped set - so those
// locations have to be reported before the "no sandboxes" return, not after.
func TestPruneReportsHostLocationsWithNoSandboxes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	cmd := newPruneCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("prune --dry-run = %v, want nil", err)
	}

	out := buf.String()
	for _, loc := range reclaim.Locations() {
		if loc.PerSandbox {
			continue
		}
		if !strings.Contains(out, loc.Name) {
			t.Errorf("prune output is missing host location %q:\n%s", loc.Name, out)
		}
	}
}

// TestPruneReportsPerSandboxLocationsForRemainingSandboxes covers the other
// half of the catalogue against the real command. A sandbox that survives the
// selection still holds run directories, overlay dirs, a shared temp directory
// and two log trees, and those are only reachable through a target naming that
// sandbox - so a prune that reported the host set alone would leave every
// per-sandbox location unreported on every host. The sandbox here is not
// orphaned, which is the common case for a bare `prune`: nothing is selected,
// and the per-sandbox set has to run on that path too.
func TestPruneReportsPerSandboxLocationsForRemainingSandboxes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	projectDir := filepath.Join(home, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	sandboxRoot := filepath.Join(sandbox.SandboxBasePath(home), "project-0a1b2c3d")
	if err := os.MkdirAll(sandbox.SandboxHomePath(sandboxRoot), 0o755); err != nil {
		t.Fatalf("mkdir sandbox: %v", err)
	}
	meta := &sandbox.Metadata{
		Name:       "project-0a1b2c3d",
		ProjectDir: projectDir,
		CreatedAt:  time.Now(),
		LastUsed:   time.Now(),
	}
	if err := sandbox.SaveMetadata(meta, sandboxRoot); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	cmd := newPruneCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("prune --dry-run = %v, want nil", err)
	}

	out := buf.String()
	for _, loc := range reclaim.Locations() {
		if !strings.Contains(out, loc.Name) {
			t.Errorf("prune output is missing location %q:\n%s", loc.Name, out)
		}
	}
	if !strings.Contains(out, "Sandbox state (project-0a1b2c3d)") {
		t.Errorf("prune output does not name the remaining sandbox:\n%s", out)
	}
}

// makeSandbox plants a sandbox on disk under base with its metadata, and
// returns its state root. projectDir is created only when create is true, so a
// caller can produce the orphan `prune` selects with no flags.
func makeSandbox(t *testing.T, home, name string, createProject bool) string {
	t.Helper()
	projectDir := filepath.Join(home, "projects", name)
	if createProject {
		if err := os.MkdirAll(projectDir, 0o755); err != nil {
			t.Fatalf("mkdir project: %v", err)
		}
	}
	root := filepath.Join(sandbox.SandboxBasePath(home), name+"-0a1b2c3d")
	if err := os.MkdirAll(sandbox.SandboxHomePath(root), 0o755); err != nil {
		t.Fatalf("mkdir sandbox: %v", err)
	}
	meta := &sandbox.Metadata{
		Name:       filepath.Base(root),
		ProjectDir: projectDir,
		CreatedAt:  time.Now(),
		LastUsed:   time.Now(),
	}
	if err := sandbox.SaveMetadata(meta, root); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	return root
}

func prepareHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

// TestPruneSweepsSurvivorsAndNotTheRemoved runs the command with a sandbox
// actually selected, which the reporting tests above never do: they leave
// toPrune empty, so the removal loop, the skip set and every real sweep are
// unexercised. A sandbox this run deleted must not be reported as remaining -
// reporting it would size a tree that no longer exists - and the survivor's
// shared temp must be swept by age.
func TestPruneSweepsSurvivorsAndNotTheRemoved(t *testing.T) {
	home := prepareHome(t)
	orphanRoot := makeSandbox(t, home, "gone", false)
	keptRoot := makeSandbox(t, home, "kept", true)

	// A stale entry in the survivor's shared temp, which the age branch takes.
	keptTmp := tools.SharedTmpPath(home, sandbox.SandboxHomePath(keptRoot))
	stale := filepath.Join(keptTmp, "go-build")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatalf("mkdir shared temp: %v", err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	for _, p := range []string{stale, keptTmp} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatalf("chtimes %s: %v", p, err)
		}
	}

	cmd := newPruneCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("prune --force = %v, want nil", err)
	}

	out := buf.String()
	if strings.Contains(out, "Sandbox state ("+filepath.Base(orphanRoot)+")") {
		t.Errorf("the removed sandbox is reported as remaining:\n%s", out)
	}
	if !strings.Contains(out, "Sandbox state ("+filepath.Base(keptRoot)+")") {
		t.Errorf("the surviving sandbox is not reported:\n%s", out)
	}
	if _, err := os.Stat(orphanRoot); !os.IsNotExist(err) {
		t.Errorf("the orphaned sandbox survived, stat err = %v", err)
	}
	if _, err := os.Stat(keptRoot); err != nil {
		t.Errorf("the surviving sandbox was removed: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the survivor's stale shared temp entry was not swept, stat err = %v", err)
	}
}

// TestPruneDryRunRemovesNothing: --dry-run selects the same orphan and must
// leave it on disk, while still skipping it in the per-sandbox report - the
// report previews the state after the prune, not before it.
func TestPruneDryRunRemovesNothing(t *testing.T) {
	home := prepareHome(t)
	orphanRoot := makeSandbox(t, home, "gone", false)

	cmd := newPruneCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("prune --dry-run = %v, want nil", err)
	}

	if _, err := os.Stat(orphanRoot); err != nil {
		t.Errorf("--dry-run removed the sandbox: %v", err)
	}
	if strings.Contains(buf.String(), "Sandbox state ("+filepath.Base(orphanRoot)+")") {
		t.Errorf("--dry-run reports a sandbox it is previewing the removal of:\n%s", buf.String())
	}
}

// TestConfiguredSandboxBase covers the base every path in prune is built from.
// The catalogue's orphan sweep finds an orphan by elimination against the
// sandboxes under this base, so a base read from the wrong place does not just
// fail to prune - it names every live sandbox an orphan.
func TestConfiguredSandboxBase(t *testing.T) {
	t.Run("honors sandbox.base_path", func(t *testing.T) {
		home := prepareHome(t)
		configured := filepath.Join(home, "elsewhere", "sandboxes")
		writeGlobalConfig(t, home, "[sandbox]\nbase_path = \""+configured+"\"\n")

		got, err := configuredSandboxBase(home)
		if err != nil {
			t.Fatalf("configuredSandboxBase: %v", err)
		}
		if got != configured {
			t.Errorf("base = %q, want the configured %q", got, configured)
		}
	})

	t.Run("falls back to the default when the key is unset", func(t *testing.T) {
		home := prepareHome(t)
		got, err := configuredSandboxBase(home)
		if err != nil {
			t.Fatalf("configuredSandboxBase: %v", err)
		}
		if want := sandbox.SandboxBasePath(home); got != want {
			t.Errorf("base = %q, want %q", got, want)
		}
	})

	t.Run("a config that cannot be read fails rather than guessing", func(t *testing.T) {
		home := prepareHome(t)
		writeGlobalConfig(t, home, "[sandbox\nbase_path = broken\n")

		if _, err := configuredSandboxBase(home); err == nil {
			t.Fatal("configuredSandboxBase accepted a malformed config; a guessed base makes the orphan sweep delete live sandboxes' shared temp")
		}
	})

	t.Run("ignores the project-local config", func(t *testing.T) {
		home := prepareHome(t)
		project := t.TempDir()
		local := filepath.Join(project, ".devsandbox.toml")
		if err := os.WriteFile(local, []byte("[sandbox]\nbase_path = \"/tmp/project-local\"\n"), 0o644); err != nil {
			t.Fatalf("write local config: %v", err)
		}
		chdir(t, project)

		got, err := configuredSandboxBase(home)
		if err != nil {
			t.Fatalf("configuredSandboxBase: %v", err)
		}
		if got == "/tmp/project-local" {
			t.Error("prune read the working directory's .devsandbox.toml; the sandbox base is a host-level setting")
		}
	})
}

func writeGlobalConfig(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "devsandbox")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

// TestPruneRemovesWorktreesOfPrunedSandboxes pins the ordering between the
// host-scoped sweep and the removal loop. Session records are one of the
// catalogued locations, and their sweep removes every record whose pid is
// dead - and a sandbox selected for pruning is inactive by definition, so its
// records are exactly the ones the sweep takes. Reading the store after the
// sweep finds nothing, and the git worktrees those records register stay
// registered in the repository with their checkout deleted underneath them.
func TestPruneRemovesWorktreesOfPrunedSandboxes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	home := prepareHome(t)

	repo := filepath.Join(home, "repo")
	git(t, "", "init", repo)
	git(t, repo, "-c", "user.email=t@example.com", "-c", "user.name=t",
		"commit", "--allow-empty", "-m", "initial")

	orphanRoot := makeSandbox(t, home, "gone", false)
	wtPath := filepath.Join(orphanRoot, "worktree")
	git(t, repo, "worktree", "add", "-b", "feature", wtPath)

	store, err := session.DefaultStore()
	if err != nil {
		t.Fatalf("session store: %v", err)
	}
	if err := store.Register(&session.Session{
		Name:    "gone",
		PID:     999999999, // dead: the record the sweep reclaims
		WorkDir: wtPath,
		Worktree: &session.WorktreeInfo{
			RepoRoot: repo,
			Path:     wtPath,
			Branch:   "feature",
		},
	}); err != nil {
		t.Fatalf("register session: %v", err)
	}

	cmd := newPruneCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("prune --force = %v, want nil", err)
	}

	list := gitOutput(t, repo, "worktree", "list", "--porcelain")
	if strings.Contains(list, wtPath) {
		t.Errorf("the pruned sandbox's worktree is still registered:\n%s\nprune said:\n%s", list, buf.String())
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	gitOutput(t, dir, args...)
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
