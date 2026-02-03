package dockerproxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProxy_StartStop(t *testing.T) {
	tmpDir := t.TempDir()
	listenPath := filepath.Join(tmpDir, "docker.sock")

	// Create a fake host socket
	hostPath := filepath.Join(tmpDir, "host.sock")
	hostListener, err := net.Listen("unix", hostPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hostListener.Close() }()

	p := New(hostPath, listenPath)

	ctx := context.Background()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Verify socket was created
	if _, err := os.Stat(listenPath); err != nil {
		t.Errorf("listen socket not created: %v", err)
	}

	if err := p.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	// Give time for cleanup
	time.Sleep(10 * time.Millisecond)
}

func TestProxy_ForwardsGET(t *testing.T) {
	tmpDir := t.TempDir()
	listenPath := filepath.Join(tmpDir, "docker.sock")
	hostPath := filepath.Join(tmpDir, "host.sock")

	// Create a fake Docker daemon
	hostListener, err := net.Listen("unix", hostPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hostListener.Close() }()

	// Handle one request
	go func() {
		conn, err := hostListener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// Read request and send response
		buf := make([]byte, 1024)
		_, _ = conn.Read(buf)
		response := "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n[]"
		_, _ = conn.Write([]byte(response))
	}()

	p := New(hostPath, listenPath)
	ctx := context.Background()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = p.Stop() }()

	// Make a request through the proxy
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", listenPath)
			},
		},
	}

	resp, err := client.Get("http://localhost/containers/json")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "[]" {
		t.Errorf("expected [], got %q", body)
	}
}

func TestProxy_BlocksPOST(t *testing.T) {
	tmpDir := t.TempDir()
	listenPath := filepath.Join(tmpDir, "docker.sock")
	hostPath := filepath.Join(tmpDir, "host.sock")

	// Create a fake Docker daemon (should not receive request)
	hostListener, err := net.Listen("unix", hostPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hostListener.Close() }()

	p := New(hostPath, listenPath)
	ctx := context.Background()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = p.Stop() }()

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", listenPath)
			},
		},
	}

	resp, err := client.Post("http://localhost/containers/create", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}
