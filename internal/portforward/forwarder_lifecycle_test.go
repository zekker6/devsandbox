package portforward

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestForwarder_UsesListenerAddress(t *testing.T) {
	for _, ip := range []string{"127.0.0.2", "::1"} {
		t.Run(ip, func(t *testing.T) {
			if ip == "::1" {
				probe, err := net.Listen("tcp6", "[::1]:0")
				if err != nil {
					t.Skipf("IPv6 loopback unavailable: %v", err)
				}
				_ = probe.Close()
			} else {
				probe, err := net.Listen("tcp4", net.JoinHostPort(ip, "0"))
				// macOS assigns only 127.0.0.1 to lo0, unlike Linux which routes all of 127/8.
				if errors.Is(err, syscall.EADDRNOTAVAIL) {
					t.Skipf("%s not assigned to loopback: %v", ip, err)
				}
				if err != nil {
					t.Fatal(err)
				}
				_ = probe.Close()
			}
			ln, port := startEchoServerAt(t, net.JoinHostPort(ip, "0"))
			defer func() { _ = ln.Close() }()
			f := &Forwarder{SandboxIP: ip, SandboxPort: port, Dialer: &localDialer{}}
			if err := f.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer f.Stop()
			conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(f.ActualHostPort())), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Write([]byte("ping")); err != nil {
				t.Fatal(err)
			}
			var reply [4]byte
			if _, err := io.ReadFull(conn, reply[:]); err != nil {
				t.Fatal(err)
			}
			if string(reply[:]) != "ping" {
				t.Fatalf("reply=%q", reply)
			}
		})
	}
}

type signalingDialer struct {
	connected chan struct{}
}

func (d signalingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err == nil {
		close(d.connected)
	}
	return conn, err
}

func TestForwarder_StopInterruptsIdleConnection(t *testing.T) {
	for _, parentCancel := range []bool{false, true} {
		name := "stop"
		if parentCancel {
			name = "parent cancellation"
		}
		t.Run(name, func(t *testing.T) {
			ln, port := startEchoServer(t)
			defer func() { _ = ln.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			connected := make(chan struct{})
			f := &Forwarder{SandboxPort: port, Dialer: signalingDialer{connected}}
			if err := f.Start(ctx); err != nil {
				t.Fatal(err)
			}
			client, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(f.ActualHostPort())), time.Second)
			if err != nil {
				f.Stop()
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()
			select {
			case <-connected:
			case <-time.After(2 * time.Second):
				_ = client.Close()
				f.Stop()
				t.Fatal("sandbox connection not established")
			}
			if parentCancel {
				cancel()
				if err := client.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
					t.Fatal(err)
				}
				var b [1]byte
				_, err := client.Read(b[:])
				if err == nil {
					t.Error("expected EOF after cancellation")
				}
				if e, ok := err.(net.Error); ok && e.Timeout() {
					t.Error("idle connection survived cancellation")
				}
			}
			done := make(chan struct{})
			go func() { f.Stop(); close(done) }()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				_ = client.Close()
				<-done
				t.Fatal("Stop blocked on idle client")
			}
			if f.ActiveConnections() != 0 {
				t.Fatal("connection still active")
			}
		})
	}
}

type failingDialer struct{}

func (failingDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("namespace unavailable")
}

func TestForwarder_ReportsDialFailure(t *testing.T) {
	reported := make(chan error, 1)
	f := &Forwarder{SandboxPort: 3000, Dialer: failingDialer{}, OnError: func(err error) { reported <- err }}
	if err := f.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer f.Stop()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(f.ActualHostPort())), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	select {
	case err := <-reported:
		if !strings.Contains(err.Error(), "namespace unavailable") {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dial failure was silent")
	}
}
