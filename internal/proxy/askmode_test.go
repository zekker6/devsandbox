package proxy

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"
)

func TestAskServer_ServerMode(t *testing.T) {
	dir := t.TempDir()

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
	dir := t.TempDir()
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
	dir := t.TempDir()
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
	dir := t.TempDir()
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
