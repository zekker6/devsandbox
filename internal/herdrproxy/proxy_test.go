package herdrproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"devsandbox/internal/cmdpattern"
)

// shortSocketDir keeps socket paths inside the kernel's sun_path limit, which
// the long sandbox TMPDIR would otherwise blow past.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// stubUpstream is a fake herdr server. It records what it receives and replies
// with whatever the test queues up.
type stubUpstream struct {
	t        *testing.T
	listener net.Listener
	path     string

	mu       sync.Mutex
	received []string

	// respond maps a request id to the raw response line to send back.
	respond func(req request) string
	// delay is applied before replying, to widen timing windows in tests.
	delay time.Duration
}

func newStubUpstream(t *testing.T, dir string) *stubUpstream {
	t.Helper()
	path := filepath.Join(dir, "up.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen upstream: %v", err)
	}
	s := &stubUpstream{t: t, listener: l, path: path}
	t.Cleanup(func() { _ = l.Close() })
	go s.serve()
	return s
}

func (s *stubUpstream) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer func() { _ = conn.Close() }()
			r := bufio.NewReader(conn)

			// Each request is answered in its own goroutine, as a real server
			// would: a slow call must not hold up a later fast one. Without
			// this the stub serializes and can never produce the out-of-order
			// replies the proxy is supposed to relay faithfully.
			var writeMu sync.Mutex
			var inFlight sync.WaitGroup
			defer inFlight.Wait()

			for {
				line, err := ReadLine(r)
				if err != nil {
					return
				}
				s.mu.Lock()
				s.received = append(s.received, string(line))
				respond, delay := s.respond, s.delay
				s.mu.Unlock()

				req, err := parseRequest(line)
				if err != nil {
					continue
				}
				if respond == nil {
					continue
				}

				inFlight.Add(1)
				go func() {
					defer inFlight.Done()
					if delay > 0 {
						time.Sleep(delay)
					}
					reply := respond(req)
					if reply == "" {
						return
					}
					writeMu.Lock()
					defer writeMu.Unlock()
					_ = WriteLine(conn, []byte(reply))
				}()
			}
		}()
	}
}

func (s *stubUpstream) got() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.received...)
}

func (s *stubUpstream) setRespond(f func(request) string) {
	s.mu.Lock()
	s.respond = f
	s.mu.Unlock()
}

// proxyFixture wires a proxy in front of a stub upstream.
type proxyFixture struct {
	proxy      *Proxy
	upstream   *stubUpstream
	listenPath string
	tabs       *cmdpattern.OwnedSet[string]
	panes      *cmdpattern.OwnedSet[string]
	projectDir string
	scriptPath string
}

func newProxyFixture(t *testing.T) *proxyFixture {
	t.Helper()
	dir := shortSocketDir(t)
	up := newStubUpstream(t, dir)

	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	scriptPath := filepath.Join(projectDir, "revdiff-launch-abc")
	if err := os.WriteFile(scriptPath, []byte(validBody()), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}

	reloc, err := NewRelocator(filepath.Join(base, "host-only"), nil)
	if err != nil {
		t.Fatalf("NewRelocator: %v", err)
	}
	t.Cleanup(func() { _ = reloc.Cleanup() })

	tabs := cmdpattern.NewOwnedSet[string]()
	panes := cmdpattern.NewOwnedSet[string]()

	filter := NewFilter(FilterConfig{
		Capabilities: []Capability{CapLaunchOverlay, CapNotify},
		LaunchScript: testScriptPattern(),
		OwnedTabs:    tabs,
		OwnedPanes:   panes,
		Relocator:    reloc,
		ProjectDir:   projectDir,
	})

	listenPath := filepath.Join(dir, "proxy.sock")
	p := New(up.path, listenPath, filter, tabs, panes)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })

	return &proxyFixture{
		proxy: p, upstream: up, listenPath: listenPath,
		tabs: tabs, panes: panes, projectDir: projectDir, scriptPath: scriptPath,
	}
}

// dial opens a client connection to the proxy.
func (f *proxyFixture) dial(t *testing.T) (net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.Dial("unix", f.listenPath)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, bufio.NewReader(c)
}

func tabCreateReply(id, tabID, paneID string) string {
	return fmt.Sprintf(
		`{"id":%q,"result":{"tab":{"tab_id":%q},"root_pane":{"pane_id":%q}}}`, id, tabID, paneID)
}

func TestProxyForwardsAllowedRequest(t *testing.T) {
	f := newProxyFixture(t)
	f.upstream.setRespond(func(req request) string { return tabCreateReply(req.ID, "tab3", "pane7") })

	conn, r := f.dial(t)
	line := `{"id":"a","method":"tab.create","params":{"cwd":"` + f.projectDir + `","label":"rev"}}`
	if err := WriteLine(conn, []byte(line)); err != nil {
		t.Fatalf("write: %v", err)
	}

	resp, err := ReadLine(r)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if !strings.Contains(string(resp), "tab3") {
		t.Errorf("response = %q, want the upstream reply relayed", resp)
	}
	if got := f.upstream.got(); len(got) != 1 {
		t.Errorf("upstream received %d requests, want 1: %v", len(got), got)
	}
}

func TestProxyDeniedRequestNeverReachesUpstream(t *testing.T) {
	f := newProxyFixture(t)

	conn, r := f.dial(t)
	line := `{"id":"danger","method":"pane.read","params":{"pane_id":"users-pane"}}`
	if err := WriteLine(conn, []byte(line)); err != nil {
		t.Fatalf("write: %v", err)
	}

	resp, err := ReadLine(r)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	var out errorResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("denial is not valid JSON: %v", err)
	}
	if out.ID != "danger" {
		t.Errorf("denial id = %q, want the request id echoed", out.ID)
	}
	if out.Error.Message == "" {
		t.Error("denial carries no message")
	}
	if got := f.upstream.got(); len(got) != 0 {
		t.Errorf("denied request reached upstream: %v", got)
	}
}

// TestProxyOwnershipBeforeRelay is the ordering guarantee. The client fires
// pane.send_input the instant it sees the tab.create reply; if the proxy
// relayed the reply before recording ownership, the follow-up would race and
// intermittently be denied.
func TestProxyOwnershipBeforeRelay(t *testing.T) {
	f := newProxyFixture(t)
	f.upstream.setRespond(func(req request) string {
		if req.Method == methodTabCreate {
			return tabCreateReply(req.ID, "tab3", "pane7")
		}
		return fmt.Sprintf(`{"id":%q,"result":{}}`, req.ID)
	})

	for i := range 50 {
		conn, r := f.dial(t)

		create := `{"id":"c","method":"tab.create","params":{"cwd":"` + f.projectDir + `"}}`
		if err := WriteLine(conn, []byte(create)); err != nil {
			t.Fatalf("write create: %v", err)
		}
		if _, err := ReadLine(r); err != nil {
			t.Fatalf("read create reply: %v", err)
		}

		// Immediately, exactly as the launcher does.
		send := `{"id":"s","method":"pane.send_input","params":{"pane_id":"pane7","text":"sh ` +
			f.scriptPath + `","keys":["Enter"]}}`
		if err := WriteLine(conn, []byte(send)); err != nil {
			t.Fatalf("write send_input: %v", err)
		}
		resp, err := ReadLine(r)
		if err != nil {
			t.Fatalf("read send_input reply: %v", err)
		}
		if strings.Contains(string(resp), "herdr-proxy:") {
			t.Fatalf("iteration %d: send_input was denied right after tab.create: %s", i, resp)
		}
		_ = conn.Close()
	}
}

func TestProxyMultiplexesMixedAllowAndDeny(t *testing.T) {
	f := newProxyFixture(t)
	f.tabs.Add("owned-tab")
	f.upstream.setRespond(func(req request) string {
		return fmt.Sprintf(`{"id":%q,"result":{"ok":true}}`, req.ID)
	})

	conn, r := f.dial(t)

	// Pipelined on one connection, alternating allowed and denied.
	lines := []string{
		`{"id":"1","method":"notification.show","params":{"title":"one"}}`,
		`{"id":"2","method":"pane.read","params":{"pane_id":"x"}}`,
		`{"id":"3","method":"tab.close","params":{"tab_id":"owned-tab"}}`,
		`{"id":"4","method":"server.stop","params":{}}`,
		`{"id":"5","method":"notification.show","params":{"title":"five"}}`,
	}
	for _, l := range lines {
		if err := WriteLine(conn, []byte(l)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	got := make(map[string]bool) // id -> denied
	for range lines {
		resp, err := ReadLine(r)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		got[responseID(resp)] = strings.Contains(string(resp), "herdr-proxy:")
	}

	for id, wantDenied := range map[string]bool{"1": false, "2": true, "3": false, "4": true, "5": false} {
		denied, ok := got[id]
		if !ok {
			t.Errorf("no response correlated to id %q", id)
			continue
		}
		if denied != wantDenied {
			t.Errorf("id %q denied = %v, want %v", id, denied, wantDenied)
		}
	}

	// Only the three allowed requests may have reached upstream.
	if up := f.upstream.got(); len(up) != 3 {
		t.Errorf("upstream received %d requests, want 3: %v", len(up), up)
	}
}

func TestProxyRelaysOutOfOrderResponses(t *testing.T) {
	f := newProxyFixture(t)
	// Reply to "slow" after "fast", inverting request order.
	f.upstream.setRespond(func(req request) string {
		if req.ID == "slow" {
			time.Sleep(80 * time.Millisecond)
		}
		return fmt.Sprintf(`{"id":%q,"result":{"ok":true}}`, req.ID)
	})

	conn, r := f.dial(t)
	for _, id := range []string{"slow", "fast"} {
		line := fmt.Sprintf(`{"id":%q,"method":"notification.show","params":{"title":"x"}}`, id)
		if err := WriteLine(conn, []byte(line)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	var order []string
	for range 2 {
		resp, err := ReadLine(r)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		order = append(order, responseID(resp))
	}

	if len(order) != 2 || order[0] != "fast" || order[1] != "slow" {
		t.Errorf("relay order = %v, want [fast slow] preserved as upstream produced them", order)
	}
}

func TestProxyRewritesRelocatedScript(t *testing.T) {
	f := newProxyFixture(t)
	f.panes.Add("pane7")
	f.upstream.setRespond(func(req request) string {
		return fmt.Sprintf(`{"id":%q,"result":{}}`, req.ID)
	})

	conn, r := f.dial(t)
	line := `{"id":"a","method":"pane.send_input","params":{"pane_id":"pane7","text":"sh ` +
		f.scriptPath + `","keys":["Enter"]}}`
	if err := WriteLine(conn, []byte(line)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ReadLine(r); err != nil {
		t.Fatalf("read: %v", err)
	}

	got := f.upstream.got()
	if len(got) != 1 {
		t.Fatalf("upstream received %d requests, want 1", len(got))
	}
	if strings.Contains(got[0], f.scriptPath) {
		t.Errorf("upstream saw the sandbox-writable script path: %s", got[0])
	}
	if !strings.Contains(got[0], "herdr-launch-") {
		t.Errorf("upstream did not receive the relocated path: %s", got[0])
	}
}

func TestProxyUpstreamUnreachable(t *testing.T) {
	dir := shortSocketDir(t)
	listenPath := filepath.Join(dir, "proxy.sock")

	p := New(filepath.Join(dir, "nonexistent.sock"), listenPath,
		NewFilter(FilterConfig{Capabilities: []Capability{CapNotify}}), nil, nil)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Stop() }()

	conn, err := net.Dial("unix", listenPath)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// The client must get a parseable error rather than hanging forever.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := ReadLine(bufio.NewReader(conn))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(resp), "upstream unreachable") {
		t.Errorf("response = %q, want an upstream-unreachable error", resp)
	}
}

func TestProxyClientDisconnectMidStream(t *testing.T) {
	f := newProxyFixture(t)
	f.upstream.setRespond(func(req request) string {
		return fmt.Sprintf(`{"id":%q,"result":{}}`, req.ID)
	})

	conn, err := net.Dial("unix", f.listenPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	line := `{"id":"a","method":"notification.show","params":{"title":"x"}}`
	if err := WriteLine(conn, []byte(line)); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Drop the connection without reading the reply.
	_ = conn.Close()

	// The proxy must survive and keep serving new clients.
	time.Sleep(50 * time.Millisecond)
	conn2, r2 := f.dial(t)
	if err := WriteLine(conn2, []byte(`{"id":"b","method":"notification.show","params":{"title":"y"}}`)); err != nil {
		t.Fatalf("write after disconnect: %v", err)
	}
	if _, err := ReadLine(r2); err != nil {
		t.Fatalf("proxy stopped serving after a client disconnect: %v", err)
	}
}

func TestProxyStopIsPrompt(t *testing.T) {
	f := newProxyFixture(t)
	f.dial(t) // an idle connection with a blocked handler

	start := time.Now()
	if err := f.proxy.Stop(); err != nil {
		t.Errorf("Stop returned error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Stop took %v with an idle connection, want prompt shutdown", elapsed)
	}
}

func TestProxyDeniesDuplicateInFlightID(t *testing.T) {
	f := newProxyFixture(t)
	f.upstream.setRespond(nil) // never reply, so ids stay in flight
	f.upstream.delay = 0

	conn, r := f.dial(t)
	line := `{"id":"same","method":"notification.show","params":{"title":"x"}}`
	for range 2 {
		if err := WriteLine(conn, []byte(line)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	// The first is forwarded and unanswered; the second must be refused rather
	// than overwriting the pending correlation.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := ReadLine(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(resp), "already in flight") {
		t.Errorf("response = %q, want a duplicate-id denial", resp)
	}
}
