package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"testing"
	"time"
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

func TestAskServer_ServerMode(t *testing.T) {
	dir := shortTempDir(t)

	server, err := NewAskServer(dir)
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
	socketDir := AskSocketDir(dir)
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	socketPath := AskSocketPath(dir)

	// Create a mock monitor server
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer func() { _ = listener.Close() }()

	connCh := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			connCh <- conn
		}
	}()

	server, err := NewAskServer(dir)
	if err != nil {
		t.Fatalf("NewAskServer failed: %v", err)
	}
	defer func() { _ = server.Close() }()

	if server.Mode() != AskModeClient {
		t.Errorf("expected client mode, got %s", server.Mode())
	}

	select {
	case conn := <-connCh:
		_ = conn.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("monitor server did not receive connection")
	}
}

func TestAskServer_ClientMode_Ask(t *testing.T) {
	dir := shortTempDir(t)
	socketDir := AskSocketDir(dir)
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	socketPath := AskSocketPath(dir)

	// Create a mock monitor that auto-approves
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer func() { _ = listener.Close() }()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		decoder := json.NewDecoder(conn)
		encoder := json.NewEncoder(conn)

		for {
			var req AskRequest
			if err := decoder.Decode(&req); err != nil {
				return
			}
			_ = encoder.Encode(AskResponse{
				ID:     req.ID,
				Action: FilterActionAllow,
			})
		}
	}()

	server, err := NewAskServer(dir)
	if err != nil {
		t.Fatalf("NewAskServer failed: %v", err)
	}
	defer func() { _ = server.Close() }()

	// Wait for client to connect
	deadline := time.Now().Add(2 * time.Second)
	for !server.HasMonitor() {
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for monitor connection")
		}
		time.Sleep(10 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := server.Ask(ctx, &AskRequest{
		ID:     "1",
		Method: "GET",
		URL:    "https://example.com",
		Host:   "example.com",
	})
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

	server, err := NewAskServer(dir)
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
	socketDir := AskSocketDir(dir)
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	socketPath := AskSocketPath(dir)

	// Monitor that captures the raw request
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer func() { _ = listener.Close() }()

	receivedCh := make(chan AskRequest, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		decoder := json.NewDecoder(conn)
		var req AskRequest
		if err := decoder.Decode(&req); err != nil {
			return
		}
		receivedCh <- req
		// Send response so Ask() doesn't block
		encoder := json.NewEncoder(conn)
		_ = encoder.Encode(AskResponse{ID: req.ID, Action: FilterActionAllow})
	}()

	server, err := NewAskServer(dir)
	if err != nil {
		t.Fatalf("NewAskServer failed: %v", err)
	}
	defer func() { _ = server.Close() }()

	deadline := time.Now().Add(2 * time.Second)
	for !server.HasMonitor() {
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for monitor")
		}
		time.Sleep(10 * time.Millisecond)
	}

	queue := NewAskQueue(server, nil, 45*time.Second)
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

	server, err := NewAskServer(dir)
	if err != nil {
		t.Fatalf("NewAskServer failed: %v", err)
	}

	socketPath := AskSocketPath(dir)

	// Connect a monitor that never responds
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("monitor dial failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	deadline := time.Now().Add(2 * time.Second)
	for !server.HasMonitor() {
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for monitor")
		}
		time.Sleep(10 * time.Millisecond)
	}

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
