package procstate

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// reapedPID returns the PID of a process that has exited and been waited for,
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

func TestAlive_Self(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Error("expected the test process itself to be alive")
	}
}

func TestAlive_Reaped(t *testing.T) {
	if Alive(reapedPID(t)) {
		t.Error("expected a reaped process to be reported dead")
	}
}

func TestAlive_NonPositive(t *testing.T) {
	for _, pid := range []int{0, -1, math.MinInt} {
		if Alive(pid) {
			t.Errorf("Alive(%d) = true, want false", pid)
		}
	}
}

func TestAlive_PID1AnswersEPERM(t *testing.T) {
	// PID 1 always exists and is owned by root, so the signal-0 probe answers
	// EPERM for an unprivileged test run. That must not read as dead. Under
	// root the probe simply succeeds, so the test would pass without exercising
	// the EPERM path; skip rather than report a check that did not run.
	if err := syscall.Kill(1, 0); !errors.Is(err, syscall.EPERM) {
		t.Skipf("pid 1 does not answer EPERM here (kill(1, 0) = %v): root, or own pid namespace", err)
	}
	if !Alive(1) {
		t.Error("expected PID 1 to be reported alive")
	}
}

func TestProbeSeam(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want State
	}{
		{name: "no error", err: nil, want: Live},
		{name: "EPERM", err: syscall.EPERM, want: Unknown},
		{name: "ESRCH", err: syscall.ESRCH, want: Dead},
		{name: "ErrProcessDone", err: os.ErrProcessDone, want: Dead},
		{name: "wrapped ESRCH", err: fmt.Errorf("probe: %w", syscall.ESRCH), want: Dead},
		{name: "wrapped ErrProcessDone", err: fmt.Errorf("probe: %w", os.ErrProcessDone), want: Dead},
		{name: "arbitrary error", err: errors.New("something unexpected"), want: Unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var probed int
			got := probe(42, func(pid int) error {
				probed = pid
				return tt.err
			})
			if got != tt.want {
				t.Errorf("probe = %v, want %v", got, tt.want)
			}
			if probed != 42 {
				t.Errorf("probe received pid %d, want 42", probed)
			}
			// Alive folds Live and Unknown together; only Dead is dead.
			if wantAlive := tt.want != Dead; (got != Dead) != wantAlive {
				t.Errorf("alive = %v, want %v", got != Dead, wantAlive)
			}
		})
	}
}

func TestProbeSeam_NonPositiveNeverProbes(t *testing.T) {
	for _, pid := range []int{0, -1, math.MinInt} {
		got := probe(pid, func(probed int) error {
			t.Errorf("probe(%d) signalled pid %d; a non-positive pid must be dead before any signal", pid, probed)
			return nil
		})
		if got != Dead {
			t.Errorf("probe(%d) = %v, want Dead", pid, got)
		}
	}
}

// The tri-state answer is what the age backstops key on: a pid that answers
// EPERM is Unknown, not Live, so a location may reclaim it by age without
// reclaiming state whose owner is provably running.
func TestProbe_SelfIsLiveAndPID1IsUnknown(t *testing.T) {
	if got := Probe(os.Getpid()); got != Live {
		t.Errorf("Probe(self) = %v, want Live", got)
	}
	if got := Probe(reapedPID(t)); got != Dead {
		t.Errorf("Probe(reaped) = %v, want Dead", got)
	}
	if err := syscall.Kill(1, 0); !errors.Is(err, syscall.EPERM) {
		t.Skipf("pid 1 does not answer EPERM here (kill(1, 0) = %v): root, or own pid namespace", err)
	}
	if got := Probe(1); got != Unknown {
		t.Errorf("Probe(1) = %v, want Unknown", got)
	}
}
