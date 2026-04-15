package portforward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"time"
)

// Dialer creates connections, optionally in a different network namespace.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// NamespaceDialer dials TCP connections inside the network namespace of the
// given target PID by delegating to an external helper process.
//
// Why a subprocess?  Entering a user namespace via setns(2) with
// CLONE_NEWUSER requires the calling process to be single-threaded, which Go
// processes never are (sysmon, GC, etc). So NamespaceDialer spawns `nsenter`
// to enter the target PID's user + network namespaces, which in turn execs
// a short-lived helper subcommand (`devsandbox __nsdial <host:port>`) that
// opens the TCP connection and bridges stdin<->socket<->stdout. The parent
// wraps the subprocess's pipes as a net.Conn.
type NamespaceDialer struct {
	pid int

	// HelperBinary is the absolute path to a binary that exposes the
	// `__nsdial <host:port>` subcommand. When empty, os.Executable() is
	// used — correct for production (devsandbox dialing from its own
	// `forward` command), overridable for tests.
	HelperBinary string

	// NsenterPath is the path to the nsenter binary. When empty, looked up
	// on PATH.
	NsenterPath string
}

// NewNamespaceDialer creates a NamespaceDialer that targets the namespaces of
// the host process with the given PID.
func NewNamespaceDialer(pid int) *NamespaceDialer {
	return &NamespaceDialer{pid: pid}
}

// DialContext launches the namespace-entering helper and returns a net.Conn
// backed by its stdio. Closing the returned conn kills the helper.
func (d *NamespaceDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("namespace dialer only supports tcp, got %q", network)
	}

	nsenter := d.NsenterPath
	if nsenter == "" {
		path, err := exec.LookPath("nsenter")
		if err != nil {
			return nil, fmt.Errorf("nsenter not found on PATH (required for port forwarding): %w", err)
		}
		nsenter = path
	}

	helper := d.HelperBinary
	if helper == "" {
		self, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("resolve own executable: %w", err)
		}
		helper = self
	}

	args := []string{
		"--target", fmt.Sprintf("%d", d.pid),
		"--user", "--net",
		"--preserve-credentials",
		"--",
		helper, "__nsdial", address,
	}
	cmd := exec.CommandContext(ctx, nsenter, args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	// Capture stderr for diagnostics on dial failure. Bounded buffer.
	stderrBuf := &cappedBuffer{max: 4096}
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start helper: %w", err)
	}

	return &helperConn{
		cmd:       cmd,
		stdin:     stdin,
		stdout:    stdout,
		stderrBuf: stderrBuf,
		localAddr: helperAddr{label: "ns-dialer-stdio"},
		remoteAddr: helperAddr{
			label: fmt.Sprintf("pid=%d %s", d.pid, address),
		},
	}, nil
}

// helperConn adapts a running helper subprocess's stdio to the net.Conn
// interface.
type helperConn struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	stderrBuf  *cappedBuffer
	localAddr  net.Addr
	remoteAddr net.Addr
	closeOnce  onceErr
}

func (c *helperConn) Read(b []byte) (int, error) {
	n, err := c.stdout.Read(b)
	if err == io.EOF || (err != nil && n == 0) {
		// If the helper terminated before producing data, surface its stderr
		// so callers see why (e.g. "dial: connection refused").
		if stderrErr := c.stderrError(); stderrErr != nil {
			return n, stderrErr
		}
	}
	return n, err
}

func (c *helperConn) Write(b []byte) (int, error) {
	return c.stdin.Write(b)
}

// CloseWrite sends EOF to the helper's stdin, which makes the helper's
// stdin->conn goroutine issue a TCP FIN toward the sandbox peer. This gives
// well-behaved peers a chance to flush before we tear down.
func (c *helperConn) CloseWrite() error {
	return c.stdin.Close()
}

func (c *helperConn) Close() error {
	return c.closeOnce.Do(func() error {
		_ = c.stdin.Close()
		_ = c.stdout.Close()
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		_ = c.cmd.Wait()
		return nil
	})
}

func (c *helperConn) LocalAddr() net.Addr  { return c.localAddr }
func (c *helperConn) RemoteAddr() net.Addr { return c.remoteAddr }

// Deadlines aren't meaningful for a pipe-backed conn; the forwarder
// doesn't rely on them, so we return nil instead of erroring (some callers
// set a zero deadline to clear, and must see success).
func (c *helperConn) SetDeadline(time.Time) error      { return nil }
func (c *helperConn) SetReadDeadline(time.Time) error  { return nil }
func (c *helperConn) SetWriteDeadline(time.Time) error { return nil }

func (c *helperConn) stderrError() error {
	if c.stderrBuf == nil {
		return nil
	}
	msg := c.stderrBuf.String()
	if msg == "" {
		return nil
	}
	return errors.New(msg)
}

// helperAddr is a trivial net.Addr implementation for helperConn; its only
// role is to give log messages something to show.
type helperAddr struct{ label string }

func (a helperAddr) Network() string { return "ns-dialer" }
func (a helperAddr) String() string  { return a.label }

// cappedBuffer is an io.Writer that retains up to max bytes and silently
// drops the rest. Safe for concurrent Write (guarded by the subprocess
// machinery writing serially).
type cappedBuffer struct {
	max  int
	buf  []byte
	full bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.full {
		return len(p), nil
	}
	remaining := b.max - len(b.buf)
	if remaining <= 0 {
		b.full = true
		return len(p), nil
	}
	take := len(p)
	if take > remaining {
		take = remaining
		b.full = true
	}
	b.buf = append(b.buf, p[:take]...)
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	return string(b.buf)
}

// onceErr runs an action at most once and caches its error.
type onceErr struct {
	done bool
	err  error
}

func (o *onceErr) Do(f func() error) error {
	if o.done {
		return o.err
	}
	o.done = true
	o.err = f()
	return o.err
}
