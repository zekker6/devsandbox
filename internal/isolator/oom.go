package isolator

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"devsandbox/internal/cgroups"
	"devsandbox/internal/logging"
	"devsandbox/internal/notice"
	"devsandbox/internal/sandbox"
)

// oomScopeTimeout bounds the wait for systemd to migrate a launch into its
// transient scope, a local D-Bus round trip that normally completes in tens of
// milliseconds.
const oomScopeTimeout = 5 * time.Second

// oomContainerTimeout bounds the wait for a container to report the PID its cgroup
// is resolved from. It is generous because a krun guest boots first, and it costs
// nothing to be: finish cancels the attach, so a container that never starts is
// never waited for.
const oomContainerTimeout = 90 * time.Second

// sigkillExitStatus is how a SIGKILL of a process inside the sandbox reaches us:
// bwrap, pasta and sh each report a killed child as 128 plus the signal number
// rather than dying of the signal themselves.
const sigkillExitStatus = 137

// errNoLimits reports that no resource limits were configured, so no transient
// scope was created and the sandbox shares the invoking process' cgroup. It is
// separate from cgroups.ErrNoOwnCgroup because the two need different treatment:
// this one is the expected state of an unconfigured launch, while that one means
// a scope was asked for and the sandbox never landed in it.
var errNoLimits = errors.New("no resource limits are configured, so the sandbox has no cgroup of its own")

// errNoContainerName reports that the launch produced an anonymous container. It
// is the shape of a `docker run --rm` with keep_container disabled: with no name
// the engine cannot be asked for the container's PID or ID, so there is nothing to
// anchor a cgroup to. Like errNoLimits, this is a launch that never could be
// monitored rather than a watch that failed.
var errNoContainerName = errors.New("the container is anonymous, so the engine cannot be asked for its cgroup")

// oomMonitor turns OOM kills into something the user can see. Nothing else does:
// the kernel kills the victim silently, a killed sandbox cannot report on itself,
// and its session file is removed on exit - which is the whole reason the record
// goes into the sandbox metadata that `sandboxes list` reads.
//
// A monitor is always usable, including when the watch could not be attached. In
// that state it still classifies how the sandbox ended, which is the one signal
// that needs no cgroup.
type oomMonitor struct {
	dispatcher  *logging.Dispatcher
	sandboxRoot string
	memoryLimit string
	limited     bool

	// cancel abandons an attach still waiting for the scope, and attached is
	// closed once the attach attempt is over. finish uses both so devsandbox never
	// waits on the attach after the sandbox it was for has exited.
	cancel   context.CancelFunc
	attached chan struct{}
	// watcher is written by the attach goroutine and read by finish only after
	// attached is closed, which is the barrier between the two.
	watcher *cgroups.OOMWatcher
}

// startOOMMonitor attaches an OOM watch to a launched sandbox process, in the
// background.
//
// It returns a monitor even when there is nothing to watch, rather than nil: a nil
// monitor reports nothing, and for a sandbox with no limits the report finish
// produces - it exited on SIGKILL and devsandbox cannot say whether an OOM killer
// did it - is the only signal that case ever gets.
//
// The attach has to wait for systemd to migrate the launch into its scope, and it
// deliberately does not do that on the caller's goroutine. A sandbox stays a
// member of the cgroup it was launched in until it is reaped, so a launch that
// never reaches its scope stays unresolvable for the whole timeout - which, waited
// on inline, would sit between the sandbox exiting and devsandbox exiting. Nothing
// about monitoring may delay either.
//
// The cost is a window at startup during which a kill is not yet watched for. It
// is small in practice, and WatchOOM reports the counters it finds at attach time
// rather than treating them as a baseline, so a kill inside that window is still
// reported as long as the cgroup still exists when the watch lands.
func startOOMMonitor(cfg *RunConfig, limits cgroups.Limits, pid int) *oomMonitor {
	m := newOOMMonitor(cfg, limits.Memory, !limits.IsZero())

	if limits.IsZero() {
		m.skipAttach(errNoLimits)
		return m
	}

	m.attach(oomScopeTimeout, cgroups.CgroupFresh, func(ctx context.Context) (string, error) {
		return cgroups.ScopeCgroupDir(ctx, pid)
	})
	return m
}

// startContainerOOMMonitor is startOOMMonitor for the container backends, where
// the cgroup belongs to the container rather than to a transient scope.
//
// resolve is given the container's PID and full ID, which only the engine can
// answer for and only once the container is running - so it is a callback, and it
// runs on the attach goroutine like the scope lookup does.
//
// The cgroup is CgroupReused because a container kept between sessions
// (keep_container, the default) keeps its cgroup, and with it the OOM counters of
// whatever ran in it before.
func startContainerOOMMonitor(cfg *RunConfig, memoryLimit string, resolve func(context.Context) (pid int, containerID string, err error)) *oomMonitor {
	m := newOOMMonitor(cfg, memoryLimit, true)

	m.attach(oomContainerTimeout, cgroups.CgroupReused, func(ctx context.Context) (string, error) {
		pid, id, err := resolve(ctx)
		if err != nil {
			return "", err
		}
		return cgroups.ContainerCgroupDir(pid, id)
	})
	return m
}

func newOOMMonitor(cfg *RunConfig, memoryLimit string, limited bool) *oomMonitor {
	return &oomMonitor{
		dispatcher:  cfg.LogDispatcher,
		sandboxRoot: cfg.SandboxCfg.SandboxRoot,
		memoryLimit: memoryLimit,
		limited:     limited,
		cancel:      func() {},
		attached:    make(chan struct{}),
	}
}

// skipAttach records that there was never a cgroup to watch, leaving the monitor
// usable for the exit classification alone.
func (m *oomMonitor) skipAttach(reason error) {
	close(m.attached)
	warnOOMWatchUnavailable(reason)
}

// attach resolves the cgroup and starts the watch, in the background and bounded
// by timeout. finish cancels it, so neither the launch nor the exit ever waits on
// a cgroup that is not going to appear.
func (m *oomMonitor) attach(timeout time.Duration, own cgroups.Ownership, resolve func(context.Context) (string, error)) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	m.cancel = cancel

	go func() {
		defer close(m.attached)
		defer cancel()

		dir, err := resolve(ctx)
		if err != nil {
			warnOOMWatchUnavailable(err)
			return
		}
		watcher, err := cgroups.WatchOOM(dir, own, m.observed)
		if err != nil {
			warnOOMWatchUnavailable(err)
			return
		}
		m.watcher = watcher
	}()
}

// monitored reports whether the watch is actually attached, and so whether the
// absence of OOM counters means anything. Only call it after the attach is over.
func (m *oomMonitor) monitored() bool { return m.watcher != nil }

// warnOOMWatchUnavailable tells the user when a sandbox that could have been
// monitored is not.
//
// It stays quiet about the states that are not failures: a launch with no limits
// has no cgroup of its own and never could be attributed (errNoLimits), an
// anonymous container cannot be asked about at all (errNoContainerName), a sandbox
// that finished before its scope resolved has nothing left to watch
// (ErrProcessGone), a sandbox that ended while the attach was still in flight
// cancelled it on the way out (context.Canceled), and a platform without cgroups
// cannot do this at all (ErrOOMUnsupported). Everything else - a scope that was
// asked for and never arrived, an unreadable memory.events, an inotify watch that
// could not be added - is reported, because the alternative is a user believing a
// sandbox is monitored when it is not.
func warnOOMWatchUnavailable(err error) {
	if oomWatchExpectedlyUnavailable(err) {
		return
	}
	// Alert, not Warn: this is emitted after the workload starts, and a plain
	// warning is diverted to the log file from that point on - which would leave
	// the message that says "this sandbox is not monitored" invisible in exactly
	// the case it exists for.
	notice.Alert("OOM kills will not be reported for this sandbox: %v", err)
}

// oomWatchExpectedlyUnavailable reports whether err is one of the states in which
// there was never anything to watch, as opposed to a watch that should have worked.
func oomWatchExpectedlyUnavailable(err error) bool {
	switch {
	case err == nil,
		errors.Is(err, errNoLimits),
		errors.Is(err, errNoContainerName),
		errors.Is(err, cgroups.ErrProcessGone),
		errors.Is(err, cgroups.ErrOOMUnsupported),
		errors.Is(err, context.Canceled):
		return true
	}
	return false
}

// observed reports a kill seen while the sandbox is still running. The counters
// are cumulative for the session, so this overwrites rather than accumulates.
func (m *oomMonitor) observed(stats cgroups.OOMStats) {
	if m == nil {
		return
	}
	notice.Alert("OOM kill: the kernel killed %s inside the sandbox%s. The sandbox is still running, but whatever was killed is gone.",
		processCount(stats.Kills), m.limitSuffix())
	m.record(stats, false)
}

// finish stops the watch and reports how the sandbox ended.
//
// Three outcomes are distinguished, and the difference matters to the user: the
// sandbox itself was OOM-killed, processes inside it were killed while it carried
// on, or it was killed by something this host cannot attribute.
//
// A nil monitor is a launch that never got as far as starting a process to
// monitor, and has nothing to report.
func (m *oomMonitor) finish(waitErr error) {
	if m == nil {
		return
	}

	// Abandon an attach that is still waiting for a scope the sandbox never
	// reached, then join it: whatever it has to say belongs before this report,
	// and nothing may outlive the launch it was watching.
	m.cancel()
	<-m.attached

	m.watcher.Stop()
	stats := m.watcher.Stats()

	switch classifyOutcome(stats, classifyKill(waitErr), m.monitored()) {
	case outcomeFatalOOM:
		notice.Alert("OOM kill: the sandbox itself was killed by the kernel%s. See 'devsandbox sandboxes list' for the record.", m.limitSuffix())
		m.record(stats, true)
	case outcomeSurvivedOOM:
		// Already reported as it happened, and the record is written. The sandbox
		// outliving the kill is the whole distinction being drawn here.
	case outcomeUnattributableKill:
		m.reportUnattributableKill(waitErr)
	case outcomeNothing:
	}
}

// oomOutcome is how a session ended, as far as OOM activity goes.
type oomOutcome int

const (
	// outcomeNothing is a session with no OOM activity and no sign of a kill.
	outcomeNothing oomOutcome = iota
	// outcomeSurvivedOOM is an OOM kill inside a sandbox that carried on running.
	outcomeSurvivedOOM
	// outcomeFatalOOM is an OOM kill that took the sandbox with it.
	outcomeFatalOOM
	// outcomeUnattributableKill is a sandbox that died on SIGKILL with no OOM
	// counter to explain it.
	outcomeUnattributableKill
)

// classifyOutcome combines what the cgroup recorded with how the sandbox exited.
//
// Neither input settles it alone. Counters without a kill mean the sandbox lost a
// process and kept going, which is the common case under a memory limit and reads
// very differently to the user than losing the sandbox. A kill without counters
// cannot be called an OOM at all - on a launch with no cgroup of its own there is
// nothing that could distinguish the OOM killer from any other `kill -9`.
func classifyOutcome(stats cgroups.OOMStats, kill killStatus, monitored bool) oomOutcome {
	switch {
	case stats.Any() && kill != killNone:
		return outcomeFatalOOM
	case stats.Any():
		return outcomeSurvivedOOM
	case kill != killNone && !monitored:
		return outcomeUnattributableKill
	default:
		// A kill with no counters from a cgroup that was being watched is simply
		// not an OOM - a `kill -9` from elsewhere - and needs no report of its own,
		// any more than a shell explains a killed child.
		return outcomeNothing
	}
}

// record persists the observation so it outlives the process, and emits the audit
// event. A metadata write failure is surfaced: it is the only part of the report
// that is still there once the terminal output has scrolled away.
func (m *oomMonitor) record(stats cgroups.OOMStats, fatal bool) {
	at := time.Now()

	if m.dispatcher != nil {
		_ = m.dispatcher.Event(logging.LevelWarn, "sandbox.oom", map[string]any{
			"oom_kills":       stats.Kills,
			"oom_group_kills": stats.GroupKills,
			"fatal":           fatal,
			"memory_limit":    m.memoryLimit,
		})
	}

	if err := sandbox.RecordOOM(m.sandboxRoot, stats.Kills, fatal, at); err != nil {
		notice.Warn("could not record the OOM kill in the sandbox metadata: %v", err)
	}
}

// reportUnattributableKill covers a sandbox that died on SIGKILL with no watch
// attached to say whether an OOM killer did it. It deliberately does not claim
// one: with no cgroup of its own the sandbox shares the invoking process'
// memory.events, whose counters move for every unrelated sibling in it, so
// there is nothing here that could tell an OOM kill from any other `kill -9`.
// Saying so is still worth doing - today such a sandbox simply vanishes.
//
// The remedy differs by why there was no watch, and offering the wrong one is
// worse than offering none: telling someone who already configured a limit to
// configure a limit reads as a bug in the message.
func (m *oomMonitor) reportUnattributableKill(waitErr error) {
	const lead = "the sandbox exited on SIGKILL. devsandbox cannot tell whether the kernel OOM killer did it: "
	if m.limited {
		notice.Alert(lead + "OOM monitoring could not be attached to this sandbox, for the reason given above.")
	} else {
		notice.Alert(lead + "that needs the sandbox to run in a cgroup of its own, which happens only when [sandbox.resources] limits are configured.")
	}

	if m.dispatcher != nil {
		_ = m.dispatcher.Event(logging.LevelWarn, "sandbox.killed", map[string]any{
			"signal":       "SIGKILL",
			"exit_code":    exitStatusOf(waitErr),
			"direct":       classifyKill(waitErr) == killDirect,
			"attributable": false,
		})
	}
}

// limitSuffix names the configured memory limit when there is one, so the message
// distinguishes a sandbox that hit its own cap from one caught by host-wide
// memory pressure.
func (m *oomMonitor) limitSuffix() string {
	if m.memoryLimit == "" {
		return " (no memory limit configured, so this was host-wide memory pressure)"
	}
	return fmt.Sprintf(" (memory limit %s)", m.memoryLimit)
}

func processCount(n int) string {
	if n == 1 {
		return "1 process"
	}
	return fmt.Sprintf("%d processes", n)
}

// killStatus describes how a sandbox's exit relates to a SIGKILL.
type killStatus int

const (
	// killNone means the exit carries no sign of a SIGKILL.
	killNone killStatus = iota
	// killPropagated is exit status 137: a SIGKILL that reached us through the
	// wrapper chain rather than as a wait status. It is indistinguishable from a
	// workload that chose to exit 137, which is why it is kept separate.
	killPropagated
	// killDirect means the process devsandbox waited on was itself SIGKILLed.
	killDirect
)

// classifyKill inspects a wait error for evidence of a SIGKILL.
//
// Both forms have to be recognised because the victim is rarely the process
// devsandbox waits on: an OOM killer picks the largest process in the cgroup,
// which is something running inside bwrap, and bwrap, pasta and sh all turn a
// killed child into exit status 137. Only when devsandbox waited on the victim
// itself does a signal wait status come back.
func classifyKill(err error) killStatus {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return killNone
	}
	if status, ok := ee.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() == syscall.SIGKILL {
		return killDirect
	}
	if ee.ExitCode() == sigkillExitStatus {
		return killPropagated
	}
	return killNone
}

// exitStatusOf returns the process exit status behind a wait error, or -1 when
// there is none to report.
func exitStatusOf(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
