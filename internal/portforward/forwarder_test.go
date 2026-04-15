package portforward

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

// localDialer dials in the same network namespace, used to test the forwarder
// without requiring Linux namespace privileges.
type localDialer struct{}

func (d *localDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, address)
}

// startEchoServer starts a TCP echo server on a random port and returns the
// listener (caller must close) and the port it is listening on.
func startEchoServer(t *testing.T) (net.Listener, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo server listen: %v", err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close() //nolint:errcheck
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	return ln, ln.Addr().(*net.TCPAddr).Port
}

func TestForwarder_ProxiesTraffic(t *testing.T) {
	echoLn, echoPort := startEchoServer(t)
	defer echoLn.Close() //nolint:errcheck

	f := &Forwarder{
		HostPort:    0,
		SandboxPort: echoPort,
		Dialer:      &localDialer{},
	}

	ctx := context.Background()
	if err := f.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer f.Stop()

	hostPort := f.ActualHostPort()
	if hostPort == 0 {
		t.Fatal("ActualHostPort returned 0 after Start")
	}

	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(hostPort)), 2*time.Second)
	if err != nil {
		t.Fatalf("dial forwarder: %v", err)
	}
	defer conn.Close() //nolint:errcheck

	msg := []byte("hello sandbox")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Signal EOF so the echo server knows we're done sending.
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}

	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(got) != string(msg) {
		t.Errorf("echo mismatch: got %q, want %q", got, msg)
	}
}

func TestForwarder_StartWithFallback_NoConflict(t *testing.T) {
	echoLn, echoPort := startEchoServer(t)
	defer echoLn.Close() //nolint:errcheck

	f := &Forwarder{
		HostPort:    0, // let the OS pick; will succeed on first try
		SandboxPort: echoPort,
		Dialer:      &localDialer{},
	}

	ctx := context.Background()
	port, fellBack, err := f.StartWithFallback(ctx)
	if err != nil {
		t.Fatalf("StartWithFallback: %v", err)
	}
	defer f.Stop()

	if fellBack {
		t.Error("fellBack = true, want false when no conflict")
	}
	if port == 0 {
		t.Error("returned port is 0")
	}
	if port != f.ActualHostPort() {
		t.Errorf("returned port %d != ActualHostPort %d", port, f.ActualHostPort())
	}
}

func TestForwarder_StartWithFallback_HostPortBusy(t *testing.T) {
	// Occupy an ephemeral host port so the forwarder's first bind attempt fails.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("grab busy port: %v", err)
	}
	defer busy.Close() //nolint:errcheck
	busyPort := busy.Addr().(*net.TCPAddr).Port

	echoLn, echoPort := startEchoServer(t)
	defer echoLn.Close() //nolint:errcheck

	f := &Forwarder{
		HostPort:    busyPort, // preferred host port is already in use
		SandboxPort: echoPort,
		Dialer:      &localDialer{},
	}

	ctx := context.Background()
	port, fellBack, err := f.StartWithFallback(ctx)
	if err != nil {
		t.Fatalf("StartWithFallback: %v", err)
	}
	defer f.Stop()

	if !fellBack {
		t.Error("fellBack = false, want true when preferred port is busy")
	}
	if port == busyPort {
		t.Errorf("fallback reused busy port %d", busyPort)
	}
	if port == 0 {
		t.Error("returned port is 0")
	}

	// The forwarder must actually work on the fallback port.
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 2*time.Second)
	if err != nil {
		t.Fatalf("dial fallback port: %v", err)
	}
	defer conn.Close() //nolint:errcheck

	msg := []byte("hi")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(got) != string(msg) {
		t.Errorf("echo mismatch: got %q, want %q", got, msg)
	}
}

func TestForwarder_StopClosesListener(t *testing.T) {
	// We don't need a real sandbox port for this test — Stop should close the
	// listener before any connection is attempted.
	f := &Forwarder{
		HostPort:    0,
		SandboxPort: 12345, // irrelevant; no connection will be made
		Dialer:      &localDialer{},
	}

	ctx := context.Background()
	if err := f.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	port := f.ActualHostPort()
	f.Stop()

	// After Stop the listener must be closed; a new connection should fail.
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 500*time.Millisecond)
	if err == nil {
		conn.Close() //nolint:errcheck
		t.Fatal("expected connection to fail after Stop, but it succeeded")
	}
}
