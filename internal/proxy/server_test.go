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
	defer os.RemoveAll(tmpDir)

	cfg := NewConfig(tmpDir, 0, false) // Port 0 for random port

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
	defer os.RemoveAll(tmpDir)

	cfg := NewConfig(tmpDir, 18080, false)

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
		fmt.Fprint(w, "hello from test server")
	}))
	defer testServer.Close()

	// Start proxy
	tmpDir, err := os.MkdirTemp("", "proxy-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := NewConfig(tmpDir, 18081, false)

	proxyServer, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	if err := proxyServer.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer proxyServer.Stop()

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
	defer resp.Body.Close()

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
		fmt.Fprint(w, "hello from TLS server")
	}))
	defer testServer.Close()

	// Start proxy
	tmpDir, err := os.MkdirTemp("", "proxy-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := NewConfig(tmpDir, 18082, false)

	proxyServer, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	if err := proxyServer.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer proxyServer.Stop()

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
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("unexpected status: %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from TLS server" {
		t.Errorf("unexpected body: %s", body)
	}
}

func TestServerWithLogging(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "proxy-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := NewConfig(tmpDir, 18083, true) // Enable logging

	server, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	if server.logger == nil {
		t.Error("logger should be set when logging enabled")
	}

	if err := server.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer server.Stop()
}
