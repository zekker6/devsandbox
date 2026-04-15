package portforward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
)

const maxConnections = 1024

// Forwarder proxies TCP connections from a host-side listener through a Dialer
// into a sandbox. HostPort=0 lets the OS assign an ephemeral port; call
// ActualHostPort after Start to retrieve it.
type Forwarder struct {
	HostPort    int    // Desired host port (0 = auto-assign)
	SandboxPort int    // Target port inside the sandbox
	Bind        string // Host bind address (default "127.0.0.1")
	Dialer      Dialer // Namespace-aware dialer

	listener   net.Listener
	actualPort int
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	sem        chan struct{}
	active     atomic.Int64
}

// Start opens the host listener and begins accepting connections. It returns
// an error if the listener cannot be opened. The provided ctx is used as the
// parent context for the accept loop; call Stop to shut down cleanly.
func (f *Forwarder) Start(ctx context.Context) error {
	bind := f.Bind
	if bind == "" {
		bind = "127.0.0.1"
	}

	addr := fmt.Sprintf("%s:%d", bind, f.HostPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	f.listener = ln
	f.actualPort = ln.Addr().(*net.TCPAddr).Port
	f.sem = make(chan struct{}, maxConnections)

	loopCtx, cancel := context.WithCancel(ctx)
	f.cancel = cancel

	f.wg.Go(func() {
		f.acceptLoop(loopCtx)
	})

	return nil
}

// StartWithFallback is like Start but retries on an ephemeral host port when
// the preferred HostPort is already in use on the host. This is the sensible
// default for auto-forwarding: a sandbox listening on a port that the host
// also happens to use (common with ephemeral-range ports) should still be
// reachable, just on a different host port. Other bind errors are returned
// as-is. The returned int is the actual host port bound; fellBack indicates
// whether the fallback path was taken.
func (f *Forwarder) StartWithFallback(ctx context.Context) (actualPort int, fellBack bool, err error) {
	err = f.Start(ctx)
	if err == nil {
		return f.actualPort, false, nil
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		return 0, false, err
	}
	f.HostPort = 0
	if err := f.Start(ctx); err != nil {
		return 0, false, err
	}
	return f.actualPort, true, nil
}

// Stop cancels the accept loop, closes the listener, and waits for all
// in-flight connections to finish.
func (f *Forwarder) Stop() {
	if f.cancel != nil {
		f.cancel()
	}
	if f.listener != nil {
		_ = f.listener.Close()
	}
	f.wg.Wait()
}

// ActualHostPort returns the port the listener is bound to. Useful when
// HostPort was 0 and the OS assigned an ephemeral port.
func (f *Forwarder) ActualHostPort() int {
	return f.actualPort
}

// ActiveConnections returns the number of connections currently being proxied.
func (f *Forwarder) ActiveConnections() int64 {
	return f.active.Load()
}

func (f *Forwarder) acceptLoop(ctx context.Context) {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			// Distinguish a deliberate close from a real error. Either way, exit.
			select {
			case <-ctx.Done():
			default:
			}
			return
		}

		// Acquire semaphore slot to bound concurrency.
		select {
		case f.sem <- struct{}{}:
		case <-ctx.Done():
			_ = conn.Close()
			return
		}

		f.wg.Add(1)
		go func(c net.Conn) {
			defer f.wg.Done()
			defer func() { <-f.sem }()
			f.handleConn(ctx, c)
		}(conn)
	}
}

func (f *Forwarder) handleConn(ctx context.Context, hostConn net.Conn) {
	defer func() { _ = hostConn.Close() }()

	f.active.Add(1)
	defer f.active.Add(-1)

	sandboxAddr := fmt.Sprintf("127.0.0.1:%d", f.SandboxPort)
	sandboxConn, err := f.Dialer.DialContext(ctx, "tcp", sandboxAddr)
	if err != nil {
		return
	}
	defer func() { _ = sandboxConn.Close() }()

	var wg sync.WaitGroup
	wg.Add(2)

	// host → sandbox
	go func() {
		defer wg.Done()
		_, _ = io.Copy(sandboxConn, hostConn)
		closeWrite(sandboxConn)
	}()

	// sandbox → host
	go func() {
		defer wg.Done()
		_, _ = io.Copy(hostConn, sandboxConn)
		closeWrite(hostConn)
	}()

	wg.Wait()
}

// closeWrite signals EOF on the write side of a connection so the remote
// peer receives a FIN instead of waiting for the connection to close entirely.
// Works for *net.TCPConn and any conn that exposes CloseWrite() (e.g. our
// subprocess-backed NamespaceDialer connection).
func closeWrite(conn net.Conn) {
	type halfCloser interface {
		CloseWrite() error
	}
	if hc, ok := conn.(halfCloser); ok {
		_ = hc.CloseWrite()
	}
}
