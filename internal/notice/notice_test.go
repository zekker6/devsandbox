// internal/notice/notice_test.go
package notice

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func resetForTest(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		setPhase(PhaseStartup)
		state.mu.Lock()
		state.stderr = os.Stderr
		state.logFile = nil
		state.logPath = ""
		state.verbose = false
		state.mu.Unlock()
	})
}

func TestStartupPhaseWritesToStderr(t *testing.T) {
	resetForTest(t)
	var buf bytes.Buffer
	logPath := filepath.Join(t.TempDir(), "w.log")
	if err := Setup(logPath, false, &buf); err != nil {
		t.Fatal(err)
	}
	Info("hello %s", "world")
	if !strings.Contains(buf.String(), "hello world") {
		t.Fatalf("stderr buffer = %q", buf.String())
	}
}

func TestRunningPhaseSuppressesStderr(t *testing.T) {
	resetForTest(t)
	var buf bytes.Buffer
	logPath := filepath.Join(t.TempDir(), "w.log")
	if err := Setup(logPath, false, &buf); err != nil {
		t.Fatal(err)
	}
	SetRunning()
	Info("secret")
	if buf.Len() != 0 {
		t.Fatalf("stderr leaked during running phase: %q", buf.String())
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "secret") {
		t.Fatalf("log missing message; got %q", data)
	}
}

func TestVerboseOverride(t *testing.T) {
	resetForTest(t)
	var buf bytes.Buffer
	logPath := filepath.Join(t.TempDir(), "w.log")
	if err := Setup(logPath, true, &buf); err != nil {
		t.Fatal(err)
	}
	SetRunning()
	Info("loud")
	if !strings.Contains(buf.String(), "loud") {
		t.Fatalf("verbose override failed: %q", buf.String())
	}
}

func TestTeardownPhaseWritesToStderr(t *testing.T) {
	resetForTest(t)
	var buf bytes.Buffer
	logPath := filepath.Join(t.TempDir(), "w.log")
	if err := Setup(logPath, false, &buf); err != nil {
		t.Fatal(err)
	}
	SetRunning()
	Info("during")
	SetTeardown()
	Info("after")
	if !strings.Contains(buf.String(), "after") {
		t.Fatalf("teardown write missing: %q", buf.String())
	}
	if strings.Contains(buf.String(), "during") {
		t.Fatalf("running-phase message leaked to stderr: %q", buf.String())
	}
}

func TestFlushEmitsBannerOnSuppressedWrites(t *testing.T) {
	resetForTest(t)
	var buf bytes.Buffer
	logPath := filepath.Join(t.TempDir(), "w.log")
	if err := Setup(logPath, false, &buf); err != nil {
		t.Fatal(err)
	}
	SetRunning()
	Warn("hidden")
	buf.Reset()
	Flush()
	if !strings.Contains(buf.String(), logPath) {
		t.Fatalf("flush banner missing log path; got %q", buf.String())
	}
}

func TestConcurrentWritesSafe(t *testing.T) {
	resetForTest(t)
	logPath := filepath.Join(t.TempDir(), "w.log")
	if err := Setup(logPath, false, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	SetRunning()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			Info("msg-%d", i)
		}(i)
	}
	wg.Wait()
	// If this test is not race-free, `go test -race` will fail.
}
