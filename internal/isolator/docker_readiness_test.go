package isolator

import (
	"strings"
	"testing"
	"time"
)

// fakeEngineExecFailsInspect builds a fake engine whose readiness probe
// (`exec ... test -f`) always fails, and whose `inspect` prints the given
// "<running> <exitcode>" state line. Any other subcommand fails.
func fakeEngineExecFailsInspect(t *testing.T, state string) string {
	t.Helper()
	body := `case "$1" in
  exec) exit 1 ;;
  inspect) echo "` + state + `"; exit 0 ;;
  *) exit 1 ;;
esac`
	return writeFakeEngine(t, body)
}

// TestWaitForContainerReady_SentinelPresent verifies the happy path: the probe
// succeeding ends the wait immediately.
func TestWaitForContainerReady_SentinelPresent(t *testing.T) {
	bin := writeFakeEngine(t, `if [ "$1" = "exec" ]; then exit 0; fi; exit 1`)

	if err := newFakeEngineIsolator(bin).waitForContainerReady(bin, "devsandbox-test", 5*time.Second); err != nil {
		t.Errorf("waitForContainerReady with sentinel present = %v, want nil", err)
	}
}

// TestWaitForContainerReady_ExitedContainerFailsFast verifies a container that
// died during setup is reported as soon as it is observed, rather than costing
// the caller the full readiness timeout. A crashing shim (e.g. an unreadable
// overlay manifest) previously stalled the launch for the whole 90s.
func TestWaitForContainerReady_ExitedContainerFailsFast(t *testing.T) {
	bin := fakeEngineExecFailsInspect(t, "false 1")

	const timeout = 10 * time.Second
	start := time.Now()
	err := newFakeEngineIsolator(bin).waitForContainerReady(bin, "devsandbox-test", timeout)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("waitForContainerReady on an exited container = nil, want an error")
	}
	if !strings.Contains(err.Error(), "exited with code 1") {
		t.Errorf("error = %q, want it to report the container exit code", err)
	}
	if elapsed >= timeout/2 {
		t.Errorf("waitForContainerReady took %s, want a fast fail well under the %s timeout", elapsed, timeout)
	}
}

// TestWaitForContainerReady_RunningContainerWaits verifies a still-running
// container is not mistaken for a dead one: a slow but healthy boot must keep
// polling until the sentinel appears or the timeout elapses.
func TestWaitForContainerReady_RunningContainerWaits(t *testing.T) {
	bin := fakeEngineExecFailsInspect(t, "true 0")

	err := newFakeEngineIsolator(bin).waitForContainerReady(bin, "devsandbox-test", 300*time.Millisecond)
	if err == nil {
		t.Fatal("waitForContainerReady = nil, want a timeout error")
	}
	if !strings.Contains(err.Error(), "did not become ready") {
		t.Errorf("error = %q, want the timeout error for a running container", err)
	}
}

// TestWaitForContainerReady_UnreadableStateWaits verifies an inspect failure is
// not treated as an exit. An unreadable state is not evidence the container
// died, so the wait must fall through to its own timeout.
func TestWaitForContainerReady_UnreadableStateWaits(t *testing.T) {
	bin := writeFakeEngine(t, `exit 1`)

	err := newFakeEngineIsolator(bin).waitForContainerReady(bin, "devsandbox-test", 300*time.Millisecond)
	if err == nil {
		t.Fatal("waitForContainerReady = nil, want a timeout error")
	}
	if !strings.Contains(err.Error(), "did not become ready") {
		t.Errorf("error = %q, want the timeout error when state is unreadable", err)
	}
}

// TestContainerExitCode verifies state parsing across the cases the readiness
// loop depends on.
func TestContainerExitCode(t *testing.T) {
	tests := []struct {
		name       string
		state      string
		wantCode   int
		wantExited bool
	}{
		{name: "exited nonzero", state: "false 137", wantCode: 137, wantExited: true},
		{name: "exited zero", state: "false 0", wantCode: 0, wantExited: true},
		{name: "still running", state: "true 0", wantExited: false},
		{name: "unparseable", state: "<no value>", wantExited: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin := fakeEngineExecFailsInspect(t, tt.state)
			code, exited := newFakeEngineIsolator(bin).containerExitCode(bin, "devsandbox-test")
			if exited != tt.wantExited {
				t.Fatalf("containerExitCode(%q) exited = %v, want %v", tt.state, exited, tt.wantExited)
			}
			if exited && code != tt.wantCode {
				t.Errorf("containerExitCode(%q) code = %d, want %d", tt.state, code, tt.wantCode)
			}
		})
	}
}
