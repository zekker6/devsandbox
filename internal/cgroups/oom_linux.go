//go:build linux

package cgroups

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// scopePollInterval is how often /proc/<pid>/cgroup is re-read while waiting for
// systemd to migrate the launch into its transient scope. The wait is a local
// D-Bus round trip that normally completes in tens of milliseconds.
const scopePollInterval = 25 * time.Millisecond

// procPIDCgroup names the cgroup membership file of a pid. It is a variable so
// tests can point the lookup at a fake procfs.
var procPIDCgroup = func(pid int) string {
	return fmt.Sprintf("/proc/%d/cgroup", pid)
}

// ScopeCgroupDir returns the cgroup v2 directory of the transient scope this
// process asked systemd to create, once pid has been migrated into it. It waits
// until ctx is done, because the scope is created over D-Bus and the caller has
// the pid before that round trip finishes.
//
// ctx must carry a deadline or be cancellable by the caller. A sandbox can exit
// while still a member of the cgroup it was launched in - it stays one until it is
// reaped - so a launch that never reaches its scope keeps answering the question
// with the wrong cgroup for as long as it is asked. Nothing here can shorten that,
// which is why the caller keeps the ability to stop waiting.
//
// Only call this for a launch that carries limits: without them Wrap creates no
// scope, so the wait can only ever time out.
//
// The scope is identified by name rather than by position. systemd chooses which
// slice a user scope lands in, so a path assembled from the user manager cgroup
// plus a conventional slice would be a guess that breaks silently. Matching
// unitName is exact - if the basename is our unit, the cgroup is the one Wrap
// asked for, wherever systemd put it - and it is what makes the counters
// attributable at all. A sandbox that never reached its own scope yields
// ErrNoOwnCgroup rather than the enclosing cgroup, whose memory.events counts
// OOM kills of unrelated sibling processes.
func ScopeCgroupDir(ctx context.Context, pid int) (string, error) {
	want := unitName() + ".scope"

	// One ticker for the whole wait rather than a timer per iteration, so the poll
	// allocates nothing after the first tick.
	ticker := time.NewTicker(scopePollInterval)
	defer ticker.Stop()

	for {
		rel, err := pidCgroup(pid)
		if err != nil {
			return "", err
		}
		if filepath.Base(rel) == want {
			return filepath.Join(cgroupRoot, filepath.FromSlash(rel)), nil
		}

		select {
		case <-ctx.Done():
			// A caller that gave up is not the same as a scope that never
			// arrived: the first happens on every sandbox that outlives its
			// launch, the second means a limit is in place and unobservable.
			if errors.Is(ctx.Err(), context.Canceled) {
				return "", fmt.Errorf("%w while waiting for the %s transient scope", ctx.Err(), want)
			}
			return "", fmt.Errorf("%w: expected the %s transient scope, found %s", ErrNoOwnCgroup, want, rel)
		case <-ticker.C:
		}
	}
}

// ContainerCgroupDir returns the cgroup v2 directory of the container whose main
// process is pid, verified to be that container's own cgroup.
//
// containerID is the full container ID, and it is required because pid is not
// something this host necessarily knows anything about. A container engine reports
// a PID from wherever its daemon runs: a remote DOCKER_HOST, or the VM behind
// Docker Desktop. Reading /proc/<pid>/cgroup for such a PID does not fail - it
// silently resolves some unrelated local process, whose memory.events would then be
// reported as the sandbox's OOM kills.
//
// Every cgroup driver in use puts the full container ID in the path
// (`docker-<id>.scope`, `libpod-<id>.scope/container`, `/docker/<id>`), so
// requiring it to appear there is what ties the cgroup to the container the engine
// was asked about. A path without it is refused as not the container's own.
func ContainerCgroupDir(pid int, containerID string) (string, error) {
	if containerID == "" {
		return "", fmt.Errorf("%w: no container ID to verify the cgroup against", ErrNoOwnCgroup)
	}

	rel, err := pidCgroup(pid)
	if err != nil {
		return "", err
	}
	if !strings.Contains(rel, containerID) {
		return "", fmt.Errorf("%w: cgroup %s does not belong to container %s (a PID from another host or VM resolves to an unrelated local process)",
			ErrNoOwnCgroup, rel, containerID)
	}
	return filepath.Join(cgroupRoot, filepath.FromSlash(rel)), nil
}

// pidCgroup returns the cgroup v2 path of a pid, relative to the unified mount
// point, from the 0:: line of its /proc/<pid>/cgroup.
func pidCgroup(pid int) (string, error) {
	path := procPIDCgroup(pid)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: %s is gone", ErrProcessGone, path)
		}
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "0::"); ok {
			return rest, nil
		}
	}
	return "", fmt.Errorf("%s has no cgroup v2 (0::) entry, so this host is using cgroup v1", path)
}

// WatchOOM starts watching dir's memory.events and reports every observed OOM
// kill to onOOM. The returned watcher keeps the latest observation available via
// Stats and must be stopped by the caller.
//
// own says whether the counters already in dir belong to the launch being
// watched; see Ownership. Getting it wrong either credits an earlier launch's
// kills to this one or discards a kill this launch suffered before the watch
// attached, so it is a required argument rather than an assumption.
//
// The watch is driven by inotify, not by polling, and that is what makes a fatal
// OOM observable at all: the kernel notifies from the OOM path itself while the
// victim is still dying, whereas the cgroup - and with it memory.events - is
// removed a few milliseconds after the last process in it exits. Any poll
// interval slow enough to be free would lose that race, and one fast enough to
// win it would not be.
//
// An error means the watch never started, and the caller is expected to say so
// rather than continue believing a sandbox is monitored. Degrading to a slower
// mechanism here would report fewer OOMs than promised while looking identical.
func WatchOOM(dir string, own Ownership, onOOM func(OOMStats)) (*OOMWatcher, error) {
	path := filepath.Join(dir, "memory.events")

	initial, err := readMemoryEvents(path)
	if err != nil {
		return nil, err
	}

	notify, err := watchModify(path)
	if err != nil {
		return nil, err
	}

	w := &OOMWatcher{
		done: make(chan struct{}),
		stop: func() { _ = notify.Close() },
	}
	go w.run(notify, path, own.baseline(initial), initial, onOOM)
	return w, nil
}

// run reports each OOM kill observed through the notify handle until the handle
// is closed by Stop or the cgroup it watches goes away.
//
// The inotify payload is deliberately never parsed: a single watch on a single
// file has nothing to disambiguate, so any readable byte means "memory.events
// changed, read it again". The file also changes for its low/high/max pressure
// counters, which is why an unchanged OOM tally is not reported.
func (w *OOMWatcher) run(notify *os.File, path string, base, initial OOMStats, onOOM func(OOMStats)) {
	defer close(w.done)

	var last OOMStats
	report := func(cur OOMStats) {
		observed := since(base, cur)
		if observed == last || !observed.Any() {
			return
		}
		last = observed
		w.setStats(observed)
		if onOOM != nil {
			onOOM(observed)
		}
	}
	report(initial)

	buf := make([]byte, 4096)
	for {
		if _, err := notify.Read(buf); err != nil {
			return
		}
		cur, err := readMemoryEvents(path)
		if err != nil {
			// The cgroup was removed under us, which happens on every normal
			// exit. Whatever was observed before this point stays in Stats.
			return
		}
		report(cur)
	}
}

func readMemoryEvents(path string) (OOMStats, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return OOMStats{}, fmt.Errorf("read %s: %w", path, err)
	}
	return parseMemoryEvents(string(data)), nil
}

// watchModify returns a handle that becomes readable whenever path is modified.
//
// The inotify descriptor is opened non-blocking and handed to os.NewFile so the
// Go runtime polls it: that makes the reading goroutine cancellable by closing
// the file, instead of leaving it blocked in a raw read that only a signal or a
// racy descriptor close could interrupt.
func watchModify(path string) (*os.File, error) {
	fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("create inotify watch for %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), "inotify:"+path)
	if _, err := syscall.InotifyAddWatch(fd, path, syscall.IN_MODIFY); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("watch %s for modifications: %w", path, err)
	}
	return f, nil
}
