package isolator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"devsandbox/internal/cgroups"
	"devsandbox/internal/sandbox"
)

// killedProcessError runs a helper and returns the wait error it produced, so the
// classification below is exercised against real wait statuses rather than
// hand-built ones - the two forms of a SIGKILL are a property of the kernel and
// the shell, not of anything this package could simulate faithfully.
func killedProcessError(t *testing.T, script string) error {
	t.Helper()
	err := exec.Command("/bin/sh", "-c", script).Run()
	if err == nil {
		t.Fatalf("helper %q exited 0, want a failure", script)
	}
	return err
}

func TestClassifyKill(t *testing.T) {
	t.Run("a process killed by SIGKILL is recognised directly", func(t *testing.T) {
		// The shell kills itself, so the wait status carries the signal.
		err := killedProcessError(t, "kill -9 $$")
		if got := classifyKill(err); got != killDirect {
			t.Errorf("classifyKill() = %v, want killDirect", got)
		}
	})

	// This is the shape an OOM kill inside the sandbox actually takes: the victim
	// is a process under bwrap, and bwrap, pasta and sh all report a killed child
	// as status 137 rather than dying of the signal.
	t.Run("a propagated SIGKILL is recognised from status 137", func(t *testing.T) {
		err := killedProcessError(t, "exit 137")
		if got := classifyKill(err); got != killPropagated {
			t.Errorf("classifyKill() = %v, want killPropagated", got)
		}
	})

	t.Run("an ordinary failure is not a kill", func(t *testing.T) {
		err := killedProcessError(t, "exit 3")
		if got := classifyKill(err); got != killNone {
			t.Errorf("classifyKill() = %v, want killNone", got)
		}
	})

	t.Run("another signal is not a kill", func(t *testing.T) {
		err := killedProcessError(t, "kill -TERM $$")
		if got := classifyKill(err); got != killNone {
			t.Errorf("classifyKill() = %v, want killNone", got)
		}
	})

	t.Run("a clean exit is not a kill", func(t *testing.T) {
		if got := classifyKill(nil); got != killNone {
			t.Errorf("classifyKill(nil) = %v, want killNone", got)
		}
	})

	t.Run("a setup failure is not a kill", func(t *testing.T) {
		if got := classifyKill(errors.New("failed to build sandbox")); got != killNone {
			t.Errorf("classifyKill() = %v, want killNone", got)
		}
	})
}

func TestExitStatusOf(t *testing.T) {
	if got := exitStatusOf(killedProcessError(t, "exit 3")); got != 3 {
		t.Errorf("exitStatusOf() = %d, want 3", got)
	}
	if got := exitStatusOf(nil); got != -1 {
		t.Errorf("exitStatusOf(nil) = %d, want -1", got)
	}
	if got := exitStatusOf(errors.New("boom")); got != -1 {
		t.Errorf("exitStatusOf() on a non-exit error = %d, want -1", got)
	}
}

// A launch with no limits has no cgroup of its own, so the monitor must say so
// rather than watch a cgroup shared with unrelated processes.
func TestStartOOMMonitorRefusesASharedCgroup(t *testing.T) {
	cfg := &RunConfig{SandboxCfg: &sandbox.Config{SandboxRoot: t.TempDir()}}

	m := startOOMMonitor(cfg, cgroups.Limits{}, os.Getpid())
	if m == nil {
		t.Fatal("startOOMMonitor() returned no monitor; it must still classify how the sandbox ended")
	}
	<-m.attached
	if m.monitored() {
		t.Error("startOOMMonitor() attached a watcher to a cgroup it does not own")
	}
}

// Nothing about monitoring may sit between the sandbox exiting and devsandbox
// exiting. The scope resolution waits up to oomScopeTimeout, and a sandbox stays a
// member of the cgroup it was launched in until it is reaped - so a launch that
// never reaches its scope stays unresolvable for that whole window. finish must
// abandon it, not wait it out.
func TestFinishDoesNotWaitForAnUnresolvableScope(t *testing.T) {
	cfg := &RunConfig{SandboxCfg: &sandbox.Config{SandboxRoot: t.TempDir()}}

	// This process is alive and in a cgroup that is not the transient scope
	// startOOMMonitor is looking for, so the resolution keeps retrying.
	m := startOOMMonitor(cfg, cgroups.Limits{Memory: "512m"}, os.Getpid())

	start := time.Now()
	m.finish(nil)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("finish() took %s to give up on the attach, want it abandoned promptly (timeout is %s)", elapsed, oomScopeTimeout)
	}
}

// startOOMMonitor is called on the launch path, before the sandbox is waited on,
// so it must not block there either.
func TestStartOOMMonitorDoesNotBlockTheLaunch(t *testing.T) {
	cfg := &RunConfig{SandboxCfg: &sandbox.Config{SandboxRoot: t.TempDir()}}

	start := time.Now()
	m := startOOMMonitor(cfg, cgroups.Limits{Memory: "512m"}, os.Getpid())
	elapsed := time.Since(start)
	t.Cleanup(func() { m.finish(nil) })

	if elapsed > time.Second {
		t.Errorf("startOOMMonitor() blocked for %s, want it to attach in the background", elapsed)
	}
}

// The unattributable states are silent by design: a launch with no limits, and a
// sandbox that exited before its cgroup could be resolved, are both normal. A
// watch that should have worked is not - a user who is told nothing will assume
// the sandbox is monitored.
func TestOOMWatchUnavailableIsOnlyQuietForExpectedStates(t *testing.T) {
	quiet := []error{
		nil,
		errNoLimits,
		cgroups.ErrProcessGone,
		cgroups.ErrOOMUnsupported,
		fmt.Errorf("resolve the scope: %w", cgroups.ErrProcessGone),
		// finish cancels an attach still in flight when the sandbox ends, which is
		// every sandbox shorter-lived than its own scope creation.
		fmt.Errorf("%w while waiting for the scope", context.Canceled),
	}
	for _, err := range quiet {
		if !oomWatchExpectedlyUnavailable(err) {
			t.Errorf("oomWatchExpectedlyUnavailable(%v) = false, want silence", err)
		}
	}

	loud := []error{
		cgroups.ErrNoOwnCgroup,
		errors.New("watch /sys/fs/cgroup/x/memory.events: no such file or directory"),
	}
	for _, err := range loud {
		if oomWatchExpectedlyUnavailable(err) {
			t.Errorf("oomWatchExpectedlyUnavailable(%v) = true, want it reported", err)
		}
	}
}

// Neither the cgroup counters nor the exit status settles what happened on its
// own, and getting the combination wrong is harmful in both directions: it either
// tells the user their sandbox was killed when it merely lost a child process, or
// claims an OOM on a host where nothing could attribute one.
func TestClassifyOutcome(t *testing.T) {
	tests := []struct {
		name      string
		stats     cgroups.OOMStats
		kill      killStatus
		monitored bool
		want      oomOutcome
	}{
		{
			name:      "a clean session reports nothing",
			monitored: true,
			want:      outcomeNothing,
		},
		{
			name:      "counters with a clean exit mean the sandbox survived",
			stats:     cgroups.OOMStats{Kills: 1},
			kill:      killNone,
			monitored: true,
			want:      outcomeSurvivedOOM,
		},
		{
			name:      "counters with a propagated kill mean the sandbox went down",
			stats:     cgroups.OOMStats{Kills: 1},
			kill:      killPropagated,
			monitored: true,
			want:      outcomeFatalOOM,
		},
		{
			name:      "counters with a direct kill mean the sandbox went down",
			stats:     cgroups.OOMStats{Kills: 2},
			kill:      killDirect,
			monitored: true,
			want:      outcomeFatalOOM,
		},
		{
			name:      "a group kill counts as OOM activity",
			stats:     cgroups.OOMStats{GroupKills: 1},
			kill:      killDirect,
			monitored: true,
			want:      outcomeFatalOOM,
		},
		{
			name: "a kill on an unmonitored sandbox is reported without claiming an OOM",
			kill: killDirect,
			want: outcomeUnattributableKill,
		},
		{
			// The cgroup answered: this was a `kill -9` from elsewhere, and a
			// shell does not explain a killed child either.
			name:      "a kill a watched cgroup recorded no OOM for is left alone",
			kill:      killDirect,
			monitored: true,
			want:      outcomeNothing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyOutcome(tt.stats, tt.kill, tt.monitored); got != tt.want {
				t.Errorf("classifyOutcome() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The record has to survive the process that observed it, because the listing is
// the only place a killed sandbox can still be explained.
func TestRecordPersistsForTheListing(t *testing.T) {
	root := seedSandboxMetadata(t)
	m := &oomMonitor{sandboxRoot: root, memoryLimit: "512m"}

	m.record(cgroups.OOMStats{Kills: 2}, true)

	meta, err := sandbox.LoadMetadata(root)
	if err != nil {
		t.Fatalf("LoadMetadata() error = %v", err)
	}
	if meta.LastOOM == nil {
		t.Fatal("no OOM record was written")
	}
	if meta.LastOOM.Kills != 2 || !meta.LastOOM.Fatal {
		t.Errorf("record = %+v, want 2 fatal kills", *meta.LastOOM)
	}
	if got := meta.LastOOM.Status(); got != "oom-killed" {
		t.Errorf("listing status = %q, want oom-killed", got)
	}
}

func seedSandboxMetadata(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	m := &sandbox.Metadata{
		Name:       "oom-test",
		ProjectDir: root,
		CreatedAt:  time.Now(),
		LastUsed:   time.Now(),
		Shell:      sandbox.ShellBash,
	}
	if err := sandbox.SaveMetadata(m, root); err != nil {
		t.Fatalf("SaveMetadata() error = %v", err)
	}
	return root
}
