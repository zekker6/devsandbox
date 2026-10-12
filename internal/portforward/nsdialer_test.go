package portforward

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNamespaceDialer_UnsupportedNetwork(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only test")
	}
	d := NewNamespaceDialer(1)
	_, err := d.DialContext(context.Background(), "udp", "127.0.0.1:1")
	if err == nil {
		t.Fatal("expected error for non-tcp network")
	}
	if !strings.Contains(err.Error(), "only supports tcp") {
		t.Errorf("expected a 'tcp only' error, got: %v", err)
	}
}

func fakeNamespaceDialer(t *testing.T, script string) *NamespaceDialer {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "nsenter")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	d := NewNamespaceDialer(1)
	d.NsenterPath = path
	return d
}

func TestNamespaceDialer_ReadinessFailure(t *testing.T) {
	for _, tt := range []struct {
		name   string
		script string
		want   string
	}{
		{"startup error", "printf 'nsenter: permission denied\\n' >&2; exit 1", "nsenter: permission denied"},
		{"missing byte", "exit 0", "EOF"},
		{"invalid byte", "printf x; read input", "invalid readiness byte 0x78"},
		{"timeout", "read input", "timed out waiting for readiness"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := fakeNamespaceDialer(t, tt.script)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, err := d.dialContext(ctx, "tcp", "127.0.0.1:1", 200*time.Millisecond)
			if conn != nil {
				_ = conn.Close()
				t.Fatal("failed readiness returned a connection")
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			if tt.name == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timeout lost deadline error: %v", err)
			}
		})
	}
}

func TestNamespaceDialer_ReadinessAndHalfClose(t *testing.T) {
	d := fakeNamespaceDialer(t, "printf '\\000payload'; cat; printf finished")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const readyTimeout = time.Second
	conn, err := d.dialContext(ctx, "tcp", "127.0.0.1:1", readyTimeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	initial := make([]byte, len("payload"))
	if _, err := io.ReadFull(conn, initial); err != nil {
		t.Fatal(err)
	}
	if string(initial) != "payload" {
		t.Fatalf("payload = %q, readiness must be consumed exactly once", initial)
	}
	// The readiness timeout must not become a connection lifetime limit.
	time.Sleep(readyTimeout + 50*time.Millisecond)
	if _, err := conn.Write([]byte("echo")); err != nil {
		t.Fatal(err)
	}
	if err := conn.(*helperConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "echofinished" {
		t.Fatalf("after half-close got %q, want echofinished", data)
	}

	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if err := conn.Close(); err != nil {
				t.Errorf("concurrent Close: %v", err)
			}
		})
	}
	wg.Wait()
	if conn.(*helperConn).cmd.ProcessState == nil {
		t.Fatal("Close did not reap helper")
	}
}

func TestNamespaceDialer_CancelDuringReadiness(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	d := fakeNamespaceDialer(t, "printf started > '"+started+"'; read input")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		conn, err := d.DialContext(ctx, "tcp", "127.0.0.1:1")
		if conn != nil {
			_ = conn.Close()
		}
		result <- err
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not start")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not stop readiness wait")
	}
}

// TestCappedBuffer_ConcurrentWriteAndString reproduces the race the full
// -race suite caught: os/exec copies the helper's stderr into the buffer on
// its own goroutine while stderrError reads it from the caller's Read path.
// Without the mutex this fails under -race regardless of scheduling.
func TestCappedBuffer_ConcurrentWriteAndString(t *testing.T) {
	b := &cappedBuffer{max: 64}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		// More than max in total, so the truncating branch runs too.
		for range 200 {
			if _, err := b.Write([]byte("dial: connection refused\n")); err != nil {
				t.Errorf("Write: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 200 {
			_ = b.String()
		}
	}()

	wg.Wait()

	if got := len(b.String()); got != 64 {
		t.Errorf("buffer retained %d bytes, want the 64-byte cap", got)
	}
}

// Ensure helperConn satisfies net.Conn.
var _ net.Conn = (*helperConn)(nil)
