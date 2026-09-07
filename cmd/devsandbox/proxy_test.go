package main

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
	"testing"
	"time"
	"unicode/utf8"

	"devsandbox/internal/notice"
	"devsandbox/internal/proxy"
	"devsandbox/internal/termsafe"
)

// The monitor sits on the same socket a concurrent session's proxy would take,
// so both of its paths - the listener that waits for a sandbox and the dialer
// that joins a running one - must establish that the peer is a proxy before
// they answer a single request, and must never mistake a live peer that fails
// that check for a stale socket file.

func TestHandshakeWithProxy_ListenerAdmitsProxyHello(t *testing.T) {
	ours, theirs := net.Pipe()
	defer func() { _ = ours.Close() }()
	defer func() { _ = theirs.Close() }()

	answer := make(chan proxy.AskHello, 1)
	go func() {
		defer close(answer)
		enc := json.NewEncoder(theirs)
		dec := json.NewDecoder(theirs)
		if err := enc.Encode(proxy.AskHello{Proto: proxy.AskProto, Role: proxy.AskRoleProxy}); err != nil {
			return
		}
		var hello proxy.AskHello
		if err := dec.Decode(&hello); err != nil {
			return
		}
		answer <- hello
	}()

	if err := handshakeWithProxy(ours, json.NewDecoder(ours), json.NewEncoder(ours), false); err != nil {
		t.Fatalf("listener refused a proxy: %v", err)
	}
	hello, ok := <-answer
	if !ok {
		t.Fatal("proxy got no hello back from the monitor")
	}
	if hello.Proto != proxy.AskProto || hello.Role != proxy.AskRoleMonitor {
		t.Errorf("monitor hello = %+v, want proto %q role %q", hello, proxy.AskProto, proxy.AskRoleMonitor)
	}
}

func TestHandshakeWithProxy_ListenerRejectsNonProxies(t *testing.T) {
	cases := []struct {
		name  string
		first any
	}{
		{name: "monitor hello", first: proxy.AskHello{Proto: proxy.AskProto, Role: proxy.AskRoleMonitor}},
		{name: "unknown protocol", first: proxy.AskHello{Proto: "devsandbox-ask/0", Role: proxy.AskRoleProxy}},
		{name: "request before hello", first: proxy.AskRequest{ID: "1", Host: "example.com"}},
		{name: "peer closes", first: io.EOF},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ours, theirs := net.Pipe()
			defer func() { _ = ours.Close() }()
			defer func() { _ = theirs.Close() }()

			answered := make(chan bool, 1)
			go func() {
				if _, isErr := tc.first.(error); isErr {
					_ = theirs.Close()
					answered <- false
					return
				}
				if err := json.NewEncoder(theirs).Encode(tc.first); err != nil {
					answered <- false
					return
				}
				// Anything that arrives now would be the monitor's hello.
				buf := make([]byte, 1)
				n, _ := theirs.Read(buf)
				answered <- n > 0
			}()

			err := handshakeWithProxy(ours, json.NewDecoder(ours), json.NewEncoder(ours), false)
			if err == nil {
				t.Fatal("listener admitted a peer that is not a proxy")
			}
			if !errors.Is(err, errAskPeerRejected) {
				t.Errorf("err = %v, want errAskPeerRejected", err)
			}
			_ = ours.Close()
			if <-answered {
				t.Error("monitor sent its hello to a peer it refused")
			}
		})
	}
}

func TestHandshakeWithProxy_DialerRequiresProxyHello(t *testing.T) {
	cases := []struct {
		name   string
		answer any
		wantOK bool
	}{
		{name: "proxy hello", answer: proxy.AskHello{Proto: proxy.AskProto, Role: proxy.AskRoleProxy}, wantOK: true},
		{name: "monitor hello", answer: proxy.AskHello{Proto: proxy.AskProto, Role: proxy.AskRoleMonitor}},
		{name: "request instead of hello", answer: proxy.AskRequest{ID: "1", Host: "example.com"}},
		{name: "closes without answering", answer: io.EOF},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ours, theirs := net.Pipe()
			defer func() { _ = ours.Close() }()
			defer func() { _ = theirs.Close() }()

			received := make(chan proxy.AskHello, 1)
			go func() {
				defer close(received)
				var hello proxy.AskHello
				if err := json.NewDecoder(theirs).Decode(&hello); err != nil {
					return
				}
				received <- hello
				if _, isErr := tc.answer.(error); isErr {
					_ = theirs.Close()
					return
				}
				_ = json.NewEncoder(theirs).Encode(tc.answer)
			}()

			err := handshakeWithProxy(ours, json.NewDecoder(ours), json.NewEncoder(ours), true)
			if tc.wantOK && err != nil {
				t.Fatalf("dialer refused a proxy: %v", err)
			}
			if !tc.wantOK {
				if err == nil {
					t.Fatal("dialer accepted a peer that is not a proxy")
				}
				if !errors.Is(err, errAskPeerRejected) {
					t.Errorf("err = %v, want errAskPeerRejected", err)
				}
			}

			hello, ok := <-received
			if !ok {
				t.Fatal("dialer sent no hello")
			}
			if hello.Proto != proxy.AskProto || hello.Role != proxy.AskRoleMonitor {
				t.Errorf("monitor hello = %+v, want proto %q role %q", hello, proxy.AskProto, proxy.AskRoleMonitor)
			}
		})
	}
}

// Auto-detection unlinks the socket when connecting fails and starts its own
// listener on the path. That is right for a stale file and wrong for a socket a
// running session owns - which is what a refused handshake means.
func TestConnectOrServeMonitor_UnlinksOnlyAStaleSocket(t *testing.T) {
	handshakeErr := fmt.Errorf("ask socket: %w: peer is a \"monitor\"", errAskPeerRejected)
	staleErr := fmt.Errorf("%w at /x: connection refused", errAskSocketStale)
	ttyErr := errors.New("proxy monitor requires an interactive terminal")

	cases := []struct {
		name        string
		socket      bool
		connectErr  error
		serveErr    error
		wantErr     error
		wantSocket  bool
		wantServed  bool
		wantDialled bool
	}{
		{name: "no socket", socket: false, wantServed: true, wantSocket: false},
		{name: "live monitor session ends", socket: true, connectErr: nil, wantSocket: true, wantDialled: true},
		{name: "stale socket", socket: true, connectErr: staleErr, wantSocket: false, wantServed: true, wantDialled: true},
		{name: "peer refused the handshake", socket: true, connectErr: handshakeErr, wantErr: handshakeErr, wantSocket: true, wantDialled: true},
		{name: "no terminal", socket: true, connectErr: ttyErr, wantErr: ttyErr, wantSocket: true, wantDialled: true},
		{name: "server fails", socket: true, connectErr: staleErr, serveErr: ttyErr, wantErr: ttyErr, wantSocket: false, wantServed: true, wantDialled: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			socketPath := filepath.Join(base, "ask.sock")
			if tc.socket {
				if err := os.WriteFile(socketPath, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			dialled, served := false, false
			err := connectOrServeMonitor(socketPath, base,
				func(path string) error {
					dialled = true
					if path != socketPath {
						t.Errorf("connect got %q, want %q", path, socketPath)
					}
					return tc.connectErr
				},
				func(sandboxBase string) error {
					served = true
					if sandboxBase != base {
						t.Errorf("serve got %q, want %q", sandboxBase, base)
					}
					return tc.serveErr
				})

			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if dialled != tc.wantDialled {
				t.Errorf("connect called = %v, want %v", dialled, tc.wantDialled)
			}
			if served != tc.wantServed {
				t.Errorf("serve called = %v, want %v", served, tc.wantServed)
			}
			_, statErr := os.Stat(socketPath)
			if exists := statErr == nil; exists != tc.wantSocket {
				t.Errorf("socket exists = %v, want %v", exists, tc.wantSocket)
			}
		})
	}
}

// A dial that fails is the one signal that nothing owns the socket; it must be
// the only error the auto-detect path reads as stale.
func TestDialAskSocket_DialFailureIsStale(t *testing.T) {
	// A Unix socket path is capped at 108 bytes, which t.TempDir() exceeds
	// under the shared $TMPDIR.
	base, err := os.MkdirTemp("/tmp", "ds-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	socketPath := filepath.Join(base, "ask.sock")
	l, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	if err := os.WriteFile(socketPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = dialAskSocket(socketPath, time.Second)
	if !errors.Is(err, errAskSocketStale) {
		t.Fatalf("err = %v, want errAskSocketStale", err)
	}
}

// A listening monitor serves every session of the project at once. A session
// that dials while another is being answered must get its hello back rather
// than wait in the backlog: its hello deadline would pass and that session
// would block every request for its lifetime.
func TestAcceptSandboxes_ServesSessionsConcurrently(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "ds-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	socketPath := filepath.Join(base, "ask.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	release := make(chan struct{})
	decide := func(req *proxy.AskRequest) proxy.AskResponse {
		<-release
		return proxy.AskResponse{ID: req.ID, Action: proxy.FilterActionAllow}
	}
	done := make(chan error, 1)
	go func() { done <- acceptSandboxes(ctx, io.Discard, listener, decide) }()

	dialSession := func(id string) (*json.Decoder, *json.Encoder) {
		t.Helper()
		conn, err := net.Dial("unix", socketPath)
		if err != nil {
			t.Fatalf("session %s: dial: %v", id, err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		dec := json.NewDecoder(conn)
		enc := json.NewEncoder(conn)
		if err := proxy.AskHandshake(conn, dec, enc, proxy.AskRoleProxy, true); err != nil {
			t.Fatalf("session %s: handshake: %v", id, err)
		}
		return dec, enc
	}

	decA, encA := dialSession("a")
	if err := encA.Encode(proxy.AskRequest{ID: "a-1", Host: "example.com"}); err != nil {
		t.Fatal(err)
	}

	// Session a is now parked in decide; b must still be admitted.
	decB, encB := dialSession("b")
	if err := encB.Encode(proxy.AskRequest{ID: "b-1", Host: "example.org"}); err != nil {
		t.Fatal(err)
	}

	close(release)
	for id, dec := range map[string]*json.Decoder{"a-1": decA, "b-1": decB} {
		var resp proxy.AskResponse
		if err := dec.Decode(&resp); err != nil {
			t.Fatalf("request %s: no response: %v", id, err)
		}
		if resp.ID != id || resp.Action != proxy.FilterActionAllow {
			t.Errorf("request %s: response %+v", id, resp)
		}
	}

	cancel()
	_ = listener.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("acceptSandboxes returned %v on shutdown", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("acceptSandboxes did not return after shutdown")
	}
}

// The monitor draws sandbox-supplied bytes on a raw-mode terminal. An escape
// sequence in any request field, or in an error that quotes what the peer
// sent, could clear the screen and repaint the prompt over a different host -
// so nothing the peer chose may reach the terminal as a control character.

// terminalLines splits monitor output into lines and fails on any control or
// format rune inside one. The CRLF terminators are the monitor's own.
func terminalLines(t *testing.T, out string) []string {
	t.Helper()
	lines := strings.Split(out, "\r\n")
	for i, line := range lines {
		if termsafe.HasControlRune(line) {
			t.Errorf("line %d reaches the terminal with a control rune: %q", i, line)
		}
	}
	return lines
}

func TestDisplayRequest_EscapesEveryField(t *testing.T) {
	req := &proxy.AskRequest{
		ID:      "7\x1b[2J",
		Method:  "GET\x1b[H",
		Host:    "api.example.com\x1b[2J\x1b[Hevil.example.com",
		Path:    "/v1/\xc2\x9b2J\xe2\x80\xaetxt.exe",
		Headers: map[string]string{"X-Trick\x1b[1A": "\x1b]0;pwned\a"},
		Body:    "\x1b[31mpayload\x1b[0m\r\n",
	}

	var buf bytes.Buffer
	displayRequest(&buf, req)
	lines := terminalLines(t, buf.String())

	for _, want := range []string{`7\x1b[2J`, `GET\x1b[H`, `\x1b[2J\x1b[Hevil`, `\u009b2J\u202etxt`, `X-Trick\x1b[1A: \x1b]0;pwned\a`, `\x1b[31mpayload`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output lacks the escaped form %q:\n%s", want, buf.String())
		}
	}

	width := utf8.RuneCountInString(lines[0])
	for i, line := range lines {
		if line == "" {
			continue
		}
		if got := utf8.RuneCountInString(line); got != width {
			t.Errorf("line %d is %d runes wide, want %d: %q", i, got, width, line)
		}
	}
}

func TestDisplayRequest_TruncatesByRune(t *testing.T) {
	req := &proxy.AskRequest{
		ID:     "1",
		Method: "POST",
		Host:   strings.Repeat("日", 80),
		Path:   "/" + strings.Repeat("\x1b", 80),
		Body:   strings.Repeat("é", 200),
	}

	var buf bytes.Buffer
	displayRequest(&buf, req)
	lines := terminalLines(t, buf.String())

	if !utf8.ValidString(buf.String()) {
		t.Error("output is not valid UTF-8: a field was cut inside a sequence")
	}
	width := utf8.RuneCountInString(lines[0])
	for i, line := range lines {
		if line == "" {
			continue
		}
		if got := utf8.RuneCountInString(line); got != width {
			t.Errorf("line %d is %d runes wide, want %d: %q", i, got, width, line)
		}
	}
	if !strings.Contains(buf.String(), "...") {
		t.Error("an over-long field was not marked as truncated")
	}
}

func TestGetUserDecision_EscapesTheEchoedHost(t *testing.T) {
	cases := []struct {
		key  byte
		want string
	}{
		{key: 'a', want: "Allowed: "},
		{key: 'b', want: "Blocked: "},
		{key: 's', want: "Allowed for session: "},
		{key: 'n', want: "Blocked for session: "},
	}
	for _, tc := range cases {
		t.Run(string(tc.key), func(t *testing.T) {
			req := &proxy.AskRequest{ID: "1", Host: "ok.example.com\x1b[2J\x1b[H✓ Allowed: evil.example.com"}
			keys := make(chan byte, 1)
			keys <- tc.key

			var buf bytes.Buffer
			resp := getUserDecision(&buf, req, keys, time.Now().Add(5*time.Second))
			if resp.ID != req.ID {
				t.Errorf("response id = %q, want %q", resp.ID, req.ID)
			}
			terminalLines(t, buf.String())
			if !strings.Contains(buf.String(), tc.want+`ok.example.com\x1b[2J\x1b[H`) {
				t.Errorf("echo line missing or unescaped:\n%s", buf.String())
			}
		})
	}
}

func TestAskTimeout_DefaultsTo30Seconds(t *testing.T) {
	cases := []struct {
		timeout int
		want    time.Duration
	}{
		{timeout: 0, want: 30 * time.Second},
		{timeout: 7, want: 7 * time.Second},
	}
	for _, tc := range cases {
		if got := askTimeout(&proxy.AskRequest{Timeout: tc.timeout}); got != tc.want {
			t.Errorf("askTimeout(Timeout: %d) = %v, want %v", tc.timeout, got, tc.want)
		}
	}
}

// Every way a prompt can end without an answer must block, because that is
// what the proxy does on its side once its deadline passes.
func TestGetUserDecision_BlocksWithoutAnAnswer(t *testing.T) {
	cases := []struct {
		name   string
		keys   func() chan byte
		window time.Duration
		want   string
	}{
		{
			name:   "deadline passes with no key",
			keys:   func() chan byte { return make(chan byte, 1) },
			window: 100 * time.Millisecond,
			want:   "Request timed out (auto-rejected)",
		},
		{
			name: "unrecognised key is ignored",
			keys: func() chan byte {
				keys := make(chan byte, 2)
				keys <- 'x'
				keys <- 'b'
				return keys
			},
			window: 5 * time.Second,
			want:   "Blocked: example.com",
		},
		{
			name: "key channel closed",
			keys: func() chan byte {
				keys := make(chan byte)
				close(keys)
				return keys
			},
			window: 5 * time.Second,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &proxy.AskRequest{ID: "1", Host: "example.com"}
			var buf bytes.Buffer
			resp := getUserDecision(&buf, req, tc.keys(), time.Now().Add(tc.window))
			if resp.ID != req.ID || resp.Action != proxy.FilterActionBlock || resp.Remember {
				t.Errorf("response = %+v, want block for %q without remember", resp, req.ID)
			}
			if strings.Contains(buf.String(), "Allowed") {
				t.Errorf("output reports an allow:\n%s", buf.String())
			}
			if !strings.Contains(buf.String(), tc.want) {
				t.Errorf("output lacks %q:\n%s", tc.want, buf.String())
			}
		})
	}
}

// A request whose window closed while it waited for the terminal is not
// drawn: the proxy has already blocked it, and a key pressed for it would
// answer nothing - or worse, the next prompt.
func TestPromptDecision_SkipsAnExpiredRequest(t *testing.T) {
	req := &proxy.AskRequest{ID: "1", Host: "late.example.com\x1b[2J"}
	keys := make(chan byte, 1)
	keys <- 'a'

	var buf bytes.Buffer
	resp := promptDecision(&buf, req, keys, time.Now().Add(-time.Millisecond))
	if resp.ID != req.ID || resp.Action != proxy.FilterActionBlock {
		t.Errorf("response = %+v, want block for %q", resp, req.ID)
	}
	terminalLines(t, buf.String())
	if strings.Contains(buf.String(), "Request #") {
		t.Errorf("an expired request was drawn:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), `Request from late.example.com\x1b[2J expired before it could be shown`) {
		t.Errorf("output lacks the expired line:\n%s", buf.String())
	}
	if len(keys) != 1 {
		t.Error("an expired request consumed the key meant for the next prompt")
	}
}

// syncBuffer collects monitor output that several deciders write at once.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitForOutput polls out until cond holds on it.
func waitForOutput(t *testing.T, out *syncBuffer, what string, cond func(string) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond(out.String()) {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not appear within 5s:\n%s", what, out.String())
		}
		time.Sleep(time.Millisecond)
	}
}

// A listening monitor serves every session at once, so two requests can
// arrive together. One prompt is on screen at a time and the key pressed
// answers that prompt: the second box is drawn only after the first is
// decided.
func TestNewDecider_SerializesPrompts(t *testing.T) {
	var out syncBuffer
	keys := make(chan byte, 2)
	decide := newDecider(&out, keys)

	reqs := []*proxy.AskRequest{
		{ID: "1", Host: "one.example.com"},
		{ID: "2", Host: "two.example.com"},
	}
	responses := make(chan proxy.AskResponse, len(reqs))
	for _, req := range reqs {
		go func() { responses <- decide(req) }()
	}

	prompts := func(n int) func(string) bool {
		return func(s string) bool { return strings.Count(s, "Decision: ") == n }
	}
	waitForOutput(t, &out, "the first prompt", prompts(1))
	keys <- 'a'
	waitForOutput(t, &out, "the second prompt", prompts(2))
	keys <- 'b'

	got := map[string]proxy.AskResponse{}
	for range reqs {
		select {
		case resp := <-responses:
			got[resp.ID] = resp
		case <-time.After(5 * time.Second):
			t.Fatal("a request was not answered")
		}
	}

	text := out.String()
	first, second := reqs[0], reqs[1]
	if strings.Index(text, first.Host) > strings.Index(text, second.Host) {
		first, second = second, first
	}
	if got[first.ID].Action != proxy.FilterActionAllow {
		t.Errorf("request %s, shown first, got %+v, want the allow key", first.ID, got[first.ID])
	}
	if got[second.ID].Action != proxy.FilterActionBlock {
		t.Errorf("request %s, shown second, got %+v, want the block key", second.ID, got[second.ID])
	}

	firstBox := strings.Index(text, "Request #"+first.ID)
	firstEcho := strings.Index(text, "✓ Allowed: "+first.Host)
	secondBox := strings.Index(text, "Request #"+second.ID)
	secondEcho := strings.Index(text, "✗ Blocked: "+second.Host)
	if firstBox < 0 || firstEcho < 0 || secondBox < 0 || secondEcho < 0 ||
		firstBox > firstEcho || firstEcho > secondBox || secondBox > secondEcho {
		t.Errorf("prompts interleaved (box %d, echo %d, box %d, echo %d):\n%s", firstBox, firstEcho, secondBox, secondEcho, text)
	}
}

// shortTempDir returns a directory whose ask socket path fits the 108-byte
// Unix socket cap, which t.TempDir() exceeds under the shared $TMPDIR.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ds-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// warnAskHandshake is what tells the user, on the confirmation gate, that
// this session's ask socket is owned by another session's proxy and every
// ask-mode request will be blocked.
func TestWarnAskHandshake(t *testing.T) {
	t.Run("no ask server", func(t *testing.T) {
		captureNotices(t)
		warnAskHandshake(nil)
		if entries, _ := notice.Raised(); len(entries) != 0 {
			t.Errorf("raised %+v for a nil ask server", entries)
		}
	})

	t.Run("listening proxy", func(t *testing.T) {
		captureNotices(t)
		server, err := proxy.NewAskServer(shortTempDir(t), log.New(io.Discard, "", 0))
		if err != nil {
			t.Fatalf("NewAskServer: %v", err)
		}
		t.Cleanup(func() { _ = server.Close() })

		warnAskHandshake(server)
		if entries, _ := notice.Raised(); len(entries) != 0 {
			t.Errorf("raised %+v for a proxy that owns its socket", entries)
		}
	})

	t.Run("socket owned by another proxy", func(t *testing.T) {
		captureNotices(t)
		dir := shortTempDir(t)
		if err := os.MkdirAll(proxy.AskSocketDir(dir), 0o700); err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("unix", proxy.AskSocketPath(dir))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			_ = json.NewEncoder(conn).Encode(proxy.AskHello{Proto: proxy.AskProto, Role: proxy.AskRoleProxy})
			_, _ = io.Copy(io.Discard, conn)
		}()

		server, err := proxy.NewAskServer(dir, log.New(io.Discard, "", 0))
		if err != nil {
			t.Fatalf("NewAskServer: %v", err)
		}
		t.Cleanup(func() { _ = server.Close() })
		if server.HandshakeError() == nil {
			t.Fatal("ask server took another proxy for its monitor")
		}

		warnAskHandshake(server)
		entries, _ := notice.Raised()
		if len(entries) != 1 {
			t.Fatalf("raised %+v, want exactly one warning", entries)
		}
		if entries[0].Level != notice.LevelWarn {
			t.Errorf("level = %q, want %q", entries[0].Level, notice.LevelWarn)
		}
		if !strings.Contains(entries[0].Msg, "not a monitor") {
			t.Errorf("warning %q does not carry the handshake error", entries[0].Msg)
		}
	})
}

// scriptedConn is a net.Conn whose reads serve a fixed byte sequence and then
// fail with an error of the test's choosing, standing in for a peer whose
// bytes end up quoted inside the error that ends its connection.
type scriptedConn struct {
	net.Conn
	r   io.Reader
	err error
}

func (c *scriptedConn) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		return n, nil
	}
	if errors.Is(err, io.EOF) {
		return 0, c.err
	}
	return n, err
}

func (c *scriptedConn) Write(p []byte) (int, error)       { return len(p), nil }
func (c *scriptedConn) Close() error                      { return nil }
func (c *scriptedConn) SetReadDeadline(_ time.Time) error { return nil }

func TestServeSandbox_EscapesConnectionErrors(t *testing.T) {
	proxyHello, err := json.Marshal(proxy.AskHello{Proto: proxy.AskProto, Role: proxy.AskRoleProxy})
	if err != nil {
		t.Fatal(err)
	}
	peerErr := errors.New("read: \x1b[2J\x1b[H\xc2\x9b31mConnection error: none\x1b[0m")

	cases := []struct {
		name   string
		before string
		want   string
	}{
		{name: "refused before the hello", before: "", want: "Refused a connection: "},
		{name: "failed after the hello", before: string(proxyHello) + "\n", want: "Connection error: "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := &scriptedConn{r: strings.NewReader(tc.before), err: peerErr}
			decide := func(req *proxy.AskRequest) proxy.AskResponse {
				t.Errorf("a request was shown: %+v", req)
				return proxy.AskResponse{ID: req.ID, Action: proxy.FilterActionBlock}
			}

			var buf bytes.Buffer
			serveSandbox(context.Background(), &buf, conn, decide)
			terminalLines(t, buf.String())
			if !strings.Contains(buf.String(), tc.want) {
				t.Errorf("output lacks %q:\n%s", tc.want, buf.String())
			}
			if !strings.Contains(buf.String(), `read: \x1b[2J\x1b[H\u009b31m`) {
				t.Errorf("error text missing or unescaped:\n%s", buf.String())
			}
		})
	}
}
