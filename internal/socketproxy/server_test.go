package socketproxy

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// shortSocketDir returns a tmpdir short enough to fit macOS's 104-byte sun_path
// limit for sockets created beneath it.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestServer_StartCreatesSocket(t *testing.T) {
	dir := shortSocketDir(t)
	path := filepath.Join(dir, "test.sock")

	s := NewServer(path, 0o600, "test", func(ctx context.Context, conn net.Conn) {})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = s.Stop() }()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if info.Mode().Type() != os.ModeSocket {
		t.Errorf("not a socket: %v", info.Mode())
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o; want 0600", info.Mode().Perm())
	}
}

func TestServer_StartRemovesStaleFile(t *testing.T) {
	dir := shortSocketDir(t)
	path := filepath.Join(dir, "stale.sock")

	// Leave a stale file at the path; Start must remove it and succeed.
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewServer(path, 0o600, "test", func(ctx context.Context, conn net.Conn) {})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start failed despite stale file: %v", err)
	}
	_ = s.Stop()
}

func TestServer_StartFailsOnBadPath(t *testing.T) {
	s := NewServer("/nonexistent-dir/nope.sock", 0o600, "test",
		func(ctx context.Context, conn net.Conn) {})
	if err := s.Start(context.Background()); err == nil {
		_ = s.Stop()
		t.Fatal("expected error on unwritable path")
	}
}

func TestServer_HandlerReceivesConnection(t *testing.T) {
	dir := shortSocketDir(t)
	path := filepath.Join(dir, "h.sock")

	gotConn := make(chan struct{}, 1)
	s := NewServer(path, 0o600, "test", func(ctx context.Context, conn net.Conn) {
		gotConn <- struct{}{}
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = s.Stop() }()

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()

	select {
	case <-gotConn:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never ran")
	}
}

func TestServer_ClosesConnAfterHandler(t *testing.T) {
	dir := shortSocketDir(t)
	path := filepath.Join(dir, "c.sock")

	// Handler returns immediately; we check from the client side that the
	// connection gets closed (read returns EOF).
	s := NewServer(path, 0o600, "test", func(ctx context.Context, conn net.Conn) {})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = s.Stop() }()

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	n, err := conn.Read(buf)
	if err == nil && n > 0 {
		t.Fatal("expected EOF, got data")
	}
	// Any non-nil err is fine (EOF, closed, reset); timeout is not.
	if err != nil {
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Fatalf("timed out waiting for server to close conn")
		}
	}
}

func TestServer_StopWaitsForHandlers(t *testing.T) {
	dir := shortSocketDir(t)
	path := filepath.Join(dir, "stop.sock")

	block := make(chan struct{})
	released := make(chan struct{})
	s := NewServer(path, 0o600, "test", func(ctx context.Context, conn net.Conn) {
		<-block
		close(released)
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Give the accept loop a beat to dispatch the handler.
	time.Sleep(50 * time.Millisecond)

	stopDone := make(chan error, 1)
	go func() { stopDone <- s.Stop() }()

	// Stop must not return while handler is blocked.
	select {
	case err := <-stopDone:
		t.Fatalf("Stop returned before handler released: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(block)
	<-released

	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop never returned after handler released")
	}
}

func TestServer_StopTimesOutOnStuckHandler(t *testing.T) {
	dir := shortSocketDir(t)
	path := filepath.Join(dir, "stuck.sock")

	forever := make(chan struct{})
	defer close(forever)
	s := NewServer(path, 0o600, "test", func(ctx context.Context, conn net.Conn) {
		<-forever
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	time.Sleep(50 * time.Millisecond)

	// Override the drain timeout to keep the test fast.
	s.drainTimeout = 100 * time.Millisecond

	start := time.Now()
	err = s.Stop()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error from Stop")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("Stop took %v; custom drainTimeout should cap it", elapsed)
	}
}

func TestServer_StopClosesListener(t *testing.T) {
	dir := shortSocketDir(t)
	path := filepath.Join(dir, "l.sock")

	s := NewServer(path, 0o600, "test", func(ctx context.Context, conn net.Conn) {})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if _, err := net.Dial("unix", path); err == nil {
		t.Error("expected dial to fail after Stop")
	}
}

func TestServer_HandlesMultipleConnections(t *testing.T) {
	dir := shortSocketDir(t)
	path := filepath.Join(dir, "m.sock")

	var count atomic.Int32
	var wg sync.WaitGroup
	s := NewServer(path, 0o600, "test", func(ctx context.Context, conn net.Conn) {
		count.Add(1)
		wg.Done()
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = s.Stop() }()

	const n = 10
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			c, err := net.Dial("unix", path)
			if err != nil {
				return
			}
			_ = c.Close()
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("only %d/%d handlers ran", count.Load(), n)
	}
	if count.Load() != n {
		t.Errorf("count=%d; want %d", count.Load(), n)
	}
}

func TestServer_HandlerContextCancelledOnStop(t *testing.T) {
	dir := shortSocketDir(t)
	path := filepath.Join(dir, "cx.sock")

	ctxSeen := make(chan context.Context, 1)
	done := make(chan struct{})
	s := NewServer(path, 0o600, "test", func(ctx context.Context, conn net.Conn) {
		ctxSeen <- ctx
		<-ctx.Done()
		close(done)
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	select {
	case <-ctxSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never ran")
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler context was not cancelled on Stop")
	}
}
