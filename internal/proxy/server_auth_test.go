package proxy

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"devsandbox/internal/logging"
	"devsandbox/internal/proxyenv"
)

// testAuthToken is the per-session credential every test server is built
// with. Same shape as a real one (NewAuthToken): 32 bytes, hex.
const testAuthToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// newTestConfig is NewConfig with the credential every server must have.
func newTestConfig(sandboxBase string, port int) *Config {
	cfg := NewConfig(sandboxBase, port)
	cfg.AuthToken = testAuthToken
	return cfg
}

// testProxyURL returns the URL a sandbox would be handed for srv, credential
// included: http.Transport turns the userinfo into Proxy-Authorization on
// every plain request and on every CONNECT, and sends nothing inside a
// tunnel - exactly what a real client does.
func testProxyURL(srv *Server) *url.URL {
	return proxyURLWithToken(srv, srv.config.AuthToken)
}

func proxyURLWithToken(srv *Server, token string) *url.URL {
	host, portStr, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		panic(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		panic(err)
	}
	u, err := url.Parse(proxyenv.URL(host, port, token))
	if err != nil {
		panic(err)
	}
	return u
}

// proxyAuthHeader is the header line a raw client sends for token.
func proxyAuthHeader(token string) string {
	return "Proxy-Authorization: Basic " +
		base64.StdEncoding.EncodeToString([]byte(proxyenv.AuthUser+":"+token)) + "\r\n"
}

// startAuthProxy starts a server whose upstream transport trusts ts, so a
// MITM'd request to it round-trips.
func startAuthProxy(t *testing.T, cfg *Config, ts *httptest.Server) *Server {
	t.Helper()
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	if ts != nil && ts.TLS != nil {
		trustUpstreamCert(t, srv, ts)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })
	return srv
}

// recordingUpstream is an upstream that remembers whether the proxy's
// credential ever reached it.
type recordingUpstream struct {
	mu   sync.Mutex
	hits int
	auth []string
}

func (u *recordingUpstream) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.hits++
		if v := r.Header.Get("Proxy-Authorization"); v != "" {
			u.auth = append(u.auth, v)
		}
		u.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}
}

func (u *recordingUpstream) snapshot() (int, []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits, append([]string(nil), u.auth...)
}

func TestNewAuthToken(t *testing.T) {
	a, b := NewAuthToken(), NewAuthToken()
	if len(a) != 64 {
		t.Errorf("token %q: want 64 hex chars (32 random bytes)", a)
	}
	if _, err := strconv.ParseUint(a[:16], 16, 64); err != nil {
		t.Errorf("token %q is not hex: %v", a, err)
	}
	if a == b {
		t.Error("two tokens are equal")
	}
}

// TestNewServer_RefusesEmptyAuthToken pins that enforcement keys on the token
// and never on Config.Enabled: a listener that exists is reachable.
func TestNewServer_RefusesEmptyAuthToken(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		cfg := NewConfig(shortTempDir(t), 0)
		cfg.Enabled = enabled
		srv, err := NewServer(cfg)
		if srv != nil {
			t.Cleanup(func() { _ = srv.Stop() })
			t.Errorf("Enabled=%v: NewServer returned a server with no auth token", enabled)
		}
		if !errors.Is(err, ErrMissingAuthToken) {
			t.Errorf("Enabled=%v: error %v does not wrap ErrMissingAuthToken", enabled, err)
		}
	}
}

// TestServerAuth_PlainRequest covers the plain-HTTP path: no credential or a
// wrong one is a 407 that reaches neither the upstream nor the request log,
// the right one round-trips and the upstream never sees the header.
func TestServerAuth_PlainRequest(t *testing.T) {
	upstream := &recordingUpstream{}
	ts := httptest.NewServer(upstream.handler())
	defer ts.Close()

	srv := startAuthProxy(t, newTestConfig(shortTempDir(t), 0), ts)

	wrongToken := strings.Repeat("f", len(testAuthToken))
	tests := []struct {
		name       string
		proxyURL   *url.URL
		wantStatus int
	}{
		{"no credential", &url.URL{Scheme: "http", Host: srv.Addr()}, http.StatusProxyAuthRequired},
		{"wrong token", proxyURLWithToken(srv, wrongToken), http.StatusProxyAuthRequired},
		{"empty token", proxyURLWithToken(srv, ""), http.StatusProxyAuthRequired},
		{"right token", testProxyURL(srv), http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{
				Transport: &http.Transport{Proxy: http.ProxyURL(tt.proxyURL)},
				Timeout:   5 * time.Second,
			}
			resp, err := client.Get(ts.URL + "/path")
			if err != nil {
				t.Fatalf("request through proxy: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusProxyAuthRequired {
				if got := resp.Header.Get("Proxy-Authenticate"); got != `Basic realm="devsandbox"` {
					t.Errorf("Proxy-Authenticate = %q, want the Basic challenge", got)
				}
				if got := resp.Header.Get("X-Blocked-By"); got != "" {
					t.Errorf("a 407 is not a filter decision, but carries X-Blocked-By=%q", got)
				}
			}
		})
	}

	hits, auth := upstream.snapshot()
	if hits != 1 {
		t.Errorf("upstream saw %d requests, want 1 (only the authenticated one)", hits)
	}
	if len(auth) != 0 {
		t.Errorf("upstream received Proxy-Authorization %v; the credential is the proxy's", auth)
	}

	entry := waitForLoggedEntry(t, srv.config.LogDir)
	if entries := readRequestLog(t, srv); len(entries) != 1 {
		t.Errorf("request log has %d entries, want 1: a refused request never reached this session", len(entries))
	}
	assertNoToken(t, "request log entry", entry)
	if _, ok := entry.RequestHeaders["Proxy-Authorization"]; ok {
		t.Error("request log entry records a Proxy-Authorization header; it is stripped before capture")
	}
}

// TestServerAuth_CONNECT_Transparent covers the tunnel path with MITM off: the
// credential is checked on the CONNECT itself, before the filter and the log.
func TestServerAuth_CONNECT_Transparent(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	target := strings.TrimPrefix(ts.URL, "https://")

	// goproxy chains every accepted CONNECT through an ambient HTTPS_PROXY
	// without consulting NO_PROXY, so under an outer proxy (a devsandbox
	// session, say) the tunnel to the loopback upstream never opens.
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "")

	cfg := newTestConfig(shortTempDir(t), 0)
	cfg.MITM = false
	srv := startAuthProxy(t, cfg, nil)

	t.Run("no credential", func(t *testing.T) {
		resp, conn := sendCONNECTWithHeaders(t, srv.Addr(), target, "")
		assertRefusedTunnel(t, resp, conn)
	})
	t.Run("wrong token", func(t *testing.T) {
		resp, conn := sendCONNECTWithHeaders(t, srv.Addr(), target, proxyAuthHeader(strings.Repeat("0", len(testAuthToken))))
		assertRefusedTunnel(t, resp, conn)
	})
	if entries := readRequestLog(t, srv); len(entries) != 0 {
		t.Errorf("refused CONNECTs reached the request log: %+v", entries)
	}

	t.Run("right token tunnels", func(t *testing.T) {
		resp, conn := sendCONNECTWithHeaders(t, srv.Addr(), target, proxyAuthHeader(testAuthToken))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("CONNECT status = %d, want 200", resp.StatusCode)
		}
		tlsConn := tls.Client(conn, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // test only
		if err := tlsConn.Handshake(); err != nil {
			t.Fatalf("TLS handshake through the tunnel: %v", err)
		}
		if _, err := io.WriteString(tlsConn, "GET / HTTP/1.1\r\nHost: "+target+"\r\nConnection: close\r\n\r\n"); err != nil {
			t.Fatalf("write in-tunnel request: %v", err)
		}
		if got := readStatusLine(t, tlsConn); !strings.Contains(got, " 200 ") {
			t.Errorf("in-tunnel response status line = %q, want 200", got)
		}
	})
	if entries := readRequestLog(t, srv); len(entries) != 1 {
		t.Errorf("request log has %d entries, want 1 (the authenticated CONNECT)", len(entries))
	}
}

// TestServerAuth_CONNECT_MITM covers the intercepted tunnel: the CONNECT is
// authenticated once and the requests decoded inside it - which carry no
// Proxy-Authorization of their own - are accepted on that. An unauthenticated
// CONNECT never opens, so an in-tunnel request on an unauthenticated path is
// impossible by construction.
//
// This is the goproxy tripwire named in CLAUDE.md: the in-tunnel acceptance
// rides on goproxy copying ctx.UserData from the CONNECT context into every
// per-request context, so the raw-TLS subtest sends two requests down one
// tunnel. A goproxy that copied it into the first request's context only
// would still pass a one-request tunnel and answer 407 to the second.
func TestServerAuth_CONNECT_MITM(t *testing.T) {
	upstream := &recordingUpstream{}
	ts := httptest.NewTLSServer(upstream.handler())
	defer ts.Close()
	target := strings.TrimPrefix(ts.URL, "https://")
	targetHost, _, _ := net.SplitHostPort(target)

	srv := startAuthProxy(t, newTestConfig(shortTempDir(t), 0), ts)

	t.Run("no credential", func(t *testing.T) {
		resp, conn := sendCONNECTWithHeaders(t, srv.Addr(), target, "")
		assertRefusedTunnel(t, resp, conn)
	})

	t.Run("in-tunnel requests need no header", func(t *testing.T) {
		resp, conn := sendCONNECTWithHeaders(t, srv.Addr(), target, proxyAuthHeader(testAuthToken))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("CONNECT status = %d, want 200", resp.StatusCode)
		}
		pool := x509.NewCertPool()
		pool.AddCert(srv.CA().Certificate)
		tlsConn := tls.Client(conn, &tls.Config{RootCAs: pool, ServerName: targetHost})
		if err := tlsConn.Handshake(); err != nil {
			t.Fatalf("TLS handshake with the MITM proxy: %v", err)
		}
		// No Proxy-Authorization on either request: the tunnel was
		// authenticated. Each response is read in full before the next
		// request is written, so the second status line is read cleanly.
		br := bufio.NewReader(tlsConn)
		for _, path := range []string{"/inside", "/inside-again"} {
			if _, err := io.WriteString(tlsConn, "GET "+path+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n"); err != nil {
				t.Fatalf("write in-tunnel request %s: %v", path, err)
			}
			inner, err := http.ReadResponse(br, nil)
			if err != nil {
				t.Fatalf("read in-tunnel response to %s: %v", path, err)
			}
			_, _ = io.Copy(io.Discard, inner.Body)
			_ = inner.Body.Close()
			if inner.StatusCode != http.StatusOK {
				t.Errorf("in-tunnel response to %s: status = %d, want 200", path, inner.StatusCode)
			}
		}
	})

	t.Run("http.Client round-trips", func(t *testing.T) {
		pool := x509.NewCertPool()
		pool.AddCert(srv.CA().Certificate)
		client := &http.Client{
			Transport: &http.Transport{
				Proxy:           http.ProxyURL(testProxyURL(srv)),
				TLSClientConfig: &tls.Config{RootCAs: pool},
			},
			Timeout: 5 * time.Second,
		}
		resp, err := client.Get(ts.URL + "/client")
		if err != nil {
			t.Fatalf("HTTPS request through the proxy: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
	})

	hits, auth := upstream.snapshot()
	if hits != 3 {
		t.Errorf("upstream saw %d requests, want 3", hits)
	}
	if len(auth) != 0 {
		t.Errorf("upstream received Proxy-Authorization %v", auth)
	}
}

// TestServer_Authorized pins what the credential check accepts: the session
// credential as a Basic payload under either spelling of the scheme, and
// nothing else.
func TestServer_Authorized(t *testing.T) {
	s := &Server{authCredential: []byte(proxyenv.AuthUser + ":" + testAuthToken)}
	encode := func(payload string) string {
		return base64.StdEncoding.EncodeToString([]byte(payload))
	}
	right := encode(proxyenv.AuthUser + ":" + testAuthToken)

	tests := []struct {
		name   string
		header string
		want   bool
	}{
		{name: "no header", header: "", want: false},
		{name: "scheme only", header: "Basic", want: false},
		{name: "bearer scheme", header: "Bearer " + right, want: false},
		{name: "malformed base64", header: "Basic %%%not-base64", want: false},
		{name: "empty payload", header: "Basic ", want: false},
		{name: "other username", header: "Basic " + encode("other:"+testAuthToken), want: false},
		{name: "wrong token", header: "Basic " + encode(proxyenv.AuthUser+":"+strings.Repeat("f", len(testAuthToken))), want: false},
		{name: "right credential", header: "Basic " + right, want: true},
		{name: "lowercase scheme", header: "basic " + right, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
			if tt.header != "" {
				req.Header.Set("Proxy-Authorization", tt.header)
			}
			if got := s.authorized(req); got != tt.want {
				t.Errorf("authorized(%q) = %v, want %v", tt.header, got, tt.want)
			}
		})
	}
}

// TestServerAuth_NoTokenInProxyRecords asserts the credential appears in
// nothing the proxy records or shows: the request log entry, the ask prompt
// and the audit events for an authenticated request.
func TestServerAuth_NoTokenInProxyRecords(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	dispatcher := logging.NewDispatcher()
	audit := &auditMemWriter{}
	dispatcher.AddWriter(audit)

	base := shortTempDir(t)
	cfg := newTestConfig(base, 0)
	cfg.MITM = false
	cfg.Dispatcher = dispatcher
	cfg.LogFilterDecisions = true
	cfg.Filter = &FilterConfig{DefaultAction: FilterActionAsk, AskTimeout: 5}
	srv := startAuthProxy(t, cfg, nil)

	var (
		mu    sync.Mutex
		asked []AskRequest
	)
	_, dec, enc := dialAsMonitor(t, AskSocketPath(base))
	go serveAsMonitor(dec, enc, func(req AskRequest) AskResponse {
		mu.Lock()
		asked = append(asked, req)
		mu.Unlock()
		return AskResponse{ID: req.ID, Action: FilterActionAllow}
	})
	waitForMonitor(t, srv, true)

	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(testProxyURL(srv))},
		Timeout:   10 * time.Second,
	}
	resp, err := client.Get(ts.URL + "/asked")
	if err != nil {
		t.Fatalf("request through proxy: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	mu.Lock()
	if len(asked) != 1 {
		t.Fatalf("monitor saw %d requests, want 1", len(asked))
	}
	assertNoToken(t, "ask prompt", asked[0])
	mu.Unlock()

	assertNoToken(t, "request log entry", waitForLoggedEntry(t, srv.config.LogDir))

	events := audit.snapshot()
	if len(events) == 0 {
		t.Fatal("no audit events emitted for an ask decision")
	}
	for _, e := range events {
		assertNoToken(t, "audit event "+e.Message, e)
	}
}

// assertRefusedTunnel checks a CONNECT was answered with the Basic challenge
// and that the connection carries nothing further: no tunnel opened.
func assertRefusedTunnel(t *testing.T, resp *http.Response, conn net.Conn) {
	t.Helper()
	if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("CONNECT status = %d, want 407", resp.StatusCode)
	}
	if got := resp.Header.Get("Proxy-Authenticate"); got != `Basic realm="devsandbox"` {
		t.Errorf("Proxy-Authenticate = %q, want the Basic challenge", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "proxy authentication required") {
		t.Errorf("body = %q, want the refusal reason", body)
	}
	// The proxy closes a refused CONNECT: the next read is EOF, never tunnel
	// bytes. A client that started a TLS handshake here would get nothing.
	if _, err := io.WriteString(conn, "\x16\x03\x01"); err == nil {
		var b [1]byte
		if n, err := conn.Read(b[:]); err == nil || n != 0 {
			t.Errorf("read %d bytes (err=%v) after a refused CONNECT: the tunnel opened", n, err)
		}
	}
}

// readStatusLine reads the status line of the response on r.
func readStatusLine(t *testing.T, r io.Reader) string {
	t.Helper()
	var line []byte
	var b [1]byte
	for {
		n, err := r.Read(b[:])
		if n == 1 {
			line = append(line, b[0])
			if b[0] == '\n' {
				break
			}
		}
		if err != nil {
			t.Fatalf("read status line: %v (got %q)", err, line)
		}
	}
	return string(line)
}

// assertNoToken serializes v as JSON and asserts the session credential is
// not in it.
func assertNoToken(t *testing.T, what string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %s: %v", what, err)
	}
	if strings.Contains(string(data), testAuthToken) {
		t.Errorf("%s contains the proxy credential: %s", what, data)
	}
	basic := base64.StdEncoding.EncodeToString([]byte(proxyenv.AuthUser + ":" + testAuthToken))
	if strings.Contains(string(data), basic) {
		t.Errorf("%s contains the Basic payload: %s", what, data)
	}
}
