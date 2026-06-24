package proxy

import (
	"bufio"
	"bytes"
	"crypto/sha1" //nolint:gosec // WebSocket handshake mandates SHA-1 (RFC 6455)
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewServer(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "proxy-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := NewConfig(tmpDir, 0) // Port 0 for random port

	server, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	if server.ca == nil {
		t.Error("server CA is nil")
	}

	if server.proxy == nil {
		t.Error("server proxy is nil")
	}
}

func TestServerStartStop(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "proxy-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := NewConfig(tmpDir, 18080)

	server, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	if err := server.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if !server.IsRunning() {
		t.Error("server should be running")
	}

	addr := server.Addr()
	if addr == "" {
		t.Error("server address is empty")
	}

	// Try starting again - should fail
	if err := server.Start(); err == nil {
		t.Error("starting twice should fail")
	}

	if err := server.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	if server.IsRunning() {
		t.Error("server should not be running")
	}
}

func TestServerHTTPProxy(t *testing.T) {
	// Start a test HTTP server
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "hello from test server")
	}))
	defer testServer.Close()

	// Start proxy
	tmpDir, err := os.MkdirTemp("", "proxy-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := NewConfig(tmpDir, 18081)

	proxyServer, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	if err := proxyServer.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = proxyServer.Stop() }()

	// Give the server a moment to start
	time.Sleep(100 * time.Millisecond)

	// Create HTTP client that uses proxy
	proxyURL, _ := url.Parse(fmt.Sprintf("http://%s", proxyServer.Addr()))
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
		Timeout: 5 * time.Second,
	}

	// Make request through proxy
	resp, err := client.Get(testServer.URL)
	if err != nil {
		t.Fatalf("request through proxy failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("unexpected status: %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from test server" {
		t.Errorf("unexpected body: %s", body)
	}
}

// trustUpstreamCert makes the proxy's upstream transport trust the given
// self-signed test server. goproxy v1.8.4 verifies upstream certificates by
// default (v1.8.3 skipped verification via its tlsClientSkipVerify config), so
// MITM tests that proxy to an httptest TLS server must register that server's
// cert with the proxy's upstream root pool. A fresh *tls.Config is assigned
// rather than mutating the shared default, so other proxies are unaffected.
func trustUpstreamCert(t *testing.T, proxyServer *Server, ts *httptest.Server) {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(ts.Certificate())
	proxyServer.proxy.Tr.TLSClientConfig = &tls.Config{RootCAs: pool}
}

func TestServerHTTPSProxy(t *testing.T) {
	// Start a test HTTPS server
	testServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "hello from TLS server")
	}))
	defer testServer.Close()

	// Start proxy
	tmpDir, err := os.MkdirTemp("", "proxy-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := NewConfig(tmpDir, 18082)

	proxyServer, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	trustUpstreamCert(t, proxyServer, testServer)

	if err := proxyServer.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = proxyServer.Stop() }()

	time.Sleep(100 * time.Millisecond)

	// Create HTTPS client that uses proxy and trusts our CA
	proxyURL, _ := url.Parse(fmt.Sprintf("http://%s", proxyServer.Addr()))

	// Trust both our CA and the test server's CA
	certPool := x509.NewCertPool()
	certPool.AddCert(proxyServer.CA().Certificate)
	certPool.AddCert(testServer.Certificate())

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				RootCAs:            certPool,
				InsecureSkipVerify: true, // For test server
			},
		},
		Timeout: 5 * time.Second,
	}

	// Make HTTPS request through proxy
	resp, err := client.Get(testServer.URL)
	if err != nil {
		t.Fatalf("HTTPS request through proxy failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("unexpected status: %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from TLS server" {
		t.Errorf("unexpected body: %s", body)
	}
}

func TestServerDynamicPortSelection(t *testing.T) {
	// Create two servers requesting the same port
	// The second should automatically get a different port

	tmpDir1, err := os.MkdirTemp("", "proxy-test-1-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir1) }()

	tmpDir2, err := os.MkdirTemp("", "proxy-test-2-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir2) }()

	// Both request port 18084
	requestedPort := 18084

	cfg1 := NewConfig(tmpDir1, requestedPort)
	server1, err := NewServer(cfg1)
	if err != nil {
		t.Fatalf("NewServer 1 failed: %v", err)
	}

	if err := server1.Start(); err != nil {
		t.Fatalf("Start 1 failed: %v", err)
	}
	defer func() { _ = server1.Stop() }()

	// Server 1 should get the requested port
	if server1.Port() != requestedPort {
		t.Errorf("server1 should get requested port %d, got %d", requestedPort, server1.Port())
	}

	// Now start second server requesting same port
	cfg2 := NewConfig(tmpDir2, requestedPort)
	server2, err := NewServer(cfg2)
	if err != nil {
		t.Fatalf("NewServer 2 failed: %v", err)
	}

	if err := server2.Start(); err != nil {
		t.Fatalf("Start 2 failed: %v", err)
	}
	defer func() { _ = server2.Stop() }()

	// Server 2 should get a different port
	if server2.Port() == requestedPort {
		t.Errorf("server2 should get different port than %d", requestedPort)
	}

	// Server 2 should get the next port
	if server2.Port() != requestedPort+1 {
		t.Errorf("server2 should get port %d, got %d", requestedPort+1, server2.Port())
	}

	t.Logf("Server 1 port: %d, Server 2 port: %d", server1.Port(), server2.Port())
}

func TestNewServer_NoMITM(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "proxy-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := NewConfig(tmpDir, 0)
	cfg.MITM = false

	server, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer with MITM=false failed: %v", err)
	}

	if server.ca != nil {
		t.Error("CA should be nil when MITM is disabled")
	}
	if server.proxy == nil {
		t.Error("proxy should still be created")
	}
}

func TestServerHTTPS_NoMITM_Tunnels(t *testing.T) {
	// Skip when running inside an environment with a transparent/system proxy.
	// CONNECT tunnel mode uses raw TCP which gets intercepted by the outer proxy,
	// causing the test to fail with EOF. MITM tests work because goproxy uses HTTP
	// transport (which chains through the system proxy correctly).
	if os.Getenv("HTTP_PROXY") != "" || os.Getenv("http_proxy") != "" {
		t.Skip("skipping: CONNECT tunnel test cannot run behind a system proxy")
	}

	// Start a test HTTPS server
	testServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "hello from TLS tunnel")
	}))
	defer testServer.Close()

	// Start proxy with MITM disabled
	tmpDir, err := os.MkdirTemp("", "proxy-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := NewConfig(tmpDir, 18083)
	cfg.MITM = false

	proxyServer, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	if err := proxyServer.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = proxyServer.Stop() }()

	time.Sleep(100 * time.Millisecond)

	// Create HTTPS client that uses proxy — no proxy CA needed since MITM is off.
	// InsecureSkipVerify is used because httptest.NewTLSServer uses a self-signed cert;
	// the test verifies tunnel functionality, not certificate validation.
	proxyURL, _ := url.Parse(fmt.Sprintf("http://%s", proxyServer.Addr()))

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // test only
			},
		},
		Timeout: 5 * time.Second,
	}

	// Make HTTPS request — should tunnel through without MITM
	resp, err := client.Get(testServer.URL)
	if err != nil {
		t.Fatalf("HTTPS request through tunnel failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("unexpected status: %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from TLS tunnel" {
		t.Errorf("unexpected body: %s", body)
	}
}

// TestServerHEAD_PreservesContentLength is a regression test for a bug where
// the proxy stripped the upstream Content-Length header from HEAD responses
// and substituted Transfer-Encoding: chunked. That breaks OCI/registry clients
// (oras-go, helm, crane, BuildKit, skopeo) which rely on HEAD for manifest
// size validation per RFC 9110 §9.3.2. The root cause was LogResponse
// replacing resp.Body with a fresh NopCloser; goproxy detects the body
// identity changed and drops Content-Length, after which Go's net/http
// switches to chunked encoding. The fix skips body replacement for HEAD.
func TestServerHEAD_PreservesContentLength(t *testing.T) {
	const upstreamLen = "1048576"

	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate an OCI registry manifest endpoint: HEAD returns headers
		// that describe what the eventual GET would return.
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.Header().Set("Docker-Content-Digest", "sha256:deadbeef")
		w.Header().Set("Content-Length", upstreamLen)
		w.WriteHeader(http.StatusOK)
	}))
	defer testServer.Close()

	tmpDir, err := os.MkdirTemp("", "proxy-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := NewConfig(tmpDir, 18085)
	proxyServer, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	if err := proxyServer.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = proxyServer.Stop() }()

	time.Sleep(100 * time.Millisecond)

	proxyURL, _ := url.Parse(fmt.Sprintf("http://%s", proxyServer.Addr()))
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
		Timeout: 5 * time.Second,
	}

	req, err := http.NewRequest(http.MethodHead, testServer.URL+"/v2/repo/manifests/v1", nil)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HEAD through proxy failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("unexpected status: %d", resp.StatusCode)
	}

	// Critical assertion: Content-Length must equal what upstream sent.
	if got := resp.Header.Get("Content-Length"); got != upstreamLen {
		t.Errorf("Content-Length: got %q, want %q (upstream value must be preserved verbatim)", got, upstreamLen)
	}

	// Critical assertion: must NOT be chunked. resp.TransferEncoding is the
	// authoritative field — Go's transport already consumed the header.
	for _, te := range resp.TransferEncoding {
		if te == "chunked" {
			t.Errorf("HEAD response was chunk-encoded; upstream Content-Length must be preserved instead")
		}
	}

	// resp.ContentLength should also reflect the upstream value.
	if resp.ContentLength != 1048576 {
		t.Errorf("resp.ContentLength: got %d, want 1048576", resp.ContentLength)
	}

	// Header passthrough sanity check.
	if got := resp.Header.Get("Docker-Content-Digest"); got != "sha256:deadbeef" {
		t.Errorf("Docker-Content-Digest: got %q, want %q", got, "sha256:deadbeef")
	}
}

// TestServerHEAD_PreservesContentLength_MITM is the same regression test but
// over HTTPS with MITM enabled — the actual configuration where the bug was
// reported (devsandbox MITM proxy in front of an OCI registry). HTTPS MITM
// runs through the same filterResponse code path as plain HTTP, so the fix
// must hold here too.
func TestServerHEAD_PreservesContentLength_MITM(t *testing.T) {
	const upstreamLen = "524"

	testServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.Header().Set("Docker-Content-Digest", "sha256:cafebabe")
		w.Header().Set("Content-Length", upstreamLen)
		w.WriteHeader(http.StatusOK)
	}))
	defer testServer.Close()

	tmpDir, err := os.MkdirTemp("", "proxy-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := NewConfig(tmpDir, 18086)
	proxyServer, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	trustUpstreamCert(t, proxyServer, testServer)
	if err := proxyServer.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = proxyServer.Stop() }()

	time.Sleep(100 * time.Millisecond)

	proxyURL, _ := url.Parse(fmt.Sprintf("http://%s", proxyServer.Addr()))

	certPool := x509.NewCertPool()
	certPool.AddCert(proxyServer.CA().Certificate)
	certPool.AddCert(testServer.Certificate())

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				RootCAs:            certPool,
				InsecureSkipVerify: true, //nolint:gosec // test only
			},
		},
		Timeout: 5 * time.Second,
	}

	req, err := http.NewRequest(http.MethodHead, testServer.URL+"/v2/repo/manifests/v1", nil)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HEAD through MITM proxy failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("unexpected status: %d", resp.StatusCode)
	}

	if got := resp.Header.Get("Content-Length"); got != upstreamLen {
		t.Errorf("Content-Length: got %q, want %q (upstream value must be preserved verbatim)", got, upstreamLen)
	}

	for _, te := range resp.TransferEncoding {
		if te == "chunked" {
			t.Errorf("HEAD response was chunk-encoded; upstream Content-Length must be preserved instead")
		}
	}

	if resp.ContentLength != 524 {
		t.Errorf("resp.ContentLength: got %d, want 524", resp.ContentLength)
	}

	if got := resp.Header.Get("Docker-Content-Digest"); got != "sha256:cafebabe" {
		t.Errorf("Docker-Content-Digest: got %q, want %q", got, "sha256:cafebabe")
	}
}

// TestServerSSE_StreamsHeadersWithoutBuffering is a regression test for codex
// (and other SSE clients) failing through the MITM proxy with "Codex SSE
// response headers timed out after 20000ms". The root cause was LogResponse
// calling io.ReadAll on the response body inside goproxy's OnResponse handler:
// goproxy relays the response to the client only after OnResponse returns, so
// reading a long-lived text/event-stream body blocks until the stream closes
// and the client never receives the response headers. The fix passes streaming
// bodies through untouched.
//
// The test server holds the SSE stream open for far longer than the client is
// willing to wait for headers. With the bug, client.Do never returns in time;
// with the fix, headers arrive immediately and the first event streams through
// while the server still holds the connection open.
func TestServerSSE_StreamsHeadersWithoutBuffering(t *testing.T) {
	release := make(chan struct{})

	testServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("test server ResponseWriter does not support flushing")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher.Flush() // send headers immediately

		_, _ = io.WriteString(w, "data: first\n\n")
		flusher.Flush() // send first event, then hold the stream open

		select {
		case <-release:
		case <-time.After(10 * time.Second): // safety net so the goroutine never leaks
		}
		_, _ = io.WriteString(w, "data: second\n\n")
		flusher.Flush()
	}))
	defer testServer.Close()
	defer close(release)

	tmpDir, err := os.MkdirTemp("", "proxy-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := NewConfig(tmpDir, 18087)
	proxyServer, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	trustUpstreamCert(t, proxyServer, testServer)
	if err := proxyServer.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = proxyServer.Stop() }()

	time.Sleep(100 * time.Millisecond)

	proxyURL, _ := url.Parse(fmt.Sprintf("http://%s", proxyServer.Addr()))
	certPool := x509.NewCertPool()
	certPool.AddCert(proxyServer.CA().Certificate)
	certPool.AddCert(testServer.Certificate())

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				RootCAs:            certPool,
				InsecureSkipVerify: true, //nolint:gosec // test only
			},
		},
		// No client.Timeout: it would also bound the (intentionally long-lived)
		// body read. We bound header delivery explicitly via the select below.
	}

	req, err := http.NewRequest(http.MethodGet, testServer.URL+"/v1/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}

	type doResult struct {
		resp *http.Response
		err  error
	}
	done := make(chan doResult, 1)
	go func() {
		resp, err := client.Do(req)
		done <- doResult{resp, err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("SSE request through MITM proxy failed: %v", res.err)
		}
		defer func() { _ = res.resp.Body.Close() }()

		if got := res.resp.Header.Get("Content-Type"); got != "text/event-stream" {
			t.Errorf("Content-Type: got %q, want text/event-stream", got)
		}

		// The first event must arrive while the server still holds the stream
		// open (release has not been signalled), proving incremental delivery.
		lineCh := make(chan string, 1)
		go func() {
			line, _ := bufio.NewReader(res.resp.Body).ReadString('\n')
			lineCh <- line
		}()
		select {
		case line := <-lineCh:
			if !strings.Contains(line, "first") {
				t.Errorf("first streamed event: got %q, want it to contain %q", line, "first")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out reading first SSE event; proxy is buffering the stream body")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client.Do did not return within 3s; proxy is buffering SSE response headers")
	}
}

// TestServerStreaming_EmptyContentTypeNotBuffered reproduces the codex failure
// captured in the field: codex's chatgpt.com/backend-api/codex/responses
// streams a response with an EMPTY Content-Type. Earlier media-type-based
// detection did not recognize it as a stream, so the proxy buffered the body
// for 10-80s before relaying headers and codex aborted with "SSE response
// headers timed out after 20000ms". The proxy must stream it through and
// deliver headers immediately regardless of Content-Type.
func TestServerStreaming_EmptyContentTypeNotBuffered(t *testing.T) {
	release := make(chan struct{})

	testServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("test server ResponseWriter does not support flushing")
			return
		}
		// Deliberately no Content-Type, like codex's streamed responses.
		w.WriteHeader(http.StatusOK)
		flusher.Flush() // headers out immediately

		_, _ = io.WriteString(w, "chunk-1\n")
		flusher.Flush() // first chunk, then hold the stream open

		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
		_, _ = io.WriteString(w, "chunk-2\n")
		flusher.Flush()
	}))
	defer testServer.Close()
	defer close(release)

	tmpDir, err := os.MkdirTemp("", "proxy-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := NewConfig(tmpDir, 18089)
	proxyServer, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	trustUpstreamCert(t, proxyServer, testServer)
	if err := proxyServer.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = proxyServer.Stop() }()

	time.Sleep(100 * time.Millisecond)

	proxyURL, _ := url.Parse(fmt.Sprintf("http://%s", proxyServer.Addr()))
	certPool := x509.NewCertPool()
	certPool.AddCert(proxyServer.CA().Certificate)
	certPool.AddCert(testServer.Certificate())

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				RootCAs:            certPool,
				InsecureSkipVerify: true, //nolint:gosec // test only
			},
		},
	}

	req, _ := http.NewRequest(http.MethodPost, testServer.URL+"/backend-api/codex/responses", nil)

	type doResult struct {
		resp *http.Response
		err  error
	}
	done := make(chan doResult, 1)
	go func() {
		resp, err := client.Do(req)
		done <- doResult{resp, err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("request through MITM proxy failed: %v", res.err)
		}
		defer func() { _ = res.resp.Body.Close() }()

		lineCh := make(chan string, 1)
		go func() {
			line, _ := bufio.NewReader(res.resp.Body).ReadString('\n')
			lineCh <- line
		}()
		select {
		case line := <-lineCh:
			if !strings.Contains(line, "chunk-1") {
				t.Errorf("first streamed chunk: got %q, want it to contain %q", line, "chunk-1")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out reading first chunk; proxy is buffering the stream body")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client.Do did not return within 3s; proxy is buffering response headers (empty Content-Type stream)")
	}
}

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// wsAcceptKey computes the Sec-WebSocket-Accept value for a given
// Sec-WebSocket-Key, per RFC 6455 §4.2.2.
func wsAcceptKey(key string) string {
	h := sha1.New() //nolint:gosec // RFC 6455 mandates SHA-1 for the accept key
	_, _ = io.WriteString(h, key+wsGUID)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// wsWriteFrame writes a single unfragmented text frame (RFC 6455). Client frames
// must be masked; server frames must not be. Payloads are limited to 125 bytes,
// which is all these tests need.
func wsWriteFrame(w io.Writer, payload []byte, mask bool) error {
	if len(payload) > 125 {
		return errors.New("wsWriteFrame: payload too large for test helper")
	}
	frame := []byte{0x81} // FIN + text opcode
	if mask {
		frame = append(frame, 0x80|byte(len(payload)))
		maskKey := []byte{0xAA, 0xBB, 0xCC, 0xDD}
		frame = append(frame, maskKey...)
		for i, b := range payload {
			frame = append(frame, b^maskKey[i%4])
		}
	} else {
		frame = append(frame, byte(len(payload)))
		frame = append(frame, payload...)
	}
	_, err := w.Write(frame)
	return err
}

// wsReadFrame reads a single text frame (RFC 6455), unmasking if needed.
func wsReadFrame(r io.Reader) ([]byte, error) {
	h := make([]byte, 2)
	if _, err := io.ReadFull(r, h); err != nil {
		return nil, err
	}
	masked := h[1]&0x80 != 0
	n := int(h[1] & 0x7f)
	if n > 125 {
		return nil, errors.New("wsReadFrame: extended length not supported in test helper")
	}
	var maskKey []byte
	if masked {
		maskKey = make([]byte, 4)
		if _, err := io.ReadFull(r, maskKey); err != nil {
			return nil, err
		}
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return payload, nil
}

// wsEchoHandler upgrades to WebSocket and echoes the first text frame it
// receives. It hijacks the connection and speaks raw RFC 6455 so the test needs
// no WebSocket dependency.
func wsEchoHandler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !headerHasToken(r.Header.Get("Connection"), "upgrade") ||
			!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "expected websocket upgrade", http.StatusBadRequest)
			return
		}
		key := r.Header.Get("Sec-WebSocket-Key")
		if key == "" {
			http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
			return
		}

		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("server ResponseWriter does not support hijacking")
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + wsAcceptKey(key) + "\r\n\r\n")
		if err := buf.Flush(); err != nil {
			return
		}

		payload, err := wsReadFrame(buf)
		if err != nil {
			return
		}
		if err := wsWriteFrame(buf, payload, false); err != nil {
			return
		}
		_ = buf.Flush()
	}
}

func headerHasToken(header, token string) bool {
	for _, part := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

// TestServerWebSocket_MITMTunnel drives a real WebSocket (WSS) through the MITM
// proxy: CONNECT, TLS interception, the HTTP Upgrade handshake (101), and a
// bidirectional echo over the upgraded connection. This is the codex
// `status=101` upgrade case from the field logs. The fix that stops buffering
// response bodies is what makes this work: goproxy relays a WebSocket by
// type-asserting resp.Body to io.ReadWriter after our OnResponse handler runs;
// the old code read the 101 body with io.ReadAll and replaced it with a
// bytes.Reader, which both stalled the stream and failed the assertion
// ("Unable to use Websocket connection").
func TestServerWebSocket_MITMTunnel(t *testing.T) {
	testServer := httptest.NewTLSServer(wsEchoHandler(t))
	defer testServer.Close()

	tmpDir, err := os.MkdirTemp("", "proxy-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := NewConfig(tmpDir, 18090)
	proxyServer, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	trustUpstreamCert(t, proxyServer, testServer)
	if err := proxyServer.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = proxyServer.Stop() }()

	time.Sleep(100 * time.Millisecond)

	proxyURL, _ := url.Parse(fmt.Sprintf("http://%s", proxyServer.Addr()))
	certPool := x509.NewCertPool()
	certPool.AddCert(proxyServer.CA().Certificate)
	certPool.AddCert(testServer.Certificate())

	// Transport drives the CONNECT tunnel + MITM TLS, and (since Go 1.12)
	// returns resp.Body as an io.ReadWriteCloser for a 101 upgrade response.
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{
			RootCAs:            certPool,
			InsecureSkipVerify: true, //nolint:gosec // test only
		},
	}
	defer transport.CloseIdleConnections()

	const wsKey = "dGhlIHNhbXBsZSBub25jZQ==" // RFC 6455 example 16-byte key
	req, err := http.NewRequest(http.MethodGet, testServer.URL+"/ws", nil)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Key", wsKey)
	req.Header.Set("Sec-WebSocket-Version", "13")

	type result struct {
		got string
		err error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := transport.RoundTrip(req)
		if err != nil {
			done <- result{err: fmt.Errorf("websocket handshake through MITM proxy failed: %w", err)}
			return
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusSwitchingProtocols {
			done <- result{err: fmt.Errorf("status: got %d, want 101", resp.StatusCode)}
			return
		}
		if got := resp.Header.Get("Sec-WebSocket-Accept"); got != wsAcceptKey(wsKey) {
			done <- result{err: fmt.Errorf("Sec-WebSocket-Accept: got %q, want %q", got, wsAcceptKey(wsKey))}
			return
		}
		conn, ok := resp.Body.(io.ReadWriteCloser)
		if !ok {
			done <- result{err: errors.New("resp.Body is not io.ReadWriteCloser; proxy broke the upgraded connection")}
			return
		}
		if err := wsWriteFrame(conn, []byte("hello websocket"), true); err != nil {
			done <- result{err: fmt.Errorf("write frame: %w", err)}
			return
		}
		echo, err := wsReadFrame(conn)
		if err != nil {
			done <- result{err: fmt.Errorf("read echo frame: %w", err)}
			return
		}
		done <- result{got: string(echo)}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatal(res.err)
		}
		if res.got != "hello websocket" {
			t.Errorf("echoed message: got %q, want %q", res.got, "hello websocket")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WebSocket exchange did not complete within 5s; proxy is not tunneling the upgrade")
	}
}

// TestServerWebSocket_PlainHTTPNonMITM drives a plain ws:// WebSocket through
// the proxy with MITM disabled. Plain HTTP never uses CONNECT, so this goes
// through goproxy's HTTP handler (http.go) rather than the MITM path
// (https.go), but the upgrade relay is the same: filterResponse (our
// OnResponse) runs first, then goproxy type-asserts resp.Body to io.ReadWriter.
// The response-body streaming fix must keep the 101 body untouched here too.
func TestServerWebSocket_PlainHTTPNonMITM(t *testing.T) {
	testServer := httptest.NewServer(wsEchoHandler(t)) // plain HTTP, not TLS
	defer testServer.Close()

	tmpDir, err := os.MkdirTemp("", "proxy-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := NewConfig(tmpDir, 18091)
	cfg.MITM = false // transparent mode: no HTTPS interception
	proxyServer, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	if err := proxyServer.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = proxyServer.Stop() }()

	time.Sleep(100 * time.Millisecond)

	proxyURL, _ := url.Parse(fmt.Sprintf("http://%s", proxyServer.Addr()))
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()

	const wsKey = "dGhlIHNhbXBsZSBub25jZQ==" // RFC 6455 example 16-byte key
	req, err := http.NewRequest(http.MethodGet, testServer.URL+"/ws", nil)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Key", wsKey)
	req.Header.Set("Sec-WebSocket-Version", "13")

	type result struct {
		got string
		err error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := transport.RoundTrip(req)
		if err != nil {
			done <- result{err: fmt.Errorf("websocket handshake through proxy failed: %w", err)}
			return
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusSwitchingProtocols {
			done <- result{err: fmt.Errorf("status: got %d, want 101", resp.StatusCode)}
			return
		}
		if got := resp.Header.Get("Sec-WebSocket-Accept"); got != wsAcceptKey(wsKey) {
			done <- result{err: fmt.Errorf("Sec-WebSocket-Accept: got %q, want %q", got, wsAcceptKey(wsKey))}
			return
		}
		conn, ok := resp.Body.(io.ReadWriteCloser)
		if !ok {
			done <- result{err: errors.New("resp.Body is not io.ReadWriteCloser; proxy broke the upgraded connection")}
			return
		}
		if err := wsWriteFrame(conn, []byte("hello ws plain"), true); err != nil {
			done <- result{err: fmt.Errorf("write frame: %w", err)}
			return
		}
		echo, err := wsReadFrame(conn)
		if err != nil {
			done <- result{err: fmt.Errorf("read echo frame: %w", err)}
			return
		}
		done <- result{got: string(echo)}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatal(res.err)
		}
		if res.got != "hello ws plain" {
			t.Errorf("echoed message: got %q, want %q", res.got, "hello ws plain")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WebSocket exchange did not complete within 5s; proxy is not tunneling the plain ws:// upgrade")
	}
}

func TestStripQuery(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"https://api.openai.com/v1/responses", "https://api.openai.com/v1/responses"},
		{"https://api.openai.com/v1/responses?token=secret", "https://api.openai.com/v1/responses"},
		{"https://h/p?a=1&b=2", "https://h/p"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := stripQuery(tt.in); got != tt.want {
			t.Errorf("stripQuery(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// syncBuffer is a concurrency-safe io.Writer for capturing proxy.Logger output
// written from goproxy's worker goroutines.
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

// TestServerDebugLogging verifies that DEVSANDBOX_DEBUG emits the per-request
// lifecycle diagnostics used to investigate the codex SSE timeout: a CONNECT
// line (interception confirmed), a request line, and a response line carrying
// the content-type and streaming detection. This is the output a user inspects
// via `devsandbox logs internal --type proxy`.
func TestServerDebugLogging(t *testing.T) {
	t.Setenv("DEVSANDBOX_DEBUG", "1")

	testServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: hi\n\n")
	}))
	defer testServer.Close()

	tmpDir, err := os.MkdirTemp("", "proxy-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := NewConfig(tmpDir, 18088)
	proxyServer, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	if !proxyServer.debug {
		t.Fatal("expected debug mode enabled via DEVSANDBOX_DEBUG")
	}
	// Redirect the internal proxy log to an in-memory buffer for assertion.
	buf := &syncBuffer{}
	proxyServer.proxy.Logger = log.New(buf, "", 0)

	trustUpstreamCert(t, proxyServer, testServer)
	if err := proxyServer.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = proxyServer.Stop() }()

	time.Sleep(100 * time.Millisecond)

	proxyURL, _ := url.Parse(fmt.Sprintf("http://%s", proxyServer.Addr()))
	certPool := x509.NewCertPool()
	certPool.AddCert(proxyServer.CA().Certificate)
	certPool.AddCert(testServer.Certificate())

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				RootCAs:            certPool,
				InsecureSkipVerify: true, //nolint:gosec // test only
			},
		},
		Timeout: 5 * time.Second,
	}

	req, _ := http.NewRequest(http.MethodGet, testServer.URL+"/v1/responses?token=secret", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request through MITM proxy failed: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	out := buf.String()
	wantContains := []string{
		"DEBUG CONNECT",
		"DEBUG request: GET",
		"DEBUG response: GET",
		"status=200",
		`content-type="text/event-stream"`,
		"streaming=true",
	}
	for _, w := range wantContains {
		if !strings.Contains(out, w) {
			t.Errorf("debug log missing %q\nfull log:\n%s", w, out)
		}
	}
	// Query string carrying a token must never appear in debug logs.
	if strings.Contains(out, "token=secret") {
		t.Errorf("debug log leaked query token:\n%s", out)
	}
}

func TestServer_CredentialInjector_SpecificityOrder(t *testing.T) {
	cfg := map[string]any{
		"specific": map[string]any{
			"enabled":      true,
			"host":         "api.github.com",
			"header":       "X-Specific",
			"value_format": "specific-{token}",
			"source":       map[string]any{"value": "S"},
		},
		"wide": map[string]any{
			"enabled":      true,
			"host":         "*.github.com",
			"header":       "X-Wide",
			"value_format": "wide-{token}",
			"source":       map[string]any{"value": "W"},
		},
	}
	injs, err := BuildCredentialInjectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Mirror server.go's first-match-wins loop on the pre-sorted slice.
	req := httptest.NewRequest("GET", "https://api.github.com/user", nil)
	for _, inj := range injs {
		if inj.Match(req) {
			inj.Inject(req)
			break
		}
	}
	if req.Header.Get("X-Specific") != "specific-S" {
		t.Errorf("specific injector should win for api.github.com; got X-Specific=%q X-Wide=%q",
			req.Header.Get("X-Specific"), req.Header.Get("X-Wide"))
	}
	if req.Header.Get("X-Wide") != "" {
		t.Errorf("wide injector must not run when specific matched")
	}
}
