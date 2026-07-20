package isolator

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeEngine creates an executable shell script standing in for the
// container engine binary (docker/podman) so startupDiagnostics can be exercised
// without a real engine on the test host. The script body runs after a `#!/bin/sh`
// shebang and may branch on "$1" (the subcommand, e.g. "logs").
func writeFakeEngine(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-engine")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write fake engine: %v", err)
	}
	return path
}

func newFakeEngineIsolator(binary string) *DockerIsolator {
	return &DockerIsolator{engine: containerEngine{binary: binary, backend: BackendDocker}}
}

// TestStartupDiagnostics_ReturnsLogTail verifies the shim's fatal output (written
// to the container's stderr) is captured and returned, trimmed, so a startup
// timeout can surface the real cause instead of a bare deadline.
func TestStartupDiagnostics_ReturnsLogTail(t *testing.T) {
	const shimErr = "devsandbox-shim: fatal: read overlay manifest: permission denied"
	bin := writeFakeEngine(t, `if [ "$1" = "logs" ]; then echo "`+shimErr+`" >&2; exit 0; fi; exit 1`)

	got := newFakeEngineIsolator(bin).startupDiagnostics("devsandbox-test")
	if got != shimErr {
		t.Errorf("startupDiagnostics = %q, want %q", got, shimErr)
	}
}

// TestStartupDiagnostics_EmptyLogsReturnEmpty verifies a clean `logs` call with
// no output yields "" (nothing to surface), not a blank-but-present string.
func TestStartupDiagnostics_EmptyLogsReturnEmpty(t *testing.T) {
	bin := writeFakeEngine(t, `if [ "$1" = "logs" ]; then exit 0; fi; exit 1`)

	if got := newFakeEngineIsolator(bin).startupDiagnostics("devsandbox-test"); got != "" {
		t.Errorf("startupDiagnostics on empty logs = %q, want \"\"", got)
	}
}

// TestStartupDiagnostics_UnresolvableBinaryReturnsEmpty verifies a failed engine
// invocation with no output is swallowed: the helper is best-effort and must
// never mask the original timeout error with a lookup failure.
func TestStartupDiagnostics_UnresolvableBinaryReturnsEmpty(t *testing.T) {
	d := newFakeEngineIsolator("/nonexistent/devsandbox-test-engine")
	if got := d.startupDiagnostics("devsandbox-test"); got != "" {
		t.Errorf("startupDiagnostics with unresolvable binary = %q, want \"\"", got)
	}
}

// TestStartupDiagnostics_FailWithOutputStillReturned verifies that when `logs`
// itself fails but prints something (e.g. "no such container"), that output is
// still surfaced - it is informative about why the container is gone.
func TestStartupDiagnostics_FailWithOutputStillReturned(t *testing.T) {
	bin := writeFakeEngine(t, `if [ "$1" = "logs" ]; then echo "Error: no such container" >&2; exit 1; fi; exit 1`)

	got := newFakeEngineIsolator(bin).startupDiagnostics("devsandbox-test")
	if !strings.Contains(got, "no such container") {
		t.Errorf("startupDiagnostics = %q, want it to contain engine error output", got)
	}
}

// TestWithStartupDiagnostics_AppendsLogsPreservingWrap verifies the wrapped base
// error stays matchable via errors.Is while the container log tail is appended
// to the message for the user.
func TestWithStartupDiagnostics_AppendsLogsPreservingWrap(t *testing.T) {
	const shimErr = "devsandbox-shim: fatal: read overlay manifest: permission denied"
	bin := writeFakeEngine(t, `if [ "$1" = "logs" ]; then echo "`+shimErr+`" >&2; exit 0; fi; exit 1`)
	d := newFakeEngineIsolator(bin)

	base := errors.New("container setup timed out after 90s")
	got := d.withStartupDiagnostics(base, "devsandbox-test")

	if !errors.Is(got, base) {
		t.Errorf("withStartupDiagnostics result must wrap the base error, got: %v", got)
	}
	msg := got.Error()
	if !strings.Contains(msg, "container setup timed out after 90s") {
		t.Errorf("result should retain the base timeout message, got: %q", msg)
	}
	if !strings.Contains(msg, shimErr) {
		t.Errorf("result should include the container log tail, got: %q", msg)
	}
}

// TestWithStartupDiagnostics_NoLogsReturnsBaseErr verifies the base error is
// returned untouched (same identity) when no diagnostics can be gathered.
func TestWithStartupDiagnostics_NoLogsReturnsBaseErr(t *testing.T) {
	d := newFakeEngineIsolator("/nonexistent/devsandbox-test-engine")
	base := errors.New("container setup timed out after 90s")

	if got := d.withStartupDiagnostics(base, "devsandbox-test"); got != base {
		t.Errorf("withStartupDiagnostics with no logs = %v, want the base error unchanged", got)
	}
}
