package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devsandbox/internal/reclaim"
	"devsandbox/internal/sandbox"
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
