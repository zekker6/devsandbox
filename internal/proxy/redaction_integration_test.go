package proxy

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// redactionTestEnv holds a running proxy and client for integration tests.
type redactionTestEnv struct {
	server *Server
	client *http.Client
}

// setupRedactionTest creates a proxy server with the given redaction config and an HTTP client that routes through it.
func setupRedactionTest(t *testing.T, redaction *RedactionConfig) *redactionTestEnv {
	t.Helper()
	tmpDir := t.TempDir()

	cfg := NewConfig(tmpDir, 0)
	cfg.Redaction = redaction

	server, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop() })

	time.Sleep(100 * time.Millisecond)

	proxyURL, _ := url.Parse(fmt.Sprintf("http://%s", server.Addr()))
	certPool := x509.NewCertPool()
	certPool.AddCert(server.CA().Certificate)

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				RootCAs: certPool,
			},
		},
		Timeout: 5 * time.Second,
	}

	return &redactionTestEnv{server: server, client: client}
}

func TestRedactionIntegration_BlockSecretInBody(t *testing.T) {
	// Local upstream server — the proxy should block before reaching it
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream was reached; redaction should have blocked the request")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	tmpDir := t.TempDir()

	cfg := NewConfig(tmpDir, 0)
	cfg.Redaction = &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "test-secret", Source: &RedactionSource{Value: "super-secret-value-123"}},
		},
	}

	server, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = server.Stop() }()

	time.Sleep(100 * time.Millisecond)

	proxyURL, _ := url.Parse(fmt.Sprintf("http://%s", server.Addr()))

	certPool := x509.NewCertPool()
	certPool.AddCert(server.CA().Certificate)

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				RootCAs: certPool,
			},
		},
		Timeout: 5 * time.Second,
	}

	// Send request with secret in body — should be blocked before forwarding
	body := bytes.NewReader([]byte(`{"data": "super-secret-value-123"}`))
	resp, err := client.Post(upstream.URL+"/post", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}

	respBody, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(respBody, []byte("secret pattern detected")) {
		t.Errorf("response body = %q, want to contain 'secret pattern detected'", respBody)
	}
}

func TestRedactionIntegration_NoSecretAllowed(t *testing.T) {
	// Local upstream server — clean requests should reach it
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status": "ok"}`))
	}))
	defer upstream.Close()

	tmpDir := t.TempDir()

	cfg := NewConfig(tmpDir, 0)
	cfg.Redaction = &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionBlock,
		Rules: []RedactionRule{
			{Name: "test-secret", Source: &RedactionSource{Value: "super-secret-value-123"}},
		},
	}

	server, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = server.Stop() }()

	time.Sleep(100 * time.Millisecond)

	proxyURL, _ := url.Parse(fmt.Sprintf("http://%s", server.Addr()))

	certPool := x509.NewCertPool()
	certPool.AddCert(server.CA().Certificate)

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				RootCAs: certPool,
			},
		},
		Timeout: 5 * time.Second,
	}

	// Send clean request (no secret)
	body := bytes.NewReader([]byte(`{"data": "nothing sensitive here"}`))
	resp, err := client.Post(upstream.URL+"/post", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Should NOT be blocked
	if resp.StatusCode == http.StatusForbidden {
		t.Error("clean request was blocked, expected to pass through")
	}
}

func TestRedactionIntegration_RedactAction(t *testing.T) {
	secret := "super-secret-value-123"

	var receivedBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		receivedBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	env := setupRedactionTest(t, &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionRedact,
		Rules: []RedactionRule{
			{Name: "test-secret", Source: &RedactionSource{Value: secret}},
		},
	})

	body := bytes.NewReader([]byte(`{"token": "` + secret + `"}`))
	resp, err := env.client.Post(upstream.URL+"/post", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Should NOT be blocked — redact action forwards the request
	if resp.StatusCode == http.StatusForbidden {
		t.Error("redact action should not block, got 403")
	}
	// Upstream should have received redacted body
	if strings.Contains(receivedBody, secret) {
		t.Error("upstream received original secret — should have been redacted")
	}
	if !strings.Contains(receivedBody, "[REDACTED:test-secret]") {
		t.Errorf("upstream body missing redaction placeholder, got: %s", receivedBody)
	}
}

func TestRedactionIntegration_LogAction(t *testing.T) {
	secret := "super-secret-value-123"

	var receivedBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		receivedBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	env := setupRedactionTest(t, &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionLog,
		Rules: []RedactionRule{
			{Name: "test-secret", Source: &RedactionSource{Value: secret}},
		},
	})

	body := bytes.NewReader([]byte(`{"token": "` + secret + `"}`))
	resp, err := env.client.Post(upstream.URL+"/post", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusForbidden {
		t.Error("log action should not block, got 403")
	}
	// Upstream should receive original unmodified body (log = observe only)
	if !strings.Contains(receivedBody, secret) {
		t.Errorf("upstream should receive original body with secret for log action, got: %s", receivedBody)
	}
}

func TestRedactionIntegration_RedactSecretInURL(t *testing.T) {
	secret := "super-secret-value-123"

	var receivedURL string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedURL = r.URL.String()
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	env := setupRedactionTest(t, &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionRedact,
		Rules: []RedactionRule{
			{Name: "test-secret", Source: &RedactionSource{Value: secret}},
		},
	})

	resp, err := env.client.Get(upstream.URL + "/data?key=" + secret)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusForbidden {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("redact action should not block, got 403: %s", respBody)
	}
	if strings.Contains(receivedURL, secret) {
		t.Error("upstream received original secret in URL — should have been redacted")
	}
	if !strings.Contains(receivedURL, "[REDACTED:test-secret]") {
		t.Errorf("upstream URL missing redaction placeholder, got: %s", receivedURL)
	}
}

func TestRedactionIntegration_RedactSecretInHeader(t *testing.T) {
	secret := "super-secret-value-123"

	var receivedHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("X-Custom")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	env := setupRedactionTest(t, &RedactionConfig{
		Enabled:       boolPtr(true),
		DefaultAction: RedactionActionRedact,
		Rules: []RedactionRule{
			{Name: "test-secret", Source: &RedactionSource{Value: secret}},
		},
	})

	req, _ := http.NewRequest("GET", upstream.URL+"/data", nil)
	req.Header.Set("X-Custom", secret)
	resp, err := env.client.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusForbidden {
		t.Error("redact action should not block, got 403")
	}
	if strings.Contains(receivedHeader, secret) {
		t.Error("upstream received original secret in header — should have been redacted")
	}
	if !strings.Contains(receivedHeader, "[REDACTED:test-secret]") {
		t.Errorf("upstream header missing redaction placeholder, got: %s", receivedHeader)
	}
}
