package sockrelay

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// shortDir returns a directory short enough for a unix socket path on macOS.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// echoUnix serves an echo on a unix socket at path.
func echoUnix(t *testing.T, path string) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
}

func TestEndToEnd_GuestSocketReachesAllowedHostSocket(t *testing.T) {
	hostDir := shortDir(t)
	upstream := filepath.Join(hostDir, "herdr.sock")
	echoUnix(t, upstream)

	guestDir := shortDir(t)
	g, h := net.Pipe()
	host := NewHost(h, map[string]string{"herdr.sock": upstream})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = host.Run(ctx) }()
	go func() {
		defer wg.Done()
		_ = ServeGuest(ctx, g, []GuestSocket{{
			Name:  "herdr.sock",
			Paths: []string{filepath.Join(guestDir, "herdr.sock"), filepath.Join(guestDir, "alias", "herdr.sock")},
		}}, nil)
	}()
	defer func() {
		cancel()
		_ = host.Close()
		wg.Wait()
	}()

	select {
	case <-host.Ready():
	case <-time.After(3 * time.Second):
		t.Fatal("guest never reported ready")
	}

	for _, p := range []string{filepath.Join(guestDir, "herdr.sock"), filepath.Join(guestDir, "alias", "herdr.sock")} {
		c, err := net.Dial("unix", p)
		if err != nil {
			t.Fatalf("dial %s: %v", p, err)
		}
		if _, err := c.Write([]byte("hello")); err != nil {
			t.Fatal(err)
		}
		if got := readWithTimeout(t, c, 5); string(got) != "hello" {
			t.Fatalf("echo via %s = %q", p, got)
		}
		_ = c.Close()
	}
}

func TestServeGuest_RemovesSocketsWhenHostGoesAway(t *testing.T) {
	guestDir := shortDir(t)
	sock := filepath.Join(guestDir, "herdr.sock")
	g, h := net.Pipe()
	// net.Pipe is synchronous, so the host end has to drain the READY frame
	// the way a real stdio pipe buffers it.
	go func() { _, _ = io.Copy(io.Discard, h) }()
	done := make(chan error, 1)
	go func() {
		done <- ServeGuest(context.Background(), g, []GuestSocket{{Name: "herdr.sock", Paths: []string{sock}}}, nil)
	}()
	// Wait for the listener, then drop the host end as a dead host would.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Lstat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("guest socket never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = h.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ServeGuest on host EOF = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ServeGuest did not return after the host closed the pipe")
	}
	if _, err := os.Lstat(sock); !os.IsNotExist(err) {
		t.Fatalf("stale socket left behind: Lstat err = %v", err)
	}
	// The run directory is per host pid and lives in the guest's persistent
	// home, where nothing on the host can sweep it; an emptied one goes too.
	if _, err := os.Lstat(guestDir); !os.IsNotExist(err) {
		t.Fatalf("empty run directory left behind: Lstat err = %v", err)
	}
}

func TestServeGuest_SocketIsPrivateToTheUser(t *testing.T) {
	guestDir := shortDir(t)
	sock := filepath.Join(guestDir, "run", "herdr.sock")
	g, h := net.Pipe()
	defer func() { _ = h.Close() }()
	go func() { _, _ = io.Copy(io.Discard, h) }()
	go func() {
		_ = ServeGuest(context.Background(), g, []GuestSocket{{Name: "herdr.sock", Paths: []string{sock}}}, nil)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if fi, err := os.Lstat(sock); err == nil {
			if perm := fi.Mode().Perm(); perm != 0o600 {
				t.Fatalf("socket mode = %o, want 0600", perm)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("guest socket never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServeGuest_ReplacesStaleSocketButNotARegularFile(t *testing.T) {
	guestDir := shortDir(t)
	stale := filepath.Join(guestDir, "stale.sock")
	ln, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	if _, err := os.Lstat(stale); err != nil {
		t.Fatalf("precondition: stale socket entry missing: %v", err)
	}

	regular := filepath.Join(guestDir, "notes.txt")
	if err := os.WriteFile(regular, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	g, h := net.Pipe()
	defer func() { _ = h.Close() }()
	go func() { _, _ = io.Copy(io.Discard, h) }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		// The primary is a regular file: fatal, nothing else is served.
		done <- ServeGuest(ctx, g, []GuestSocket{
			{Name: "stale.sock", Paths: []string{stale}},
			{Name: "notes", Paths: []string{regular}},
		}, nil)
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ServeGuest replaced a regular file; want an error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ServeGuest did not fail on a regular file in the way")
	}
	if data, err := os.ReadFile(regular); err != nil || string(data) != "keep" {
		t.Fatalf("regular file clobbered: %q, %v", data, err)
	}
}

// An alias is a convenience for clients that derive a path on their own; one
// that cannot be created must not take the primary down with it, and kitty's
// socket in the same relay even less so.
func TestServeGuest_AliasFailureIsLoggedNotFatal(t *testing.T) {
	guestDir := shortDir(t)
	primary := filepath.Join(guestDir, "run", "herdr.sock")
	alias := filepath.Join(guestDir, "alias.txt")
	if err := os.WriteFile(alias, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	hostDir := shortDir(t)
	upstream := filepath.Join(hostDir, "herdr.sock")
	echoUnix(t, upstream)
	g, h := net.Pipe()
	host := NewHost(h, map[string]string{"herdr.sock": upstream})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logged []string
	logf := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	go func() { _ = host.Run(ctx) }()
	go func() {
		_ = ServeGuest(ctx, g, []GuestSocket{{Name: "herdr.sock", Paths: []string{primary, alias}}}, logf)
	}()
	defer func() { _ = host.Close() }()

	select {
	case <-host.Ready():
	case <-time.After(3 * time.Second):
		t.Fatal("relay never became ready with a failed alias")
	}
	c, err := net.Dial("unix", primary)
	if err != nil {
		t.Fatalf("primary not served: %v", err)
	}
	_ = c.Close()
	if data, _ := os.ReadFile(alias); string(data) != "keep" {
		t.Fatalf("alias file clobbered: %q", data)
	}
	if len(logged) == 0 || !strings.Contains(logged[0], alias) {
		t.Fatalf("alias failure not logged: %q", logged)
	}
}

// Two sessions of one project share a kept container and so the alias path.
// The second must not unlink the first's live socket; it skips the alias,
// and the first session keeps serving it.
func TestServeGuest_LeavesLiveAliasAlone(t *testing.T) {
	guestDir := shortDir(t)
	alias := filepath.Join(guestDir, "herdr.sock")
	echoUnix(t, alias) // the first session's live listener

	primary := filepath.Join(guestDir, "run2", "herdr.sock")
	g, h := net.Pipe()
	defer func() { _ = h.Close() }()
	go func() { _, _ = io.Copy(io.Discard, h) }()
	go func() {
		_ = ServeGuest(context.Background(), g, []GuestSocket{{Name: "herdr.sock", Paths: []string{primary, alias}}}, nil)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Lstat(primary); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second session's primary never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	c, err := net.Dial("unix", alias)
	if err != nil {
		t.Fatalf("first session's alias no longer accepts: %v", err)
	}
	if _, err := c.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	if got := readWithTimeout(t, c, 2); string(got) != "hi" {
		t.Fatalf("alias is not the first session's echo any more: %q", got)
	}
	_ = c.Close()
}

func TestHost_DeniesNameOutsideAllowlist(t *testing.T) {
	hostDir := shortDir(t)
	upstream := filepath.Join(hostDir, "herdr.sock")
	echoUnix(t, upstream)
	g, h := net.Pipe()
	host := NewHost(h, map[string]string{"herdr.sock": upstream})
	guest := New(g, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = host.Run(ctx) }()
	go func() { _ = guest.Run(ctx) }()
	defer func() { _ = host.Close(); _ = guest.Close() }()

	local, remote := net.Pipe()
	if err := guest.Open("kitty.sock", remote); err != nil {
		t.Fatal(err)
	}
	_ = local.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := local.Read(make([]byte, 1)); err == nil {
		t.Fatal("stream for a name outside the allowlist stayed open")
	}
}
