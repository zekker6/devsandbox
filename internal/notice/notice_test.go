// internal/notice/notice_test.go
package notice

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type captured struct {
	level Level
	msg   string
	ts    time.Time
}

// captureSink builds a sink that appends into a slice owned by the caller.
// Returns a function that snapshots the captured entries.
func captureSink() (snapshot func() []captured, sink SinkFunc) {
	var (
		mu sync.Mutex
		c  []captured
	)
	sink = func(level Level, msg string, ts time.Time) {
		mu.Lock()
		defer mu.Unlock()
		c = append(c, captured{level: level, msg: msg, ts: ts})
	}
	snapshot = func() []captured {
		mu.Lock()
		defer mu.Unlock()
		out := make([]captured, len(c))
		copy(out, c)
		return out
	}
	return snapshot, sink
}

func resetForTest(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		setPhase(PhaseStartup)
		state.mu.Lock()
		state.stderr = os.Stderr
		state.logFile = nil
		state.logPath = ""
		state.verbose = false
		state.buffer = nil
		state.sink = nil
		state.droppedCount = 0
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

func TestAttachSink_DrainsBufferInOrder(t *testing.T) {
	resetForTest(t)
	if err := Setup(filepath.Join(t.TempDir(), "w.log"), false, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}

	Info("first")
	Warn("second")
	Error("third")

	snap, sink := captureSink()
	dropped := AttachSink(sink)

	if dropped != 0 {
		t.Errorf("dropped = %d, want 0", dropped)
	}
	got := snap()
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3", len(got))
	}
	if got[0].msg != "first" || got[1].msg != "second" || got[2].msg != "third" {
		t.Errorf("buffer drain order wrong: %+v", got)
	}
	if got[0].level != LevelInfo || got[1].level != LevelWarn || got[2].level != LevelError {
		t.Errorf("buffer drain levels wrong: %+v", got)
	}
}

func TestAttachSink_DispatchesLiveAfterAttach(t *testing.T) {
	resetForTest(t)
	if err := Setup(filepath.Join(t.TempDir(), "w.log"), false, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}

	snap, sink := captureSink()
	AttachSink(sink)

	Info("after-attach")
	got := snap()
	if len(got) != 1 || got[0].msg != "after-attach" {
		t.Fatalf("live dispatch missing or wrong: %+v", got)
	}
}

func TestAttachSink_OverflowReturnsDroppedCount(t *testing.T) {
	resetForTest(t)
	if err := Setup(filepath.Join(t.TempDir(), "w.log"), false, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}

	// Emit maxBuffered + 5 entries before any sink is attached.
	for i := range maxBuffered + 5 {
		Info("msg-%d", i)
	}

	snap, sink := captureSink()
	dropped := AttachSink(sink)

	if dropped != 5 {
		t.Errorf("dropped = %d, want 5", dropped)
	}
	got := snap()
	if len(got) != maxBuffered {
		t.Errorf("snapshot length = %d, want %d", len(got), maxBuffered)
	}
	// After overflow drops oldest, the first surviving entry is msg-5.
	if got[0].msg != "msg-5" {
		t.Errorf("oldest survivor = %q, want msg-5", got[0].msg)
	}
}

func TestAttachSink_NilSinkIsNoop(t *testing.T) {
	resetForTest(t)
	if err := Setup(filepath.Join(t.TempDir(), "w.log"), false, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	Info("buffered")

	dropped := AttachSink(nil)
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0 from nil-sink call", dropped)
	}
	// Buffer must still be retained after nil-sink call.
	state.mu.Lock()
	bufLen := len(state.buffer)
	state.mu.Unlock()
	if bufLen != 1 {
		t.Errorf("buffer length after nil AttachSink = %d, want 1", bufLen)
	}
}

func TestSetup_ResetsAuditState(t *testing.T) {
	resetForTest(t)

	// First "session": attach a sink and emit something live, then overflow
	// the buffer in a non-attached state to populate dropped count.
	tmp := t.TempDir()
	if err := Setup(filepath.Join(tmp, "w.log"), false, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}

	snap1, sink1 := captureSink()
	AttachSink(sink1)
	Info("live-1")
	if got := len(snap1()); got != 1 {
		t.Fatalf("snap1 len = %d, want 1", got)
	}

	// Re-Setup (simulating a process that re-initialises notice). Buffer,
	// sink, and dropped count must all clear so a stale sink doesn't keep
	// receiving entries from a new "session."
	if err := Setup(filepath.Join(tmp, "w.log"), false, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}

	Info("post-reset")

	// snap1 must NOT have grown — old sink is detached.
	if got := len(snap1()); got != 1 {
		t.Errorf("snap1 grew after Setup reset: len=%d, want 1", got)
	}

	// Buffer received the new entry; AttachSink with a fresh sink drains it.
	snap2, sink2 := captureSink()
	dropped := AttachSink(sink2)
	if dropped != 0 {
		t.Errorf("dropped = %d after Setup reset, want 0", dropped)
	}
	got2 := snap2()
	if len(got2) != 1 || got2[0].msg != "post-reset" {
		t.Errorf("snap2 = %+v, want one entry with msg=post-reset", got2)
	}
}

func TestFlushWithoutSink_DispatchPathIsNoop(t *testing.T) {
	resetForTest(t)
	var buf bytes.Buffer
	if err := Setup(filepath.Join(t.TempDir(), "w.log"), false, &buf); err != nil {
		t.Fatal(err)
	}
	SetRunning()
	Info("hidden")
	Flush()
	// Pre-existing semantics preserved: stderr banner mentions log path.
	if !strings.Contains(buf.String(), "wrapper messages") {
		t.Errorf("flush banner missing without sink; got %q", buf.String())
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
