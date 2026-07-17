package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// helperSandboxHomeEnv carries the sandbox home to the helper process below.
const helperSandboxHomeEnv = "DEVSANDBOX_TEST_HELPER_SANDBOX_HOME"

// TestHelperConcurrentSession is not a test. It is re-executed as a subprocess
// by TestConcurrentSessions_DoNotDisturbEachOther to hold a live proxy socket
// under a second PID, which is the only way to reproduce two sessions sharing
// one sandbox home.
func TestHelperConcurrentSession(t *testing.T) {
	sandboxHome := os.Getenv(helperSandboxHomeEnv)
	if sandboxHome == "" {
		t.Skip("subprocess helper; runs only when re-executed by its parent test")
	}

	d := &Docker{enabled: true, hostSocket: "/nonexistent/docker.sock"}
	if err := d.Start(context.Background(), sandboxHome, sandboxHome); err != nil {
		t.Fatalf("helper Start: %v", err)
	}
	// Stay up, holding the socket, until the parent kills us.
	time.Sleep(time.Minute)
}

// waitForFile polls until path exists, failing the test if it never appears.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

// TestConcurrentSessions_DoNotDisturbEachOther is the regression test for the
// reported failure: a second session for the same project ran, and the first
// session's socket was gone for the rest of its life. Sandbox home is keyed on
// the project, so both sessions genuinely share it here.
func TestConcurrentSessions_DoNotDisturbEachOther(t *testing.T) {
	sandboxHome := shortSocketDir(t)

	// Session one: a separate process, still running throughout.
	helper := exec.Command(os.Args[0], "-test.run=^TestHelperConcurrentSession$")
	helper.Env = append(os.Environ(), helperSandboxHomeEnv+"="+sandboxHome)
	if err := helper.Start(); err != nil {
		t.Fatalf("start helper session: %v", err)
	}
	t.Cleanup(func() {
		_ = helper.Process.Kill()
		_ = helper.Wait()
	})

	liveSocket := filepath.Join(sandboxHome, runDirName, strconv.Itoa(helper.Process.Pid), dockerSocketName)
	waitForFile(t, liveSocket)

	// Session two: this process, doing what a second session does on startup.
	if _, err := cleanupStaleRunDirs(sandboxHome); err != nil {
		t.Fatalf("cleanupStaleRunDirs: %v", err)
	}
	if _, err := os.Stat(liveSocket); err != nil {
		t.Fatalf("startup cleanup removed a live session's socket: %v", err)
	}

	d := &Docker{enabled: true, hostSocket: "/nonexistent/docker.sock"}
	if err := d.Start(context.Background(), sandboxHome, sandboxHome); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ownSocket := d.socketPath(sandboxHome)
	if ownSocket == liveSocket {
		t.Fatalf("both sessions resolved to the same socket path %q", ownSocket)
	}
	if _, err := os.Stat(liveSocket); err != nil {
		t.Fatalf("starting a second session removed a live session's socket: %v", err)
	}

	// Session two exits.
	if err := d.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := os.Stat(ownSocket); !os.IsNotExist(err) {
		t.Error("expected this session's own socket to be removed on stop")
	}
	if _, err := os.Stat(liveSocket); err != nil {
		t.Errorf("a second session exiting removed the live session's socket: %v", err)
	}
}
