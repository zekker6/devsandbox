package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"devsandbox/internal/config"
	"devsandbox/internal/notice"
	"devsandbox/internal/portforward"
	"devsandbox/internal/session"
)

type autoForwardScanner struct {
	mu      sync.Mutex
	entries []portforward.ListenEntry
}

func (s *autoForwardScanner) ListeningPorts() ([]portforward.ListenEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]portforward.ListenEntry(nil), s.entries...), nil
}

func (s *autoForwardScanner) set(entries ...portforward.ListenEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = entries
}

func waitAutoPorts(t *testing.T, store *session.Store, count int) []session.ForwardedPort {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sess, err := store.Get("auto-test")
		if err != nil {
			t.Fatal(err)
		}
		if len(sess.ForwardedPorts) == count {
			return sess.ForwardedPorts
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("forwarded port count did not reach %d", count)
	return nil
}

func TestAutoForward_RestartWithIdleClient(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	// macOS assigns only 127.0.0.1 to lo0, unlike Linux which routes all of 127/8.
	if errors.Is(err, syscall.EADDRNOTAVAIL) {
		t.Skipf("127.0.0.2 not assigned to loopback: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = io.Copy(conn, conn)
	}()

	store := newForwardTestStore(t)
	sess := &session.Session{Name: "auto-test", PID: os.Getpid(), StartedAt: time.Now()}
	if err := store.Register(sess); err != nil {
		t.Fatal(err)
	}
	scanner := &autoForwardScanner{}
	scanner.set(portforward.ListenEntry{IP: "127.0.0.2", Port: port})
	stop := startAutoForward(context.Background(), config.PortForwardingConfig{ScanInterval: "10ms"}, sess, store, scanner, &net.Dialer{})
	defer stop()
	ports := waitAutoPorts(t, store, 1)
	client, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(ports[0].HostPort)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if err := client.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	var reply [4]byte
	if _, err := io.ReadFull(client, reply[:]); err != nil {
		t.Fatal(err)
	}
	if string(reply[:]) != "ping" {
		t.Fatalf("reply=%q", reply)
	}

	// The application stops accepting while the browser keeps its connection.
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	scanner.set()
	waitAutoPorts(t, store, 0)
	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("removed forward kept its sandbox connection")
	}
	// Reappearance must still be processed after removal of an active forward.
	scanner.set(portforward.ListenEntry{IP: "127.0.0.2", Port: port})
	waitAutoPorts(t, store, 1)
	stop()
	if err := store.Remove(sess.Name); err != nil {
		t.Fatal(err)
	}
	// stop joins the monitor, so it cannot recreate the removed session record.
	if _, err := store.Get(sess.Name); err == nil {
		t.Fatal("session remains after cleanup")
	}
}

func TestForwardError_ReachesRunningTerminalSafely(t *testing.T) {
	var terminal bytes.Buffer
	if err := notice.Setup(filepath.Join(t.TempDir(), "wrapper.log"), false, &terminal); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		notice.SetStartup()
		if err := notice.Setup("", false, io.Discard); err != nil {
			t.Error(err)
		}
	})
	notice.SetRunning()
	reportForwardError(errors.New("helper failed: \x1b[2J"))
	if !strings.Contains(terminal.String(), "helper failed") {
		t.Fatal("runtime failure was suppressed")
	}
	if strings.Contains(terminal.String(), "\x1b") {
		t.Fatal("raw terminal escape in diagnostic")
	}
}
