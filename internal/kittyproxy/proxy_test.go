package kittyproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// shortSocketDir returns a tempdir whose path is short enough for a UNIX
// domain socket beneath it to fit within macOS's 104-byte sun_path limit.
// t.TempDir() on macOS lives under /var/folders/... and easily exceeds it.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ds")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

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

// fmtMsg renders a log line exactly as the production logger does. It used to
// be a "minimal sprintf substitute" that concatenated the arguments and applied
// no verbs, which meant every log assertion tested the substitute rather than
// the format string - and the `%q` quoting that stops sandbox-chosen argv from
// forging whole `allow cmd=…` records could not be observed at all.
func fmtMsg(format string, args []any) string {
	return fmt.Sprintf(format, args...)
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
	dir := shortSocketDir(t)
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
	received := up.Received()
	if len(received) != 1 {
		t.Fatalf("upstream received %d frames", len(received))
	}
	// Byte-identical, not merely present. The whole StrictUnmarshal argument
	// rests on the premise that what was validated is what the host parses, so
	// a re-marshal anywhere in forward would break it silently.
	if !bytes.Equal(received[0], cmd) {
		t.Errorf("forwarded bytes differ from the validated request:\n got %s\nwant %s", received[0], cmd)
	}
}

func TestProxy_DeniesAndLogs(t *testing.T) {
	dir := shortSocketDir(t)
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

// startLsProxy starts a proxy permitting `ls` in front of a fake upstream whose
// `ls` reply is lsReply.
func startLsProxy(t *testing.T, lsReply []byte) (listenPath string, owned *OwnedSet, logger *recordingLogger) {
	t.Helper()
	dir := shortSocketDir(t)
	upstreamPath := filepath.Join(dir, "upstream.sock")
	listenPath = filepath.Join(dir, "proxy.sock")

	up := newFakeUpstream(t, upstreamPath)
	t.Cleanup(up.Close)
	up.Reply("ls", lsReply)

	logger = &recordingLogger{}
	owned = NewOwnedSet()
	filter := NewFilter(FilterConfig{Capabilities: []Capability{CapListOwned}, Owned: owned})
	p := New(upstreamPath, listenPath, filter, owned)
	p.SetLogger(logger)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })
	return listenPath, owned, logger
}

func lsRequest(t *testing.T) []byte {
	t.Helper()
	cmd, err := json.Marshal(map[string]any{"cmd": "ls", "payload": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}

// unfilterableLsReply carries kitty's ls envelope with `data` holding the window
// list directly instead of the JSON-encoded string kitty sends, which is what
// FilterLsResponse fails on. The title and cwd stand in for the host state a
// fall-through hands the sandbox.
const unfilterableLsReply = `{"ok":true,"data":[{"tabs":[{"windows":[{"id":99,"title":"host-secret","cwd":"/home/user/private"}]}]}]}`

func TestProxy_LsResponseUnfilterable_Denies(t *testing.T) {
	listenPath, _, _ := startLsProxy(t, []byte(unfilterableLsReply))

	resp := roundTrip(t, listenPath, lsRequest(t))

	for _, leaked := range []string{"host-secret", "/home/user/private"} {
		if contains(string(resp), leaked) {
			t.Fatalf("unfiltered upstream ls body reached the sandbox: %s", resp)
		}
	}
	var r kittyResponse
	if err := json.Unmarshal(resp, &r); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if r.OK {
		t.Errorf("expected ok=false, got %s", resp)
	}
	if !contains(r.Error, "could not be filtered") {
		t.Errorf("error should name the filter failure, got %q", r.Error)
	}
}

func TestProxy_LsResponseUnfilterable_LogsError(t *testing.T) {
	listenPath, _, logger := startLsProxy(t, []byte(unfilterableLsReply))

	_ = roundTrip(t, listenPath, lsRequest(t))

	found := false
	for _, rec := range logger.all() {
		if contains(rec, "ERR ") && contains(rec, "filter ls response") {
			found = true
		}
	}
	if !found {
		t.Errorf("no ls filter error logged: %v", logger.all())
	}
}

// The denial must not swallow the responses the filter can parse.
func TestProxy_LsResponseFilteredToOwned(t *testing.T) {
	reply := `{"ok":true,"data":"[{\"tabs\":[{\"windows\":[{\"id\":3,\"title\":\"host-secret\"},{\"id\":7,\"title\":\"mine\"}]}]}]"}`
	listenPath, owned, _ := startLsProxy(t, []byte(reply))
	owned.Add(7)

	resp := roundTrip(t, listenPath, lsRequest(t))

	var r kittyResponse
	if err := json.Unmarshal(resp, &r); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !r.OK {
		t.Fatalf("expected ok=true, got %s", resp)
	}
	if contains(string(resp), "host-secret") {
		t.Errorf("non-owned window leaked: %s", resp)
	}
	if !contains(string(resp), `\"id\":7`) {
		t.Errorf("owned window missing: %s", resp)
	}
}

func TestProxy_OwnershipEnablesClose(t *testing.T) {
	dir := shortSocketDir(t)
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
