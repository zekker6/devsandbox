package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"devsandbox/internal/config"
	"devsandbox/internal/fsutil"
	"devsandbox/internal/proxy"
	"devsandbox/internal/sandbox"
	"devsandbox/internal/termsafe"
)

// errAskSocketStale is returned when nothing answers on the socket path. It is
// the one failure auto-detection may read as "the file is left over": the
// socket is unlinked and the monitor listens there itself.
var errAskSocketStale = errors.New("failed to connect to ask server")

// errAskPeerRejected is returned when something answered on the socket but did
// not complete the handshake as a proxy. The socket is live and owned - by a
// monitor already, or by a build that predates the hello - so it is left
// alone.
var errAskPeerRejected = errors.New("ask socket peer is not a devsandbox proxy")

func newProxyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Proxy-related commands",
		Long:  `Commands for managing the HTTP proxy, including the ask mode monitor and filter configuration.`,
	}

	cmd.AddCommand(newProxyMonitorCmd())
	cmd.AddCommand(newFilterCmd())

	return cmd
}

func newProxyMonitorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "monitor [socket-path]",
		Short: "Monitor and approve HTTP requests in ask mode",
		Long: `Interactive terminal for approving/denying HTTP requests when proxy is running in ask mode.

Can be started before or after the sandbox:
  - Before: Creates the socket and waits for the sandbox to connect.
  - After:  Connects to the sandbox's existing socket.

Running more than one session of this project in ask mode: start the monitor
first. A second session that finds another session's socket blocks every
ask-mode request for its lifetime. The monitor serves all sessions at once, one
prompt at a time. Monitor and sandbox must be the same devsandbox version.

If no socket path is provided, it will be auto-detected from the current directory's sandbox.

Keys (instant response, no Enter needed):
  a - Allow this request
  b - Block this request
  s - Allow and remember for session
  n - Block and remember for session

Requests that don't receive a response within 30 seconds are automatically rejected.`,
		Example: `  # Auto-detect socket from current project (before or after sandbox)
  devsandbox proxy monitor

  # Explicit socket path (client mode only)
  devsandbox proxy monitor /path/to/ask.sock`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return runProxyMonitor(args[0])
			}

			sandboxBase, err := resolveSandboxBase()
			if err != nil {
				return err
			}

			return connectOrServeMonitor(proxy.AskSocketPath(sandboxBase), sandboxBase, runProxyMonitor, runProxyMonitorServer)
		},
	}
}

// connectOrServeMonitor joins the sandbox listening on socketPath, or listens
// there itself when the path is unused. Only a dial nobody answers means the
// file is stale. Every other failure - a peer that is not a proxy, no
// terminal - is reported as is, because unlinking a socket a running session
// owns would cut that session off from its monitor.
func connectOrServeMonitor(socketPath, sandboxBase string, connect func(string) error, serve func(string) error) error {
	if _, err := os.Stat(socketPath); err == nil {
		connectErr := connect(socketPath)
		if !errors.Is(connectErr, errAskSocketStale) {
			return connectErr
		}
		_ = os.Remove(socketPath)
	}
	return serve(sandboxBase)
}

// resolveSandboxBase returns the sandbox base path for the current directory's project.
func resolveSandboxBase() (string, error) {
	appCfg, _, projectDir, err := config.LoadConfig()
	if err != nil {
		return "", err
	}

	basePath := appCfg.Sandbox.BasePath
	if basePath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		basePath = sandbox.SandboxBasePath(home)
	}

	projectName := sandbox.GenerateSandboxName(projectDir)
	return filepath.Join(basePath, projectName), nil
}

// dialAskSocket connects to the ask socket. A dial nothing answers is the
// stale-socket signal auto-detection acts on.
func dialAskSocket(socketPath string, timeout time.Duration) (net.Conn, error) {
	conn, err := net.DialTimeout("unix", socketPath, timeout)
	if err != nil {
		return nil, fmt.Errorf("%w at %s: %w\nMake sure the sandbox is running with --filter-default=ask", errAskSocketStale, socketPath, err)
	}
	return conn, nil
}

// handshakeWithProxy runs the monitor's side of the ask handshake on a fresh
// connection. initiator is true when the monitor dialed a running sandbox and
// false when it accepted one.
func handshakeWithProxy(conn net.Conn, dec *json.Decoder, enc *json.Encoder, initiator bool) error {
	if err := proxy.AskHandshake(conn, dec, enc, proxy.AskRoleMonitor, initiator); err != nil {
		return fmt.Errorf("%w: %w", errAskPeerRejected, err)
	}
	return nil
}

// decider shows one request and returns the user's answer to it.
type decider func(*proxy.AskRequest) proxy.AskResponse

// newDecider serializes prompts on the terminal: one request is shown at a
// time, so the key pressed answers the request on screen and no other. A
// listening monitor serves every session of the project at once and would
// otherwise interleave their prompts.
//
// The decision window is measured from the request's arrival, not from when
// its prompt is drawn: the proxy's own deadline started when it sent the
// request, and a request that waited behind another prompt for longer than
// that has already been blocked. Drawing it with a fresh window would take an
// answer the proxy discards.
func newDecider(w io.Writer, keyChan <-chan byte) decider {
	var mu sync.Mutex
	return func(req *proxy.AskRequest) proxy.AskResponse {
		deadline := time.Now().Add(askTimeout(req))
		mu.Lock()
		defer mu.Unlock()
		return promptDecision(w, req, keyChan, deadline)
	}
}

// askTimeout is how long the proxy waits for an answer to req.
func askTimeout(req *proxy.AskRequest) time.Duration {
	if req.Timeout > 0 {
		return time.Duration(req.Timeout) * time.Second
	}
	return 30 * time.Second
}

// promptDecision shows req and returns the key that answers it, unless its
// window closed before it reached the screen.
func promptDecision(w io.Writer, req *proxy.AskRequest, keyChan <-chan byte, deadline time.Time) proxy.AskResponse {
	if !time.Now().Before(deadline) {
		emitf(w, "Request from %s expired before it could be shown (the sandbox already blocked it)\r\n", termsafe.Escape(req.Host))
		return proxy.AskResponse{ID: req.ID, Action: proxy.FilterActionBlock}
	}
	displayRequest(w, req)
	return getUserDecision(w, req, keyChan, deadline)
}

// serveProxyConn answers the requests one proxy sends until the connection
// ends. It returns the error that ended it; the caller decides whether that
// was expected.
func serveProxyConn(w io.Writer, dec *json.Decoder, enc *json.Encoder, decide decider) error {
	for {
		var req proxy.AskRequest
		if err := dec.Decode(&req); err != nil {
			return err
		}

		resp := decide(&req)

		if err := enc.Encode(&resp); err != nil {
			return fmt.Errorf("failed to send response: %w", err)
		}

		emit(w, "\r\n")
	}
}

// acceptSandboxes serves every session that dials listener until ctx is
// cancelled, each on its own goroutine. Serving them one after another would
// leave a session that connects while another is being answered waiting in
// the backlog: its hello deadline would pass unanswered, and that session
// would block every request for its lifetime.
func acceptSandboxes(ctx context.Context, w io.Writer, listener net.Listener, decide decider) error {
	var sessions sync.WaitGroup
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				sessions.Wait()
				return nil
			}
			return fmt.Errorf("accept failed: %w", err)
		}

		sessions.Add(1)
		go func() {
			defer sessions.Done()
			serveSandbox(ctx, w, conn, decide)
		}()
	}
}

// serveSandbox handles one accepted connection: whoever dialed has to prove
// it is a proxy before it is shown a single request, and a refused peer is
// dropped without an answer.
func serveSandbox(ctx context.Context, w io.Writer, conn net.Conn, decide decider) {
	// Close the connection on shutdown so a blocked decode unblocks.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	defer func() { _ = conn.Close() }()

	encoder := json.NewEncoder(conn)
	decoder := json.NewDecoder(conn)

	if err := handshakeWithProxy(conn, decoder, encoder, false); err != nil {
		if ctx.Err() == nil {
			emitf(w, "\r\nRefused a connection: %s\r\n\r\n", errText(err))
		}
		return
	}

	emit(w, "Sandbox connected.\r\n\r\n")

	err := serveProxyConn(w, decoder, encoder, decide)
	if ctx.Err() != nil {
		return
	}
	if !errors.Is(err, io.EOF) {
		emitf(w, "\r\nConnection error: %s\r\n", errText(err))
	}
	emit(w, "\r\nSandbox disconnected. Waiting for next connection...\r\n\r\n")
}

func runProxyMonitorServer(sandboxBase string) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("proxy monitor requires an interactive terminal (stdin is not a TTY)")
	}

	socketDir := proxy.AskSocketDir(sandboxBase)
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		return fmt.Errorf("failed to create socket directory: %w", err)
	}

	lockPath := proxy.AskLockPath(sandboxBase)
	lock, err := fsutil.TryFileLock(lockPath)
	if err != nil {
		return fmt.Errorf("another monitor already owns this socket: %w", err)
	}
	defer func() { _ = lock.Release() }()

	socketPath := proxy.AskSocketPath(sandboxBase)
	_ = os.Remove(socketPath) // Clean up any stale socket

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("failed to create socket: %w", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()

	// Set terminal to raw mode
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set raw terminal mode: %w", err)
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()

	// Shutdown coordination — cancel context instead of os.Exit so defers run
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Keyboard input channel
	keyChan := make(chan byte, 10)
	go func() {
		buf := make([]byte, 1)
		for {
			n, readErr := os.Stdin.Read(buf)
			if readErr != nil || n == 0 {
				return
			}
			if buf[0] == 3 { // Ctrl+C
				cancel()
				_ = listener.Close()
				return
			}
			keyChan <- buf[0]
		}
	}()

	// Signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigChan:
			cancel()
			_ = listener.Close()
		case <-ctx.Done():
		}
	}()

	out := os.Stdout
	printHeader(out)
	emit(out, "Waiting for sandbox to connect...\r\n\r\n")

	// Serve sessions until shutdown (persists across sandbox restarts)
	err = acceptSandboxes(ctx, out, listener, newDecider(out, keyChan))
	if ctx.Err() != nil {
		emit(out, "\r\nExiting monitor...\r\n")
		return nil
	}
	return err
}

func runProxyMonitor(socketPath string) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("proxy monitor requires an interactive terminal (stdin is not a TTY)")
	}

	// Connect to the ask server
	conn, err := dialAskSocket(socketPath, 2*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	encoder := json.NewEncoder(conn)
	decoder := json.NewDecoder(conn)

	// The socket path is per project, so what answered may be another
	// session's proxy rather than the one this monitor was started for.
	if err := handshakeWithProxy(conn, decoder, encoder, true); err != nil {
		return fmt.Errorf("ask socket %s: %w", socketPath, err)
	}

	// Set terminal to raw mode for single-key input
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set raw terminal mode: %w", err)
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()

	// Shutdown coordination — cancel context instead of os.Exit so defers run
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Channel for keyboard input
	keyChan := make(chan byte, 10)

	// Read keyboard in background goroutine
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil || n == 0 {
				return
			}
			if buf[0] == 3 { // Ctrl+C
				cancel()
				_ = conn.Close()
				return
			}
			keyChan <- buf[0]
		}
	}()

	// Handle signals for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigChan:
			cancel()
			_ = conn.Close()
		case <-ctx.Done():
		}
	}()

	out := os.Stdout
	printHeader(out)

	// Read requests from the server
	err = serveProxyConn(out, decoder, encoder, newDecider(out, keyChan))
	if ctx.Err() != nil {
		emit(out, "\r\nExiting monitor...\r\n")
		return nil
	}
	emitf(out, "\r\nConnection closed: %s\r\n", errText(err))
	return nil
}

func printHeader(w io.Writer) {
	emit(w, "╔════════════════════════════════════════════════════════════════╗\r\n")
	emit(w, "║            devsandbox HTTP Request Monitor                     ║\r\n")
	emit(w, "╠════════════════════════════════════════════════════════════════╣\r\n")
	emit(w, "║  Waiting for requests...                                       ║\r\n")
	emit(w, "║  Keys: [a]llow  [b]lock  [s]ession-allow  [n]ever-allow        ║\r\n")
	emit(w, "║  Requests timeout after 30 seconds (auto-reject)               ║\r\n")
	emit(w, "╚════════════════════════════════════════════════════════════════╝\r\n")
	emit(w, "\r\n")
}

// Every string in a request is the sandbox's to choose, and the monitor draws
// it on a raw-mode terminal that acts on whatever control sequence it is
// handed: an ESC in a path could clear the screen and repaint the prompt over
// a different host than the one being approved. Nothing the peer sent reaches
// the terminal except through cell or errText.

// cell renders a sandbox-supplied string into a box column: escaped so the
// terminal shows it, then cut to the column width in runes. Escaping goes
// first because it can lengthen the text, and cutting escaped text cannot
// expose a control character - only printable ASCII is left around the cut.
func cell(s string, width int) string {
	return termsafe.Truncate(termsafe.Escape(s), width)
}

// errText renders an error for the terminal. Its text can carry bytes the
// peer chose - a decode error quotes what it choked on, a refused hello the
// role it claimed - so it is escaped like a request field.
func errText(err error) string {
	return termsafe.Escape(err.Error())
}

// emit and emitf write monitor output. A failed write to the user's own
// terminal has no remedy but stopping, and the connection loop stops by itself
// when the peer goes away, so the error is dropped instead of being threaded
// through every prompt.
func emit(w io.Writer, s string) { _, _ = io.WriteString(w, s) }

func emitf(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }

func displayRequest(w io.Writer, req *proxy.AskRequest) {
	emit(w, "┌──────────────────────────────────────────────────────────────────┐\r\n")
	emitf(w, "│  %-64s│\r\n", cell("Request #"+req.ID, 64))
	emit(w, "├──────────────────────────────────────────────────────────────────┤\r\n")
	emitf(w, "│  Method: %-56s│\r\n", cell(req.Method, 56))
	emitf(w, "│  Host:   %-56s│\r\n", cell(req.Host, 56))
	emitf(w, "│  Path:   %-56s│\r\n", cell(req.Path, 56))

	if len(req.Headers) > 0 {
		emit(w, "├──────────────────────────────────────────────────────────────────┤\r\n")
		for k, v := range req.Headers {
			emitf(w, "│  %-64s│\r\n", cell(k+": "+v, 64))
		}
	}

	if req.Body != "" {
		emit(w, "├──────────────────────────────────────────────────────────────────┤\r\n")
		emit(w, "│  Body preview:                                                   │\r\n")
		emitf(w, "│  %-64s│\r\n", cell(req.Body, 64))
	}

	emit(w, "├──────────────────────────────────────────────────────────────────┤\r\n")
	emit(w, "│  [a]llow    [b]lock    [s]ession-allow    [n]ever-allow          │\r\n")
	emit(w, "└──────────────────────────────────────────────────────────────────┘\r\n")
}

func getUserDecision(w io.Writer, req *proxy.AskRequest, keyChan <-chan byte, deadline time.Time) proxy.AskResponse {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()

	host := termsafe.Escape(req.Host)
	emit(w, "Decision: ")

	for {
		select {
		case key, ok := <-keyChan:
			if !ok {
				return proxy.AskResponse{ID: req.ID, Action: proxy.FilterActionBlock}
			}
			resp := proxy.AskResponse{ID: req.ID}

			switch key {
			case 'a', 'A', 'y', 'Y':
				resp.Action = proxy.FilterActionAllow
				emitf(w, "%c\r\n✓ Allowed: %s\r\n", key, host)
				return resp

			case 'b', 'B':
				resp.Action = proxy.FilterActionBlock
				emitf(w, "%c\r\n✗ Blocked: %s\r\n", key, host)
				return resp

			case 's', 'S':
				resp.Action = proxy.FilterActionAllow
				resp.Remember = true
				emitf(w, "%c\r\n✓ Allowed for session: %s\r\n", key, host)
				return resp

			case 'n', 'N':
				resp.Action = proxy.FilterActionBlock
				resp.Remember = true
				emitf(w, "%c\r\n✗ Blocked for session: %s\r\n", key, host)
				return resp

			default:
				continue
			}

		case <-timer.C:
			emit(w, "\r\n  Request timed out (auto-rejected)\r\n")
			return proxy.AskResponse{ID: req.ID, Action: proxy.FilterActionBlock}
		}
	}
}
