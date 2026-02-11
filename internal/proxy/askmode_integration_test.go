package proxy

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"
)

func TestIntegration_MonitorFirst(t *testing.T) {
	dir := shortTempDir(t)
	socketDir := AskSocketDir(dir)
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	socketPath := AskSocketPath(dir)

	// Step 1: Start monitor (creates socket, listens)
	monitorListener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("monitor listen failed: %v", err)
	}
	defer func() { _ = monitorListener.Close() }()

	// Monitor auto-approves in background
	go func() {
		for {
			conn, err := monitorListener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				dec := json.NewDecoder(c)
				enc := json.NewEncoder(c)
				for {
					var req AskRequest
					if err := dec.Decode(&req); err != nil {
						return
					}
					_ = enc.Encode(AskResponse{
						ID:     req.ID,
						Action: FilterActionAllow,
					})
				}
			}(conn)
		}
	}()

	// Step 2: AskServer starts, detects socket, connects as client
	server, err := NewAskServer(dir)
	if err != nil {
		t.Fatalf("NewAskServer failed: %v", err)
	}
	defer func() { _ = server.Close() }()

	if server.Mode() != AskModeClient {
		t.Fatalf("expected client mode, got %s", server.Mode())
	}

	// Wait for client to connect
	deadline := time.Now().Add(5 * time.Second)
	for !server.HasMonitor() {
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for monitor connection")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Step 3: Send a request — should be approved by monitor
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := server.Ask(ctx, &AskRequest{
		ID:     "integration-1",
		Method: "GET",
		URL:    "https://example.com/test",
		Host:   "example.com",
		Path:   "/test",
	})
	if err != nil {
		t.Fatalf("Ask failed: %v", err)
	}
	if resp.Action != FilterActionAllow {
		t.Errorf("expected allow, got %s", resp.Action)
	}

	// Step 4: Simulate sandbox restart — close AskServer, create new one
	_ = server.Close()

	server2, err := NewAskServer(dir)
	if err != nil {
		t.Fatalf("second NewAskServer failed: %v", err)
	}
	defer func() { _ = server2.Close() }()

	if server2.Mode() != AskModeClient {
		t.Fatalf("expected client mode after restart, got %s", server2.Mode())
	}

	// Wait for client to reconnect
	deadline2 := time.Now().Add(5 * time.Second)
	for !server2.HasMonitor() {
		if time.Now().After(deadline2) {
			t.Fatal("timeout waiting for monitor reconnection")
		}
		time.Sleep(10 * time.Millisecond)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	resp2, err := server2.Ask(ctx2, &AskRequest{
		ID:     "integration-2",
		Method: "POST",
		URL:    "https://example.com/api",
		Host:   "example.com",
		Path:   "/api",
	})
	if err != nil {
		t.Fatalf("second Ask failed: %v", err)
	}
	if resp2.Action != FilterActionAllow {
		t.Errorf("expected allow, got %s", resp2.Action)
	}
}
