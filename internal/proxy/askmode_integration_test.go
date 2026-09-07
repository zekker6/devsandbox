package proxy

import (
	"context"
	"io"
	"log"
	"testing"
	"time"
)

// TestIntegration_SandboxFirst verifies the flow where the sandbox (AskServer)
// starts first in server mode, and then a monitor connects after.
func TestIntegration_SandboxFirst(t *testing.T) {
	dir := shortTempDir(t)

	// Step 1: AskServer starts first — no socket, so server mode
	server, err := NewAskServer(dir, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewAskServer failed: %v", err)
	}
	defer func() { _ = server.Close() }()

	if server.Mode() != AskModeServer {
		t.Fatalf("expected server mode, got %s", server.Mode())
	}

	socketPath := AskSocketPath(dir)

	// Step 2: Monitor connects after (simulates runProxyMonitor client mode)
	conn, monitorDec, monitorEnc := dialAsMonitor(t, socketPath)
	go serveAsMonitor(monitorDec, monitorEnc, allowAll)
	waitForAskMonitor(t, server)

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
	waitForMonitorState(t, server, false)

	_, monitorDec2, monitorEnc2 := dialAsMonitor(t, socketPath)
	go serveAsMonitor(monitorDec2, monitorEnc2, blockAll) // Different action to distinguish
	waitForAskMonitor(t, server)

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
	shortAskTimers(t)
	dir := shortTempDir(t)
	socketPath := AskSocketPath(dir)

	// Step 1: Start first monitor
	monitor1 := listenUnix(t, socketPath, monitorHandler(allowAll))

	// Step 2: AskServer connects as client
	server, err := NewAskServer(dir, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewAskServer failed: %v", err)
	}
	defer func() { _ = server.Close() }()

	if server.Mode() != AskModeClient {
		t.Fatalf("expected client mode, got %s", server.Mode())
	}
	waitForAskMonitor(t, server)

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
	monitor1.Close()
	waitForMonitorState(t, server, false)

	// Step 5: Start a new monitor on the same socket path
	listenUnix(t, socketPath, monitorHandler(blockAll)) // Different action to distinguish

	// Step 6: Wait for reconnection
	waitForAskMonitor(t, server)

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

	// Step 1: Start monitor (creates socket, listens, auto-approves)
	listenUnix(t, AskSocketPath(dir), monitorHandler(allowAll))

	// Step 2: AskServer starts, detects socket, connects as client
	server, err := NewAskServer(dir, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewAskServer failed: %v", err)
	}
	defer func() { _ = server.Close() }()

	if server.Mode() != AskModeClient {
		t.Fatalf("expected client mode, got %s", server.Mode())
	}
	waitForAskMonitor(t, server)

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

	server2, err := NewAskServer(dir, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("second NewAskServer failed: %v", err)
	}
	defer func() { _ = server2.Close() }()

	if server2.Mode() != AskModeClient {
		t.Fatalf("expected client mode after restart, got %s", server2.Mode())
	}
	waitForAskMonitor(t, server2)

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
