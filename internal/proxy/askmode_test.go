package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"devsandbox/internal/notice"
)

// shortTempDir creates a short temp directory suitable for Unix socket paths.
// macOS limits socket paths to 104 bytes; t.TempDir() paths are too long.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ds-")
	if err != nil {
		t.Fatalf("failed to create short temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// testHelloTimeout is the hello deadline the test-side peers use. It only
// has to outlast a local socket round trip.
const testHelloTimeout = 2 * time.Second

// shortAskTimers shrinks the hello deadline and the reconnect period that
// NewAskServer captures for the duration of a test. The production values are
// seconds, and several scenarios below wait them out on purpose.
func shortAskTimers(t *testing.T) {
	t.Helper()
	oldHello, oldReconnect := askHelloTimeout, askReconnectInterval
	askHelloTimeout = 300 * time.Millisecond
	askReconnectInterval = 50 * time.Millisecond
	t.Cleanup(func() { askHelloTimeout, askReconnectInterval = oldHello, oldReconnect })
}

// fakeListener owns a Unix socket and hands every accepted connection to a
// handler on its own goroutine, standing in for whatever process holds the ask
// socket: a monitor, or another session's proxy.
type fakeListener struct {
	listener net.Listener
	accepted atomic.Int32

	mu    sync.Mutex
	conns []net.Conn
}

func listenUnix(t *testing.T, socketPath string, handle func(net.Conn)) *fakeListener {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	l, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	f := &fakeListener{listener: l}
	t.Cleanup(f.Close)

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			f.accepted.Add(1)
			f.mu.Lock()
			f.conns = append(f.conns, conn)
			f.mu.Unlock()
			go handle(conn)
		}
	}()
	return f
}

// Close stops accepting and drops every accepted connection, as a monitor
// exiting does. Closing the listener also unlinks the socket file.
func (f *fakeListener) Close() {
	_ = f.listener.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.conns {
		_ = c.Close()
	}
	f.conns = nil
}

func allowAll(req AskRequest) AskResponse {
	return AskResponse{ID: req.ID, Action: FilterActionAllow}
}

func blockAll(req AskRequest) AskResponse {
	return AskResponse{ID: req.ID, Action: FilterActionBlock}
}

// serveAsMonitor answers every request read from dec with respond until the
// connection ends.
func serveAsMonitor(dec *json.Decoder, enc *json.Encoder, respond func(AskRequest) AskResponse) {
	for {
		var req AskRequest
		if err := dec.Decode(&req); err != nil {
			return
		}
		if err := enc.Encode(respond(req)); err != nil {
			return
		}
	}
}

// monitorHandler behaves as `devsandbox proxy monitor` does on a connection it
// accepted: it requires a proxy hello, answers with its own, then serves.
func monitorHandler(respond func(AskRequest) AskResponse) func(net.Conn) {
	return func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		dec := json.NewDecoder(conn)
		enc := json.NewEncoder(conn)
		if err := askHandshake(conn, dec, enc, AskRoleMonitor, false, testHelloTimeout); err != nil {
			return
		}
		serveAsMonitor(dec, enc, respond)
	}
}

// proxyListenerHandler behaves as a server-mode proxy does on a connection it
// accepted: it requires a monitor hello and closes on anything else.
func proxyListenerHandler(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)
	if err := askHandshake(conn, dec, enc, AskRoleProxy, false, testHelloTimeout); err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, conn)
}

// dialAsMonitor dials a server-mode proxy's socket and completes the
// monitor's side of the handshake.
func dialAsMonitor(t *testing.T, socketPath string) (net.Conn, *json.Decoder, *json.Encoder) {
	t.Helper()
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("monitor dial failed: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)
	if err := askHandshake(conn, dec, enc, AskRoleMonitor, true, testHelloTimeout); err != nil {
		t.Fatalf("monitor handshake failed: %v", err)
	}
	return conn, dec, enc
}

// waitForAskMonitor blocks until server has registered a monitor connection.
func waitForAskMonitor(t *testing.T, server *AskServer) {
	t.Helper()
	waitForMonitorState(t, server, true)
}

func waitForMonitorState(t *testing.T, server *AskServer, want bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for server.HasMonitor() != want {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for HasMonitor() == %v", want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// startMonitor dials the server-mode ask socket under dir and answers every
// request with respond. It returns once the server has registered it.
func startMonitor(t *testing.T, server *AskServer, dir string, respond func(AskRequest) AskResponse) {
	t.Helper()
	_, dec, enc := dialAsMonitor(t, AskSocketPath(dir))
	go serveAsMonitor(dec, enc, respond)
	waitForAskMonitor(t, server)
}

func askOnce(t *testing.T, server *AskServer, id string) (AskResponse, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return server.Ask(ctx, &AskRequest{
		ID:     id,
		Method: "GET",
		URL:    "https://example.com",
		Host:   "example.com",
	})
}

// captureNotices routes notice output to a buffer for the test and reports
// the running phase, where only an Alert reaches the terminal.
func captureNotices(t *testing.T) *bytes.Buffer {
	t.Helper()
	var stderr bytes.Buffer
	if err := notice.Setup("", false, &stderr); err != nil {
		t.Fatalf("notice.Setup: %v", err)
	}
	t.Cleanup(func() { _ = notice.Setup("", false, nil) })
	notice.SetRunning()
	t.Cleanup(notice.SetStartup)
	return &stderr
}

func TestAskServer_ServerMode(t *testing.T) {
	dir := shortTempDir(t)

	server, err := NewAskServer(dir, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewAskServer failed: %v", err)
	}
	defer func() { _ = server.Close() }()

	if server.Mode() != AskModeServer {
		t.Errorf("expected server mode, got %s", server.Mode())
	}

	socketPath := AskSocketPath(dir)
	if _, err := os.Stat(socketPath); err != nil {
		t.Fatalf("socket should exist: %v", err)
	}
}

func TestAskServer_ClientMode(t *testing.T) {
	dir := shortTempDir(t)
	monitor := listenUnix(t, AskSocketPath(dir), monitorHandler(allowAll))

	server, err := NewAskServer(dir, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewAskServer failed: %v", err)
	}
	defer func() { _ = server.Close() }()

	if server.Mode() != AskModeClient {
		t.Errorf("expected client mode, got %s", server.Mode())
	}
	if err := server.HandshakeError(); err != nil {
		t.Errorf("handshake with a monitor must succeed, got %v", err)
	}
	if monitor.accepted.Load() != 1 {
		t.Errorf("monitor accepted %d connections, want 1", monitor.accepted.Load())
	}
}

func TestAskServer_ClientMode_Ask(t *testing.T) {
	dir := shortTempDir(t)
	listenUnix(t, AskSocketPath(dir), monitorHandler(allowAll))

	server, err := NewAskServer(dir, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewAskServer failed: %v", err)
	}
	defer func() { _ = server.Close() }()

	waitForAskMonitor(t, server)

	resp, err := askOnce(t, server, "1")
	if err != nil {
		t.Fatalf("Ask failed: %v", err)
	}
	if resp.Action != FilterActionAllow {
		t.Errorf("expected allow, got %s", resp.Action)
	}
}

func TestAskServer_StaleSocket(t *testing.T) {
	dir := shortTempDir(t)
	socketDir := AskSocketDir(dir)
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	// Create a stale socket file (not listening)
	socketPath := AskSocketPath(dir)
	staleListener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	_ = staleListener.Close() // Close immediately, leaving stale file

	server, err := NewAskServer(dir, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewAskServer failed: %v", err)
	}
	defer func() { _ = server.Close() }()

	if server.Mode() != AskModeServer {
		t.Errorf("expected server mode after stale cleanup, got %s", server.Mode())
	}
}

func TestAskQueue_SetsTimeout(t *testing.T) {
	dir := shortTempDir(t)

	receivedCh := make(chan AskRequest, 1)
	listenUnix(t, AskSocketPath(dir), monitorHandler(func(req AskRequest) AskResponse {
		receivedCh <- req
		return allowAll(req)
	}))

	server, err := NewAskServer(dir, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewAskServer failed: %v", err)
	}
	defer func() { _ = server.Close() }()

	waitForAskMonitor(t, server)

	queue := NewAskQueue(server, nil, 45*time.Second, log.New(io.Discard, "", 0))
	_, _ = queue.RequestApproval(&AskRequest{
		ID:     "timeout-test",
		Method: "GET",
		URL:    "https://example.com",
		Host:   "example.com",
	})

	select {
	case req := <-receivedCh:
		if req.Timeout != 45 {
			t.Errorf("expected Timeout=45, got %d", req.Timeout)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not receive request")
	}
}

func TestAskServer_Close_CancelsPending(t *testing.T) {
	dir := shortTempDir(t)

	server, err := NewAskServer(dir, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewAskServer failed: %v", err)
	}

	// Connect a monitor that never responds
	dialAsMonitor(t, AskSocketPath(dir))
	waitForAskMonitor(t, server)

	// Send a request in background — monitor will never respond
	errCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := server.Ask(ctx, &AskRequest{
			ID:     "pending-1",
			Method: "GET",
			URL:    "https://example.com",
			Host:   "example.com",
		})
		errCh <- err
	}()

	// Give Ask time to register the pending request
	time.Sleep(50 * time.Millisecond)

	// Close the server — should cancel the pending request immediately
	_ = server.Close()

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrNoMonitor) {
			t.Errorf("expected ErrNoMonitor, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ask did not return after Close — pending request was not cancelled")
	}
}

// The ask socket is shared by every devsandbox process of one project, and
// which side listens depends only on who started first. Before the hello, a
// proxy that found the socket live registered whatever answered as its
// monitor - a concurrent session's proxy included - and a listening proxy
// admitted whoever dialed. The tests below pin the handshake that closes that.

func TestAskHandshake_RolesMustBeOpposite(t *testing.T) {
	cases := []struct {
		name         string
		dialer       AskRole
		listener     AskRole
		wantAccepted bool
	}{
		{name: "proxy dials monitor", dialer: AskRoleProxy, listener: AskRoleMonitor, wantAccepted: true},
		{name: "monitor dials proxy", dialer: AskRoleMonitor, listener: AskRoleProxy, wantAccepted: true},
		{name: "proxy dials proxy", dialer: AskRoleProxy, listener: AskRoleProxy, wantAccepted: false},
		{name: "monitor dials monitor", dialer: AskRoleMonitor, listener: AskRoleMonitor, wantAccepted: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dialSide, listenSide := net.Pipe()
			defer func() { _ = dialSide.Close() }()
			defer func() { _ = listenSide.Close() }()

			listenerErr := make(chan error, 1)
			go func() {
				err := askHandshake(listenSide, json.NewDecoder(listenSide), json.NewEncoder(listenSide), tc.listener, false, testHelloTimeout)
				if err != nil {
					_ = listenSide.Close()
				}
				listenerErr <- err
			}()

			dialErr := askHandshake(dialSide, json.NewDecoder(dialSide), json.NewEncoder(dialSide), tc.dialer, true, testHelloTimeout)
			if (dialErr == nil) != tc.wantAccepted {
				t.Errorf("dialer: err = %v, want accepted = %v", dialErr, tc.wantAccepted)
			}
			if err := <-listenerErr; (err == nil) != tc.wantAccepted {
				t.Errorf("listener: err = %v, want accepted = %v", err, tc.wantAccepted)
			}
		})
	}
}

func TestAskHandshake_RejectsWhatIsNotAHello(t *testing.T) {
	cases := []struct {
		name  string
		first any
		want  string
	}{
		{name: "response before hello", first: AskResponse{ID: "1", Action: FilterActionAllow}, want: "hello"},
		{name: "request before hello", first: AskRequest{ID: "1", Host: "example.com"}, want: "hello"},
		{name: "unknown protocol", first: AskHello{Proto: "devsandbox-ask/0", Role: AskRoleMonitor}, want: `"devsandbox-ask/0"`},
		{name: "silent peer", first: nil, want: "no hello"},
		{name: "peer closes", first: io.EOF, want: "hello"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ours, theirs := net.Pipe()
			defer func() { _ = ours.Close() }()
			defer func() { _ = theirs.Close() }()

			go func() {
				switch first := tc.first.(type) {
				case nil:
				case error:
					_ = theirs.Close()
				default:
					_ = json.NewEncoder(theirs).Encode(first)
				}
			}()

			start := time.Now()
			err := askHandshake(ours, json.NewDecoder(ours), json.NewEncoder(ours), AskRoleProxy, false, 200*time.Millisecond)
			if err == nil {
				t.Fatal("handshake accepted a peer that sent no valid hello")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name the reason %q", err, tc.want)
			}
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Errorf("handshake took %v, must give up at the hello deadline", elapsed)
			}
		})
	}
}

// (a) A listening proxy admits a peer that identifies as a monitor and tells
// it who it is talking to.
func TestAskServer_ServerMode_AdmitsMonitorHello(t *testing.T) {
	dir := shortTempDir(t)
	server, err := NewAskServer(dir, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewAskServer failed: %v", err)
	}
	defer func() { _ = server.Close() }()

	conn, err := net.Dial("unix", AskSocketPath(dir))
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer func() { _ = conn.Close() }()
	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	if err := enc.Encode(AskHello{Proto: AskProto, Role: AskRoleMonitor}); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var hello AskHello
	if err := dec.Decode(&hello); err != nil {
		t.Fatalf("no hello back from the proxy: %v", err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	if hello.Proto != AskProto || hello.Role != AskRoleProxy {
		t.Fatalf("proxy hello = %+v, want proto %q role %q", hello, AskProto, AskRoleProxy)
	}

	waitForAskMonitor(t, server)
	go serveAsMonitor(dec, enc, allowAll)

	resp, err := askOnce(t, server, "a-1")
	if err != nil {
		t.Fatalf("Ask failed: %v", err)
	}
	if resp.Action != FilterActionAllow {
		t.Errorf("action = %q, want allow", resp.Action)
	}
}

// (b) A listening proxy closes on anything that is not a monitor hello, and
// answers nothing - so the peer never learns it reached a proxy, and never
// becomes a monitor whose answers count. The refusal is logged, because the
// peer is told nothing and a monitor from an older build otherwise just never
// receives a request.
func TestAskServer_ServerMode_RejectsPeersThatAreNotMonitors(t *testing.T) {
	shortAskTimers(t)

	cases := []struct {
		name    string
		first   any
		wantLog string
	}{
		{name: "proxy hello", first: AskHello{Proto: AskProto, Role: AskRoleProxy}, wantLog: `peer is a "proxy", not a "monitor"`},
		{name: "unknown protocol", first: AskHello{Proto: "devsandbox-ask/0", Role: AskRoleMonitor}, wantLog: `"devsandbox-ask/0"`},
		{name: "response before hello", first: AskResponse{ID: "1", Action: FilterActionAllow}, wantLog: "hello"},
		{name: "silent", first: nil, wantLog: "no hello from peer"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := shortTempDir(t)
			var logBuf syncBuffer
			server, err := NewAskServer(dir, log.New(&logBuf, "", 0))
			if err != nil {
				t.Fatalf("NewAskServer failed: %v", err)
			}
			defer func() { _ = server.Close() }()

			conn, err := net.Dial("unix", AskSocketPath(dir))
			if err != nil {
				t.Fatalf("dial failed: %v", err)
			}
			defer func() { _ = conn.Close() }()

			if tc.first != nil {
				if err := json.NewEncoder(conn).Encode(tc.first); err != nil {
					t.Fatalf("send: %v", err)
				}
			}

			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			buf := make([]byte, 64)
			n, readErr := conn.Read(buf)
			if n != 0 || !errors.Is(readErr, io.EOF) {
				t.Fatalf("proxy answered a peer it must refuse: %q, err = %v", buf[:n], readErr)
			}

			if server.HasMonitor() {
				t.Error("a refused peer was registered as a monitor")
			}
			if _, err := askOnce(t, server, "b-1"); !errors.Is(err, ErrNoMonitor) {
				t.Errorf("Ask err = %v, want ErrNoMonitor", err)
			}

			// The line is written after the connection is closed, so the EOF
			// above does not order it.
			wantPrefix := "ask mode: refused a connection on " + AskSocketPath(dir) + ": "
			deadline := time.Now().Add(3 * time.Second)
			for !strings.Contains(logBuf.String(), wantPrefix) {
				if time.Now().After(deadline) {
					t.Fatalf("refusal not logged: %q, want %q", logBuf.String(), wantPrefix)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if got := logBuf.String(); !strings.Contains(got, tc.wantLog) {
				t.Errorf("refusal log = %q, want it to name the cause %q", got, tc.wantLog)
			}
		})
	}
}

// (d) A proxy that dials a live socket whose owner does not identify as a
// monitor must not take its answers: it ends up with no monitor at all,
// says so, and leaves the socket to its owner. What it says depends on the
// cause: an owner that sent no hello is from a build that predates it, an
// owner that answered with the wrong role is a concurrent session.
func TestAskServer_ClientMode_DeadWhenSocketOwnerIsNotMonitor(t *testing.T) {
	shortAskTimers(t)

	holdOpen := func(conn net.Conn) {
		_, _ = io.Copy(io.Discard, conn)
		_ = conn.Close()
	}
	const (
		olderBuild     = "owned by a monitor or session from an older devsandbox build"
		anotherSession = "owned by another devsandbox session, not a monitor"
	)
	cases := []struct {
		name   string
		handle func(net.Conn)
		want   string
	}{
		{name: "answers with a proxy hello", handle: func(conn net.Conn) {
			_ = json.NewEncoder(conn).Encode(AskHello{Proto: AskProto, Role: AskRoleProxy})
			holdOpen(conn)
		}, want: anotherSession},
		{name: "never answers", handle: holdOpen, want: olderBuild},
		{name: "listening proxy", handle: proxyListenerHandler, want: anotherSession},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := shortTempDir(t)
			socketPath := AskSocketPath(dir)
			listenUnix(t, socketPath, tc.handle)

			server, err := NewAskServer(dir, log.New(io.Discard, "", 0))
			if err != nil {
				t.Fatalf("NewAskServer must not fail the launch: %v", err)
			}
			defer func() { _ = server.Close() }()

			if server.Mode() != AskModeClient {
				t.Fatalf("mode = %s, want client", server.Mode())
			}
			herr := server.HandshakeError()
			if herr == nil {
				t.Fatal("HandshakeError() = nil, want the refusal")
			}
			for _, want := range []string{socketPath, tc.want, "not a monitor", "devsandbox proxy monitor"} {
				if !strings.Contains(herr.Error(), want) {
					t.Errorf("HandshakeError() = %q, want it to mention %q", herr, want)
				}
			}
			if server.HasMonitor() {
				t.Error("HasMonitor() = true for a peer that is not a monitor")
			}
			if _, err := askOnce(t, server, "d-1"); !errors.Is(err, ErrNoMonitor) {
				t.Errorf("Ask err = %v, want ErrNoMonitor", err)
			}

			_ = server.Close()
			if _, err := os.Stat(socketPath); err != nil {
				t.Errorf("a proxy that lost the handshake unlinked a socket it does not own: %v", err)
			}
		})
	}
}

// (e) Two proxies on one socket path: the second finds the first's socket
// live, and neither may end up trusting the other's bytes as decisions.
func TestAskServer_TwoProxiesOneSocket_NeitherTrustsTheOther(t *testing.T) {
	shortAskTimers(t)
	dir := shortTempDir(t)

	first, err := NewAskServer(dir, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("first NewAskServer failed: %v", err)
	}
	defer func() { _ = first.Close() }()
	if first.Mode() != AskModeServer {
		t.Fatalf("first mode = %s, want server", first.Mode())
	}

	second, err := NewAskServer(dir, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("second NewAskServer failed: %v", err)
	}
	defer func() { _ = second.Close() }()
	if second.Mode() != AskModeClient {
		t.Fatalf("second mode = %s, want client", second.Mode())
	}
	if second.HandshakeError() == nil {
		t.Fatal("second proxy accepted the first as its monitor")
	}

	// Past the first's own hello deadline, the second must never have been
	// counted as a monitor.
	time.Sleep(3 * askHelloTimeout)
	if first.HasMonitor() {
		t.Error("first proxy registered the second as a monitor")
	}
	if second.HasMonitor() {
		t.Error("second proxy treats the first as a monitor")
	}
	if _, err := askOnce(t, first, "e-1"); !errors.Is(err, ErrNoMonitor) {
		t.Errorf("first Ask err = %v, want ErrNoMonitor", err)
	}
	if _, err := askOnce(t, second, "e-1"); !errors.Is(err, ErrNoMonitor) {
		t.Errorf("second Ask err = %v, want ErrNoMonitor", err)
	}
}

// (f) The reconnect loop dials the same path the monitor used to own. When a
// concurrent session's proxy has taken it in the meantime, the loop must not
// adopt that proxy as the monitor: it stops, blocks every ask, and says so on
// the terminal - the workload is running by then, so only an Alert reaches it.
func TestAskServer_ClientReconnect_DeadWhenNextListenerIsProxy(t *testing.T) {
	shortAskTimers(t)
	stderr := captureNotices(t)

	dir := shortTempDir(t)
	socketPath := AskSocketPath(dir)
	monitor := listenUnix(t, socketPath, monitorHandler(allowAll))

	server, err := NewAskServer(dir, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewAskServer failed: %v", err)
	}
	defer func() { _ = server.Close() }()
	waitForAskMonitor(t, server)
	if resp, err := askOnce(t, server, "f-1"); err != nil || resp.Action != FilterActionAllow {
		t.Fatalf("Ask before the monitor left: resp %+v, err %v", resp, err)
	}

	monitor.Close()
	waitForMonitorState(t, server, false)

	other := listenUnix(t, socketPath, proxyListenerHandler)

	deadline := time.Now().Add(3 * time.Second)
	for server.HandshakeError() == nil {
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for the reconnect to be refused")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if server.HasMonitor() {
		t.Error("HasMonitor() = true after reconnecting to a proxy")
	}
	if _, err := askOnce(t, server, "f-2"); !errors.Is(err, ErrNoMonitor) {
		t.Errorf("Ask err = %v, want ErrNoMonitor", err)
	}

	seen := other.accepted.Load()
	time.Sleep(10 * askReconnectInterval)
	if got := other.accepted.Load(); got != seen {
		t.Errorf("proxy kept re-dialing after the refusal: %d connections, was %d", got, seen)
	}

	if !strings.Contains(stderr.String(), "owned by another devsandbox session") || !strings.Contains(stderr.String(), socketPath) {
		t.Errorf("no Alert naming the socket reached the terminal: %q", stderr.String())
	}
	raised, _ := notice.Raised()
	if len(raised) != 1 || raised[0].Level != notice.LevelWarn {
		t.Errorf("raised notices = %+v, want exactly one warning", raised)
	}
}

// (g) A reconnect to a real monitor performs the handshake and resumes.
func TestAskServer_ClientReconnect_ResumesWithMonitor(t *testing.T) {
	shortAskTimers(t)
	stderr := captureNotices(t)

	dir := shortTempDir(t)
	socketPath := AskSocketPath(dir)
	monitor := listenUnix(t, socketPath, monitorHandler(allowAll))

	server, err := NewAskServer(dir, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewAskServer failed: %v", err)
	}
	defer func() { _ = server.Close() }()
	waitForAskMonitor(t, server)

	monitor.Close()
	waitForMonitorState(t, server, false)

	listenUnix(t, socketPath, monitorHandler(blockAll))
	waitForAskMonitor(t, server)

	resp, err := askOnce(t, server, "g-1")
	if err != nil {
		t.Fatalf("Ask after reconnect failed: %v", err)
	}
	if resp.Action != FilterActionBlock {
		t.Errorf("action = %q, want block from the second monitor", resp.Action)
	}
	if err := server.HandshakeError(); err != nil {
		t.Errorf("HandshakeError() = %v after a successful reconnect", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("a successful reconnect raised a notice: %q", stderr.String())
	}
}

func TestAskQueue_RequestApproval_UnknownActionBlocks(t *testing.T) {
	cases := []struct {
		name   string
		action FilterAction
		want   FilterAction
	}{
		{name: "empty", action: "", want: FilterActionBlock},
		{name: "yes", action: "yes", want: FilterActionBlock},
		{name: "uppercase allow", action: "ALLOW", want: FilterActionBlock},
		{name: "allow with trailing space", action: "allow ", want: FilterActionBlock},
		{name: "ask", action: FilterActionAsk, want: FilterActionBlock},
		{name: "block", action: FilterActionBlock, want: FilterActionBlock},
		{name: "allow", action: FilterActionAllow, want: FilterActionAllow},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := shortTempDir(t)
			server, err := NewAskServer(dir, log.New(io.Discard, "", 0))
			if err != nil {
				t.Fatalf("NewAskServer failed: %v", err)
			}
			defer func() { _ = server.Close() }()

			startMonitor(t, server, dir, func(req AskRequest) AskResponse {
				return AskResponse{ID: req.ID, Action: tc.action}
			})

			var logBuf bytes.Buffer
			queue := NewAskQueue(server, nil, 5*time.Second, log.New(&logBuf, "", 0))
			got, err := queue.RequestApproval(&AskRequest{
				ID:     "req-1",
				Method: "GET",
				URL:    "https://example.com",
				Host:   "example.com",
			})
			if err != nil {
				t.Fatalf("RequestApproval failed: %v", err)
			}
			if got != tc.want {
				t.Fatalf("action %q: got %q, want %q", tc.action, got, tc.want)
			}

			valid := tc.action == FilterActionAllow || tc.action == FilterActionBlock
			quoted := fmt.Sprintf("%q", string(tc.action))
			if valid && logBuf.Len() != 0 {
				t.Errorf("valid action %q must not be logged as rejected, got %q", tc.action, logBuf.String())
			}
			if !valid && !strings.Contains(logBuf.String(), quoted) {
				t.Errorf("rejected action must be logged as %s, got %q", quoted, logBuf.String())
			}
		})
	}
}

func TestAskQueue_RequestApproval_RememberOnlyForValidAction(t *testing.T) {
	cases := []struct {
		name       string
		action     FilterAction
		wantCached FilterAction
	}{
		{name: "unknown action is not cached", action: "yes", wantCached: ""},
		{name: "empty action is not cached", action: "", wantCached: ""},
		{name: "allow is cached", action: FilterActionAllow, wantCached: FilterActionAllow},
		{name: "block is cached", action: FilterActionBlock, wantCached: FilterActionBlock},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := shortTempDir(t)
			server, err := NewAskServer(dir, log.New(io.Discard, "", 0))
			if err != nil {
				t.Fatalf("NewAskServer failed: %v", err)
			}
			defer func() { _ = server.Close() }()

			startMonitor(t, server, dir, func(req AskRequest) AskResponse {
				return AskResponse{ID: req.ID, Action: tc.action, Remember: true}
			})

			engine, err := NewFilterEngine(&FilterConfig{DefaultAction: FilterActionAsk})
			if err != nil {
				t.Fatalf("NewFilterEngine failed: %v", err)
			}

			queue := NewAskQueue(server, engine, 5*time.Second, log.New(io.Discard, "", 0))
			if _, err := queue.RequestApproval(&AskRequest{
				ID:     "req-1",
				Method: "GET",
				URL:    "https://example.com",
				Host:   "example.com",
			}); err != nil {
				t.Fatalf("RequestApproval failed: %v", err)
			}

			if got := engine.getCachedDecision("example.com"); got != tc.wantCached {
				t.Errorf("cached decision after %q: got %q, want %q", tc.action, got, tc.wantCached)
			}
		})
	}
}
