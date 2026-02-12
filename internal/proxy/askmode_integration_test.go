package proxy

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

// TestIntegration_SandboxFirst verifies the flow where the sandbox (AskServer)
// starts first in server mode, and then a monitor connects after.
func TestIntegration_SandboxFirst(t *testing.T) {
	dir := shortTempDir(t)

	// Step 1: AskServer starts first — no socket, so server mode
	server, err := NewAskServer(dir)
	if err != nil {
		t.Fatalf("NewAskServer failed: %v", err)
	}
	defer func() { _ = server.Close() }()

	if server.Mode() != AskModeServer {
		t.Fatalf("expected server mode, got %s", server.Mode())
	}

	socketPath := AskSocketPath(dir)

	// Step 2: Monitor connects after (simulates runProxyMonitor client mode)
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("monitor dial failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	monitorEnc := json.NewEncoder(conn)
	monitorDec := json.NewDecoder(conn)

	// Auto-approve in background (reads requests, writes responses)
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		for {
			var req AskRequest
			if err := monitorDec.Decode(&req); err != nil {
				return
			}
			_ = monitorEnc.Encode(AskResponse{
				ID:     req.ID,
				Action: FilterActionAllow,
			})
		}
	}()

	// Wait for AskServer to register the monitor
	deadline := time.Now().Add(5 * time.Second)
	for !server.HasMonitor() {
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for monitor to be registered")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Step 3: Send a request — should be approved by monitor
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := server.Ask(ctx, &AskRequest{
		ID:     "sandbox-first-1",
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

	// Step 4: Send another request
	resp2, err := server.Ask(ctx, &AskRequest{
		ID:     "sandbox-first-2",
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

	// Step 5: Monitor disconnects, new monitor connects (simulates monitor restart)
	_ = conn.Close()
	<-monitorDone

	// Brief wait for AskServer to notice disconnect
	time.Sleep(50 * time.Millisecond)

	conn2, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("second monitor dial failed: %v", err)
	}
	defer func() { _ = conn2.Close() }()

	monitorEnc2 := json.NewEncoder(conn2)
	monitorDec2 := json.NewDecoder(conn2)

	go func() {
		for {
			var req AskRequest
			if err := monitorDec2.Decode(&req); err != nil {
				return
			}
			_ = monitorEnc2.Encode(AskResponse{
				ID:     req.ID,
				Action: FilterActionBlock, // Different action to distinguish
			})
		}
	}()

	// Wait for new monitor to be registered
	deadline2 := time.Now().Add(5 * time.Second)
	for !server.HasMonitor() {
		if time.Now().After(deadline2) {
			t.Fatal("timeout waiting for second monitor to be registered")
		}
		time.Sleep(10 * time.Millisecond)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	resp3, err := server.Ask(ctx2, &AskRequest{
		ID:     "sandbox-first-3",
		Method: "DELETE",
		URL:    "https://example.com/resource",
		Host:   "example.com",
		Path:   "/resource",
	})
	if err != nil {
		t.Fatalf("third Ask failed: %v", err)
	}
	if resp3.Action != FilterActionBlock {
		t.Errorf("expected block from second monitor, got %s", resp3.Action)
	}
}

// TestIntegration_ClientReconnect verifies that a client-mode AskServer
// recovers when the monitor disconnects and restarts.
func TestIntegration_ClientReconnect(t *testing.T) {
	dir := shortTempDir(t)
	socketDir := AskSocketDir(dir)
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	socketPath := AskSocketPath(dir)

	// Step 1: Start first monitor — track accepted connections so we can close them
	listener1, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("monitor listen failed: %v", err)
	}

	var monitor1Conns []net.Conn
	var monitor1Mu sync.Mutex

	go func() {
		for {
			conn, err := listener1.Accept()
			if err != nil {
				return
			}
			monitor1Mu.Lock()
			monitor1Conns = append(monitor1Conns, conn)
			monitor1Mu.Unlock()
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

	// Step 2: AskServer connects as client
	server, err := NewAskServer(dir)
	if err != nil {
		t.Fatalf("NewAskServer failed: %v", err)
	}
	defer func() { _ = server.Close() }()

	if server.Mode() != AskModeClient {
		t.Fatalf("expected client mode, got %s", server.Mode())
	}

	deadline := time.Now().Add(5 * time.Second)
	for !server.HasMonitor() {
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for initial monitor connection")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Step 3: Verify initial request works
	ctx1, cancel1 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel1()

	resp1, err := server.Ask(ctx1, &AskRequest{
		ID:     "reconnect-1",
		Method: "GET",
		URL:    "https://example.com",
		Host:   "example.com",
	})
	if err != nil {
		t.Fatalf("initial Ask failed: %v", err)
	}
	if resp1.Action != FilterActionAllow {
		t.Errorf("expected allow, got %s", resp1.Action)
	}

	// Step 4: Kill the monitor — close listener and all accepted connections
	_ = listener1.Close()
	monitor1Mu.Lock()
	for _, c := range monitor1Conns {
		_ = c.Close()
	}
	monitor1Mu.Unlock()

	// Wait for AskServer to notice disconnect
	deadline2 := time.Now().Add(5 * time.Second)
	for server.HasMonitor() {
		if time.Now().After(deadline2) {
			t.Fatal("timeout waiting for disconnect detection")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Step 5: Start a new monitor on the same socket path
	_ = os.Remove(socketPath)
	listener2, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("second monitor listen failed: %v", err)
	}
	defer func() { _ = listener2.Close() }()

	go func() {
		for {
			conn, err := listener2.Accept()
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
						Action: FilterActionBlock, // Different action to distinguish
					})
				}
			}(conn)
		}
	}()

	// Step 6: Wait for reconnection
	deadline3 := time.Now().Add(10 * time.Second)
	for !server.HasMonitor() {
		if time.Now().After(deadline3) {
			t.Fatal("timeout waiting for reconnection")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Step 7: Verify request works through reconnected monitor
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()

	resp2, err := server.Ask(ctx2, &AskRequest{
		ID:     "reconnect-2",
		Method: "POST",
		URL:    "https://example.com/api",
		Host:   "example.com",
	})
	if err != nil {
		t.Fatalf("reconnected Ask failed: %v", err)
	}
	if resp2.Action != FilterActionBlock {
		t.Errorf("expected block from second monitor, got %s", resp2.Action)
	}
}

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
