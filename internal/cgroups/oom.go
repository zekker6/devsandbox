package cgroups

import (
	"errors"
	"strconv"
	"strings"
	"sync"
)

// ErrOOMUnsupported reports that OOM monitoring cannot run on this platform. It
// reads cgroup v2 memory.events, which exists only on Linux.
var ErrOOMUnsupported = errors.New("OOM monitoring reads cgroup v2 memory.events and is only supported on Linux")

// ErrNoOwnCgroup reports that the sandbox did not end up in a cgroup of its own,
// so OOM kills cannot be attributed to it. This is the state of every launch with
// no configured limits: without a transient scope the sandbox stays in the
// invoking process' cgroup, whose memory.events counts OOM kills of every
// unrelated sibling process in it - a terminal's whole session, typically.
var ErrNoOwnCgroup = errors.New("the sandbox has no cgroup of its own, so OOM kills cannot be attributed to it")

// ErrProcessGone reports that the process whose cgroup was being resolved exited
// first. A sandbox that finishes faster than systemd migrates it into its scope is
// not a monitoring failure worth telling the user about, so it is a distinct error
// rather than folded into the others.
var ErrProcessGone = errors.New("the sandbox process exited before its cgroup could be resolved")

// OOMStats are the cgroup v2 memory.events counters that record OOM activity.
// They are cumulative over the lifetime of the cgroup they were read from, which
// is why a watcher is only ever pointed at a cgroup created for one launch: the
// counters then describe that launch and nothing else.
type OOMStats struct {
	// Kills is memory.events' oom_kill: the number of processes in the cgroup
	// killed by an OOM killer. It covers both the cgroup hitting its own
	// memory.max and the host-wide OOM killer picking a process in it, which is
	// why a sandbox with limits also gets a signal for global memory pressure.
	Kills int
	// GroupKills is memory.events' oom_group_kill: the number of times the whole
	// cgroup was killed as one unit. That needs memory.oom.group set, which
	// systemd transient scopes do not set, so it is normally zero and is tracked
	// only so a host that does set it is not misreported as having no kills.
	GroupKills int
}

// Any reports whether either counter recorded an OOM kill.
func (s OOMStats) Any() bool { return s.Kills > 0 || s.GroupKills > 0 }

// Ownership tells a watcher whether the counters already in a cgroup belong to
// the launch being watched. It has no default on purpose: guessing wrong is a
// misreport in one direction or the other, and only the caller knows which cgroup
// it handed over.
type Ownership int

const (
	// CgroupFresh means the cgroup was created for this launch, so a counter that
	// is already non-zero records a kill that happened between its creation and
	// the watch attaching - a sandbox dying of memory pressure while it starts up,
	// which has no other signal. Those counters are reported as they are read.
	CgroupFresh Ownership = iota
	// CgroupReused means the cgroup outlives the launch, as a container kept
	// between sessions does. Counters found at attach time belong to whatever ran
	// before and are taken as the baseline; only increases beyond it are reported.
	CgroupReused
)

// baseline returns the counters a watch starts from under this ownership.
func (o Ownership) baseline(initial OOMStats) OOMStats {
	if o == CgroupReused {
		return initial
	}
	return OOMStats{}
}

// since returns cur relative to base, clamped at zero: a cgroup that was removed
// and recreated under the same path would otherwise produce a negative count.
func since(base, cur OOMStats) OOMStats {
	return OOMStats{
		Kills:      max(cur.Kills-base.Kills, 0),
		GroupKills: max(cur.GroupKills-base.GroupKills, 0),
	}
}

// OOMWatcher observes OOM kills in a sandbox's own cgroup. Every method is safe
// on a nil receiver, so a caller that could not start one - an unsupported host,
// a sandbox with no cgroup of its own - needs no nil checks around it.
type OOMWatcher struct {
	mu    sync.Mutex
	stats OOMStats
	stop  func()
	done  chan struct{}
}

// Stats returns the counters as of the watcher's last observation. Call it after
// Stop to be sure every observation is included.
func (w *OOMWatcher) Stats() OOMStats {
	if w == nil {
		return OOMStats{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stats
}

// Stop ends the watch and waits for the watching goroutine to finish, so a
// caller reading Stats afterwards cannot race with an in-flight observation.
func (w *OOMWatcher) Stop() {
	if w == nil || w.stop == nil {
		return
	}
	w.stop()
	<-w.done
}

// setStats records an observation.
func (w *OOMWatcher) setStats(s OOMStats) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stats = s
}

// parseMemoryEvents reads the OOM counters out of a cgroup v2 memory.events
// file. Unknown keys are ignored and an unparseable line is skipped rather than
// failing the whole read: the file is flat-keyed, gains keys across kernel
// versions, and a single bad line must not discard the counters that did parse.
func parseMemoryEvents(data string) OOMStats {
	var s OOMStats
	for line := range strings.SplitSeq(data, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			continue
		}
		switch key {
		case "oom_kill":
			s.Kills = n
		case "oom_group_kill":
			s.GroupKills = n
		}
	}
	return s
}
