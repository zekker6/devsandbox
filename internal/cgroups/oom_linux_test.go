//go:build linux

package cgroups

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"devsandbox/internal/fsutil"
)

// shortCtx bounds a resolution that is expected to give up.
func shortCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

// longCtx bounds a resolution that is expected to succeed, with enough slack that
// a loaded machine does not fail the test.
func longCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// fakeProcCgroup points the pid -> cgroup lookup at a directory of files named
// after pids, so the resolution can be driven without a real process.
func fakeProcCgroup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := procPIDCgroup
	procPIDCgroup = func(pid int) string { return filepath.Join(dir, "proc", pidName(pid)) }
	t.Cleanup(func() { procPIDCgroup = prev })
	if err := os.MkdirAll(filepath.Join(dir, "proc"), 0o755); err != nil {
		t.Fatalf("create fake procfs: %v", err)
	}
	return dir
}

func pidName(pid int) string {
	return "pid-" + strconv.Itoa(pid)
}

// writeProcCgroup replaces the fake cgroup file atomically. A real
// /proc/<pid>/cgroup read is a kernel-generated snapshot, so a poller never sees
// it half-written; a plain os.WriteFile truncates first, which lets a concurrent
// resolution read an empty file and reject the host as cgroup v1.
// writeProcCgroup replaces the fake cgroup file atomically. A real
// /proc/<pid>/cgroup read is a kernel-generated snapshot, so a poller never sees
// it half-written; a plain os.WriteFile truncates first, which lets a concurrent
// resolution read an empty file and reject the host as cgroup v1.
func writeProcCgroup(t *testing.T, dir string, pid int, content string) {
	t.Helper()
	if err := fsutil.WriteFileAtomic(filepath.Join(dir, "proc", pidName(pid)), []byte(content), 0o644); err != nil {
		t.Fatalf("write fake /proc/%d/cgroup: %v", pid, err)
	}
}

// fakeCgroupRoot repoints the unified mount point so a resolved scope path can be
// asserted against a directory the test owns.
func fakeCgroupRoot(t *testing.T, dir string) {
	t.Helper()
	prev := cgroupRoot
	cgroupRoot = dir
	t.Cleanup(func() { cgroupRoot = prev })
}

// The whole attribution guarantee rests on this: the counters are only trusted
// when the cgroup is the transient scope this process asked systemd to create.
// Accepting the enclosing cgroup instead would report OOM kills of unrelated
// sibling processes - a terminal's whole session - as the sandbox being killed.
func TestScopeCgroupDirRequiresOurOwnScope(t *testing.T) {
	const pid = 4242

	tests := []struct {
		name        string
		procContent string
		wantErr     error
		wantSuffix  string
	}{
		{
			name:        "our transient scope resolves",
			procContent: "0::/user.slice/user-1000.slice/user@1000.service/app.slice/" + unitName() + ".scope\n",
			wantSuffix:  "/user.slice/user-1000.slice/user@1000.service/app.slice/" + unitName() + ".scope",
		},
		{
			name:        "another session's scope is refused",
			procContent: "0::/user.slice/user-1000.slice/user@1000.service/app.slice/devsandbox-999-deadbeef.scope\n",
			wantErr:     ErrNoOwnCgroup,
		},
		{
			name:        "a shared session cgroup is refused",
			procContent: "0::/user.slice/user-1000.slice/session-3.scope\n",
			wantErr:     ErrNoOwnCgroup,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := fakeProcCgroup(t)
			fakeCgroupRoot(t, filepath.Join(dir, "cgroup"))
			writeProcCgroup(t, dir, pid, tt.procContent)

			got, err := ScopeCgroupDir(shortCtx(t), pid)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ScopeCgroupDir() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ScopeCgroupDir() error = %v", err)
			}
			want := filepath.Join(dir, "cgroup") + tt.wantSuffix
			if got != want {
				t.Errorf("ScopeCgroupDir() = %q, want %q", got, want)
			}
		})
	}
}

// systemd creates the scope over D-Bus, so the caller holds the pid before the
// migration lands. Failing on the first read would leave every launch unmonitored
// on a host that answers a few milliseconds slower than this one.
func TestScopeCgroupDirWaitsForTheMigration(t *testing.T) {
	const pid = 77
	dir := fakeProcCgroup(t)
	fakeCgroupRoot(t, filepath.Join(dir, "cgroup"))
	writeProcCgroup(t, dir, pid, "0::/user.slice/user-1000.slice/session-3.scope\n")

	go func() {
		time.Sleep(50 * time.Millisecond)
		writeProcCgroup(t, dir, pid, "0::/user.slice/app.slice/"+unitName()+".scope\n")
	}()

	got, err := ScopeCgroupDir(longCtx(t), pid)
	if err != nil {
		t.Fatalf("ScopeCgroupDir() error = %v", err)
	}
	if filepath.Base(got) != unitName()+".scope" {
		t.Errorf("ScopeCgroupDir() = %q, want it to end at our own scope", got)
	}
}

// A sandbox that finishes before systemd migrates it is not a monitoring failure
// the user should be warned about, so it gets its own error.
func TestScopeCgroupDirReportsAGoneProcess(t *testing.T) {
	dir := fakeProcCgroup(t)
	fakeCgroupRoot(t, filepath.Join(dir, "cgroup"))

	_, err := ScopeCgroupDir(shortCtx(t), 1234)
	if !errors.Is(err, ErrProcessGone) {
		t.Fatalf("ScopeCgroupDir() error = %v, want ErrProcessGone", err)
	}
}

func TestScopeCgroupDirRejectsCgroupV1(t *testing.T) {
	const pid = 5
	dir := fakeProcCgroup(t)
	fakeCgroupRoot(t, filepath.Join(dir, "cgroup"))
	writeProcCgroup(t, dir, pid, "6:memory:/user.slice\n5:cpu,cpuacct:/user.slice\n")

	_, err := ScopeCgroupDir(shortCtx(t), pid)
	if err == nil {
		t.Fatal("ScopeCgroupDir() = nil error, want a cgroup v1 rejection")
	}
	if !strings.Contains(err.Error(), "cgroup v1") {
		t.Errorf("ScopeCgroupDir() error = %q, want it to name cgroup v1", err)
	}
}

// writeEvents replaces a memory.events file, which is what the kernel does to the
// real one and what the watch reacts to.
func writeEvents(t *testing.T, path string, kills, groupKills int) {
	t.Helper()
	body := "low 0\nhigh 0\nmax 0\noom " + strconv.Itoa(kills) + "\noom_kill " + strconv.Itoa(kills) +
		"\noom_group_kill " + strconv.Itoa(groupKills) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write memory.events: %v", err)
	}
}

// collector records every reported observation.
type collector struct {
	mu   sync.Mutex
	seen []OOMStats
}

func (c *collector) record(s OOMStats) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, s)
}

func (c *collector) last() (OOMStats, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.seen) == 0 {
		return OOMStats{}, 0
	}
	return c.seen[len(c.seen)-1], len(c.seen)
}

// waitFor polls cond until it holds or the deadline passes. The watch is driven
// by an asynchronous kernel notification, so the assertion cannot be immediate.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestWatchOOMReportsKills(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.events")
	writeEvents(t, path, 0, 0)

	var c collector
	w, err := WatchOOM(dir, CgroupFresh, c.record)
	if err != nil {
		t.Fatalf("WatchOOM() error = %v", err)
	}
	defer w.Stop()

	writeEvents(t, path, 1, 0)
	waitFor(t, "the first OOM kill", func() bool {
		got, _ := c.last()
		return got == OOMStats{Kills: 1}
	})

	writeEvents(t, path, 3, 0)
	waitFor(t, "the second batch of OOM kills", func() bool {
		got, _ := c.last()
		return got == OOMStats{Kills: 3}
	})

	if got := w.Stats(); got != (OOMStats{Kills: 3}) {
		t.Errorf("Stats() = %+v, want the latest observation", got)
	}
}

// memory.events also changes for its low/high/max pressure counters, which move
// under ordinary memory use. Reporting those as OOM kills would make the warning
// meaningless.
func TestWatchOOMIgnoresNonOOMChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.events")
	writeEvents(t, path, 0, 0)

	var c collector
	w, err := WatchOOM(dir, CgroupFresh, c.record)
	if err != nil {
		t.Fatalf("WatchOOM() error = %v", err)
	}
	defer w.Stop()

	for i := range 5 {
		body := "low 0\nhigh " + strconv.Itoa(i+1) + "\nmax 0\noom 0\noom_kill 0\noom_group_kill 0\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write memory.events: %v", err)
		}
	}

	// Prove the watch is live by making it report a real kill, then assert the
	// pressure-only changes did not add observations of their own.
	writeEvents(t, path, 1, 0)
	waitFor(t, "the OOM kill after the pressure churn", func() bool {
		got, _ := c.last()
		return got == OOMStats{Kills: 1}
	})

	if _, count := c.last(); count != 1 {
		t.Errorf("watcher reported %d observations, want only the single OOM kill", count)
	}
}

// A sandbox can die of memory pressure while it is still starting up, before the
// watch is attached. The cgroup is created for this launch, so a tally that is
// already non-zero is this sandbox's and must be reported rather than treated as
// a baseline to count from - that case is precisely the one with no other signal.
func TestWatchOOMReportsKillsThatPrecededTheWatch(t *testing.T) {
	dir := t.TempDir()
	writeEvents(t, filepath.Join(dir, "memory.events"), 4, 0)

	var c collector
	w, err := WatchOOM(dir, CgroupFresh, c.record)
	if err != nil {
		t.Fatalf("WatchOOM() error = %v", err)
	}
	defer w.Stop()

	waitFor(t, "the kills that happened before the watch started", func() bool {
		got, _ := c.last()
		return got == OOMStats{Kills: 4}
	})
	if got := w.Stats(); got != (OOMStats{Kills: 4}) {
		t.Errorf("Stats() = %+v, want the kills observed at attach time", got)
	}
}

func TestWatchOOMStopIsIdempotentAndJoins(t *testing.T) {
	dir := t.TempDir()
	writeEvents(t, filepath.Join(dir, "memory.events"), 0, 0)

	w, err := WatchOOM(dir, CgroupFresh, nil)
	if err != nil {
		t.Fatalf("WatchOOM() error = %v", err)
	}
	w.Stop()
	w.Stop()
}

func TestWatchOOMFailsWhenThereIsNoMemoryEventsFile(t *testing.T) {
	_, err := WatchOOM(t.TempDir(), CgroupFresh, nil)
	if err == nil {
		t.Fatal("WatchOOM() = nil error, want a failure naming the unreadable file")
	}
	if !strings.Contains(err.Error(), "memory.events") {
		t.Errorf("WatchOOM() error = %q, want it to name memory.events", err)
	}
}

// The caller must be able to stop the wait, not just outlast it. A sandbox stays a
// member of the cgroup it was launched in until it is reaped, so a launch that
// never reached its scope keeps looking resolvable for the full timeout - and that
// wait would otherwise sit between the sandbox exiting and devsandbox exiting.
func TestScopeCgroupDirStopsWhenTheCallerCancels(t *testing.T) {
	const pid = 91
	dir := fakeProcCgroup(t)
	fakeCgroupRoot(t, filepath.Join(dir, "cgroup"))
	writeProcCgroup(t, dir, pid, "0::/user.slice/user-1000.slice/session-3.scope\n")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := ScopeCgroupDir(ctx, pid)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ScopeCgroupDir() error = %v, want context.Canceled", err)
	}
	// A cancellation must not be reported as a scope that never arrived: that
	// distinction decides whether the user is told monitoring failed.
	if errors.Is(err, ErrNoOwnCgroup) {
		t.Errorf("ScopeCgroupDir() error = %v, want it kept distinct from ErrNoOwnCgroup", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("ScopeCgroupDir() took %s to notice the cancellation", elapsed)
	}
}

// A container kept between sessions keeps its cgroup, so the kills already counted
// there belong to whatever ran before. Reporting them would mark a fresh session
// as OOM-killed the moment it starts.
func TestWatchOOMReusedCgroupCountsFromAttachTime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.events")
	writeEvents(t, path, 4, 0)

	var c collector
	w, err := WatchOOM(dir, CgroupReused, c.record)
	if err != nil {
		t.Fatalf("WatchOOM() error = %v", err)
	}
	defer w.Stop()

	// Give the initial read a chance to be (wrongly) reported before proving it
	// was not, then confirm only the new kill lands.
	writeEvents(t, path, 6, 0)
	waitFor(t, "the kills that happened after the watch attached", func() bool {
		got, _ := c.last()
		return got == OOMStats{Kills: 2}
	})
	if _, count := c.last(); count != 1 {
		t.Errorf("watcher reported %d observations, want only the kills after it attached", count)
	}
}

// The container ID is the anchor: a PID that belongs to a remote daemon or to the
// VM behind Docker Desktop resolves to an unrelated local process, whose OOM
// counters would otherwise be reported as this sandbox's.
func TestContainerCgroupDirRequiresTheContainerID(t *testing.T) {
	const (
		pid = 3131
		id  = "9f2c1de4a7b8c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e"
	)

	tests := []struct {
		name        string
		procContent string
		containerID string
		wantErr     bool
	}{
		{
			name:        "systemd cgroup driver",
			procContent: "0::/system.slice/docker-" + id + ".scope\n",
			containerID: id,
		},
		{
			name:        "cgroupfs cgroup driver",
			procContent: "0::/docker/" + id + "\n",
			containerID: id,
		},
		{
			name:        "rootless podman",
			procContent: "0::/user.slice/user-1000.slice/user@1000.service/user.slice/libpod-" + id + ".scope/container\n",
			containerID: id,
		},
		{
			name:        "an unrelated local process is refused",
			procContent: "0::/user.slice/user-1000.slice/session-3.scope\n",
			containerID: id,
			wantErr:     true,
		},
		{
			name:        "another container is refused",
			procContent: "0::/system.slice/docker-1111111111111111111111111111111111111111111111111111111111111111.scope\n",
			containerID: id,
			wantErr:     true,
		},
		{
			name:        "no container ID to check against is refused",
			procContent: "0::/system.slice/docker-" + id + ".scope\n",
			containerID: "",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := fakeProcCgroup(t)
			fakeCgroupRoot(t, filepath.Join(dir, "cgroup"))
			writeProcCgroup(t, dir, pid, tt.procContent)

			got, err := ContainerCgroupDir(pid, tt.containerID)
			if tt.wantErr {
				if !errors.Is(err, ErrNoOwnCgroup) {
					t.Fatalf("ContainerCgroupDir() error = %v, want ErrNoOwnCgroup", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ContainerCgroupDir() error = %v", err)
			}
			if !strings.Contains(got, tt.containerID) {
				t.Errorf("ContainerCgroupDir() = %q, want it to resolve under the container's cgroup", got)
			}
		})
	}
}

func TestContainerCgroupDirReportsAGoneProcess(t *testing.T) {
	dir := fakeProcCgroup(t)
	fakeCgroupRoot(t, filepath.Join(dir, "cgroup"))

	_, err := ContainerCgroupDir(4321, "deadbeef")
	if !errors.Is(err, ErrProcessGone) {
		t.Fatalf("ContainerCgroupDir() error = %v, want ErrProcessGone", err)
	}
}
