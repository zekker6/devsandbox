package kittyproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeUpstream listens on a UDS and replies with canned responses keyed by cmd.
type fakeUpstream struct {
	listener net.Listener
	mu       sync.Mutex
	replies  map[string][]byte // cmd -> response body
	received [][]byte          // raw payloads received
}

func newFakeUpstream(t *testing.T, sockPath string) *fakeUpstream {
	t.Helper()
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	u := &fakeUpstream{listener: l, replies: map[string][]byte{}}
	go u.serve()
	return u
}

func (u *fakeUpstream) Reply(cmd string, body []byte) {
	u.mu.Lock()
	u.replies[cmd] = body
	u.mu.Unlock()
}

func (u *fakeUpstream) Received() [][]byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([][]byte, len(u.received))
	copy(out, u.received)
	return out
}

func (u *fakeUpstream) serve() {
	for {
		conn, err := u.listener.Accept()
		if err != nil {
			return
		}
		go u.handle(conn)
	}
}

func (u *fakeUpstream) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	payload, err := ReadFrame(r)
	if err != nil {
		return
	}
	u.mu.Lock()
	u.received = append(u.received, payload)
	var c command
	_ = json.Unmarshal(payload, &c)
	reply := u.replies[c.Cmd]
	u.mu.Unlock()
	if reply == nil {
		reply = []byte(`{"ok":true,"data":null}`)
	}
	_ = WriteFrame(conn, reply)
}

func (u *fakeUpstream) Close() { _ = u.listener.Close() }

type recordingLogger struct {
	mu      sync.Mutex
	records []string
}

func (l *recordingLogger) LogErrorf(component, format string, args ...any) {
	l.mu.Lock()
	l.records = append(l.records, "ERR "+component+" "+fmtMsg(format, args))
	l.mu.Unlock()
}
func (l *recordingLogger) LogInfof(component, format string, args ...any) {
	l.mu.Lock()
	l.records = append(l.records, "INF "+component+" "+fmtMsg(format, args))
	l.mu.Unlock()
}
func (l *recordingLogger) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.records))
	copy(out, l.records)
	return out
}

func fmtMsg(format string, args []any) string {
	// minimal sprintf substitute that just concatenates
	out := format
	for _, a := range args {
		out += " " + jsonOrString(a)
	}
	return out
}
func jsonOrString(a any) string {
	b, err := json.Marshal(a)
	if err != nil {
		return ""
	}
	return string(b)
}

// roundTrip sends one DCS frame to the proxy and returns the response payload.
func roundTrip(t *testing.T, sockPath string, payload []byte) []byte {
	t.Helper()
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := WriteFrame(conn, payload); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	r := bufio.NewReader(conn)
	resp, err := ReadFrame(r)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp
}

func TestProxy_AllowsAndForwards(t *testing.T) {
	dir := t.TempDir()
	upstreamPath := filepath.Join(dir, "upstream.sock")
	listenPath := filepath.Join(dir, "proxy.sock")

	up := newFakeUpstream(t, upstreamPath)
	defer up.Close()
	up.Reply("launch", []byte(`{"ok":true,"data":42}`))

	logger := &recordingLogger{}
	owned := NewOwnedSet()
	filter := NewFilter(FilterConfig{
		Capabilities:   []Capability{CapLaunchOverlay},
		LaunchPatterns: []CommandPattern{{Program: "revdiff", ArgsMatcher: MatchAny()}},
		Owned:          owned,
	})
	p := New(upstreamPath, listenPath, filter, owned)
	p.SetLogger(logger)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Stop() }()

	cmd, _ := json.Marshal(map[string]any{
		"cmd": "launch",
		"payload": map[string]any{
			"type": "overlay",
			"args": []string{"revdiff", "a"},
		},
	})

	resp := roundTrip(t, listenPath, cmd)
	if string(resp) != `{"ok":true,"data":42}` {
		t.Errorf("response = %s", resp)
	}
	if !owned.Contains(42) {
		t.Error("expected owned set to contain id 42 after launch")
	}
	if len(up.Received()) != 1 {
		t.Errorf("upstream received %d frames", len(up.Received()))
	}
}

func TestProxy_DeniesAndLogs(t *testing.T) {
	dir := t.TempDir()
	upstreamPath := filepath.Join(dir, "upstream.sock")
	listenPath := filepath.Join(dir, "proxy.sock")

	up := newFakeUpstream(t, upstreamPath)
	defer up.Close()

	logger := &recordingLogger{}
	owned := NewOwnedSet()
	filter := NewFilter(FilterConfig{
		Capabilities:   []Capability{CapLaunchOverlay},
		LaunchPatterns: []CommandPattern{{Program: "revdiff", ArgsMatcher: MatchAny()}},
		Owned:          owned,
	})
	p := New(upstreamPath, listenPath, filter, owned)
	p.SetLogger(logger)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Stop() }()

	cmd, _ := json.Marshal(map[string]any{
		"cmd": "launch",
		"payload": map[string]any{
			"type": "overlay",
			"args": []string{"sh", "-c", "curl evil"},
		},
	})

	resp := roundTrip(t, listenPath, cmd)
	var r kittyResponse
	if err := json.Unmarshal(resp, &r); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if r.OK {
		t.Errorf("expected ok=false, got %s", resp)
	}
	if r.Error == "" {
		t.Errorf("expected error message, got %s", resp)
	}

	// Upstream must NOT have received the frame.
	time.Sleep(20 * time.Millisecond)
	if got := len(up.Received()); got != 0 {
		t.Errorf("upstream received %d frames; want 0", got)
	}

	// Logger must have a deny record.
	found := false
	for _, rec := range logger.all() {
		if contains(rec, "deny") || contains(rec, "denied") {
			found = true
		}
	}
	if !found {
		t.Errorf("no deny log line found: %v", logger.all())
	}
}

func TestProxy_OwnershipEnablesClose(t *testing.T) {
	dir := t.TempDir()
	upstreamPath := filepath.Join(dir, "upstream.sock")
	listenPath := filepath.Join(dir, "proxy.sock")

	up := newFakeUpstream(t, upstreamPath)
	defer up.Close()
	up.Reply("launch", []byte(`{"ok":true,"data":7}`))
	up.Reply("close-window", []byte(`{"ok":true,"data":null}`))

	logger := &recordingLogger{}
	owned := NewOwnedSet()
	filter := NewFilter(FilterConfig{
		Capabilities: []Capability{CapLaunchOverlay, CapCloseOwned},
		LaunchPatterns: []CommandPattern{
			{Program: "revdiff", ArgsMatcher: MatchAny()},
		},
		Owned: owned,
	})
	p := New(upstreamPath, listenPath, filter, owned)
	p.SetLogger(logger)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Stop() }()

	launchCmd, _ := json.Marshal(map[string]any{
		"cmd": "launch",
		"payload": map[string]any{
			"type": "overlay",
			"args": []string{"revdiff"},
		},
	})
	_ = roundTrip(t, listenPath, launchCmd)

	// Now close-window with the owned id should succeed.
	closeOwned, _ := json.Marshal(map[string]any{
		"cmd":     "close-window",
		"payload": map[string]any{"match": "id:7"},
	})
	resp := roundTrip(t, listenPath, closeOwned)
	var r kittyResponse
	_ = json.Unmarshal(resp, &r)
	if !r.OK {
		t.Errorf("expected ok=true for owned close, got %s", resp)
	}

	// And close-window with a non-owned id should be denied.
	closeOther, _ := json.Marshal(map[string]any{
		"cmd":     "close-window",
		"payload": map[string]any{"match": "id:8"},
	})
	resp = roundTrip(t, listenPath, closeOther)
	r = kittyResponse{}
	_ = json.Unmarshal(resp, &r)
	if r.OK {
		t.Errorf("expected deny for non-owned id, got %s", resp)
	}
}
