package sockrelay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Host is the devsandbox end of the relay: it answers the guest's OPEN by
// dialing the host unix socket registered under that name, and nothing else.
// A name outside allowed is refused, so the guest's reach is exactly the set of
// proxy sockets the host chose to expose.
type Host struct {
	mux *Mux
}

// NewHost wraps rw (the stdio of the guest relay process) with an allowlist of
// name → host unix socket path.
func NewHost(rw io.ReadWriteCloser, allowed map[string]string) *Host {
	// Copy so the caller cannot widen the allowlist after construction.
	paths := make(map[string]string, len(allowed))
	for k, v := range allowed {
		paths[k] = v
	}
	open := func(name string) (net.Conn, error) {
		path, ok := paths[name]
		if !ok {
			return nil, fmt.Errorf("sockrelay: %q is not an exposed socket", name)
		}
		return net.Dial("unix", path)
	}
	return &Host{mux: New(rw, open)}
}

// Run relays until the guest end closes or ctx ends.
func (h *Host) Run(ctx context.Context) error { return h.mux.Run(ctx) }

// Ready is closed once the guest reports its listeners exist.
func (h *Host) Ready() <-chan struct{} { return h.mux.Ready() }

// Close tears the relay down.
func (h *Host) Close() error { return h.mux.Close() }

// GuestSocket is one host socket as the guest exposes it: the name the host
// allowlists it under, and every path the guest listens on for it. The first
// path is the primary; the rest are aliases a client may derive on its own.
type GuestSocket struct {
	Name  string
	Paths []string
}

// LogFunc receives the guest end's non-fatal diagnostics. nil discards them.
type LogFunc func(format string, args ...any)

// ServeGuest is the sandbox end: it listens on every path, relays each accepted
// connection as a stream named after its socket, reports READY, and runs until
// rw closes - which is how a dead or finished host ends the relay. Listeners
// are removed on return so a stale socket never outlives its session.
//
// A primary path that cannot be served is fatal. An alias that cannot be is
// logged and skipped: it is a convenience for clients that derive a path on
// their own, and losing it must not take the primary down, nor another
// socket's. A path that already holds a dead socket is replaced, since that is
// what a previous session left behind; one that holds a live socket is left
// alone, because a second session of the same project in one container would
// otherwise unlink the first's. Anything else in the way is refused: the relay
// runs as the sandbox user in the sandbox home, and it must not clobber a file
// there on the strength of a name.
func ServeGuest(ctx context.Context, rw io.ReadWriteCloser, sockets []GuestSocket, logf LogFunc) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	mux := New(rw, nil)
	var listeners []net.Listener
	closeAll := func() {
		for _, ln := range listeners {
			_ = ln.Close() // unlinks the socket file
		}
		// The primary path's directory is the per-session run directory in
		// the guest's persistent home, which no host-side sweep reaches; an
		// emptied one is removed, a non-empty one is left alone.
		for _, s := range sockets {
			if len(s.Paths) > 0 {
				_ = os.Remove(filepath.Dir(s.Paths[0]))
			}
		}
	}
	for _, s := range sockets {
		for i, p := range s.Paths {
			ln, err := listenReplacingStale(p)
			switch {
			case err == nil:
			case i == 0:
				closeAll()
				_ = rw.Close()
				return err
			case errors.Is(err, errLiveSocket):
				logf("sockrelay: alias %s is served by another session; leaving it", p)
				continue
			default:
				logf("sockrelay: alias %s skipped: %v", p, err)
				continue
			}
			listeners = append(listeners, ln)
			go acceptLoop(mux, s.Name, ln, logf)
		}
	}
	defer closeAll()
	if err := mux.SendReady(); err != nil {
		return err
	}
	return mux.Run(ctx)
}

var (
	errNotASocket = errors.New("sockrelay: path exists and is not a socket")
	errLiveSocket = errors.New("sockrelay: path holds a live socket")
)

// listenReplacingStale listens on path, unlinking a leftover socket first.
func listenReplacingStale(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("sockrelay: create %s: %w", filepath.Dir(path), err)
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%w: %s", errNotASocket, path)
		}
		if socketAccepts(path) {
			return nil, fmt.Errorf("%w: %s", errLiveSocket, path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("sockrelay: remove stale %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("sockrelay: inspect %s: %w", path, err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("sockrelay: listen %s: %w", path, err)
	}
	// connect(2) needs write permission on the socket file; the workload runs
	// as the same user, so nobody else needs any.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("sockrelay: chmod %s: %w", path, err)
	}
	return ln, nil
}

// socketAccepts reports whether something answers connect(2) at path. The
// probe sends nothing.
func socketAccepts(path string) bool {
	c, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// acceptLoop feeds accepted connections into mux until the listener closes.
// An Accept error short of that - EMFILE under a descriptor flood, say - is
// logged and retried after a pause rather than ending the listener for good.
func acceptLoop(mux *Mux, name string, ln net.Listener, logf LogFunc) {
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			logf("sockrelay: accept on %s: %v", name, err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Open closes conn itself on failure.
			_ = mux.Open(name, conn)
		}()
	}
}
