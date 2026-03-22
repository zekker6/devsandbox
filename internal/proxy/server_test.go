package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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
