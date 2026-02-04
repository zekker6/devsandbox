package dockerproxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// Logger is an interface for logging proxy events.
// This is compatible with logging.ErrorLogger.
type Logger interface {
	LogErrorf(component, format string, args ...any)
	LogInfof(component, format string, args ...any)
}

// Proxy is a filtering proxy for the Docker socket.
// It logs errors to the provided logger if set, otherwise errors are silent.
type Proxy struct {
	hostSocket string
	listenPath string
	listener   net.Listener
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	logger     Logger
}

// New creates a new Docker socket proxy.
func New(hostSocket, listenPath string) *Proxy {
	return &Proxy{
		hostSocket: hostSocket,
		listenPath: listenPath,
	}
}

// SetLogger sets the logger for proxy errors.
// If nil, errors are not logged.
func (p *Proxy) SetLogger(logger Logger) {
	p.logger = logger
}

// logError logs an error if a logger is configured.
func (p *Proxy) logError(format string, args ...any) {
	if p.logger != nil {
		p.logger.LogErrorf("docker-proxy", format, args...)
	}
}

// logInfo logs an info message if a logger is configured.
func (p *Proxy) logInfo(format string, args ...any) {
	if p.logger != nil {
		p.logger.LogInfof("docker-proxy", format, args...)
	}
}

// Start begins listening and proxying requests.
func (p *Proxy) Start(ctx context.Context) error {
	// Remove existing socket if present
	_ = os.Remove(p.listenPath)

	listener, err := net.Listen("unix", p.listenPath)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", p.listenPath, err)
	}
	p.listener = listener

	// Make socket accessible
	if err := os.Chmod(p.listenPath, 0o666); err != nil {
		_ = listener.Close()
		return fmt.Errorf("failed to chmod socket: %w", err)
	}

	p.ctx, p.cancel = context.WithCancel(ctx)

	p.wg.Add(1)
	go p.acceptLoop()

	return nil
}

// Stop gracefully shuts down the proxy.
func (p *Proxy) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.listener != nil {
		_ = p.listener.Close()
	}

	// Wait for connections to drain with timeout
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("timeout waiting for connections to close")
	}
}

func (p *Proxy) acceptLoop() {
	defer p.wg.Done()

	for {
		conn, err := p.listener.Accept()
		if err != nil {
			select {
			case <-p.ctx.Done():
				return
			default:
				p.logError("failed to accept connection: %v", err)
				continue
			}
		}

		p.wg.Add(1)
		go p.handleConnection(conn)
	}
}

func (p *Proxy) handleConnection(conn net.Conn) {
	defer p.wg.Done()
	defer func() { _ = conn.Close() }()

	// Parse HTTP request
	reader := bufio.NewReader(conn)
	req, err := http.ReadRequest(reader)
	if err != nil {
		// Connection closed or malformed request - common for connection probes
		if err != io.EOF {
			p.logError("failed to parse request: %v", err)
		}
		return
	}

	// Check if allowed
	if !IsAllowed(req.Method, req.URL.Path) {
		reason := DenyReason(req.Method, req.URL.Path)
		p.logInfo("request denied: %s %s - %s", req.Method, req.URL.Path, reason)
		p.sendError(conn, http.StatusForbidden, reason)
		return
	}

	// Log and forward to Docker daemon
	p.logInfo("request allowed: %s %s", req.Method, req.URL.Path)
	p.forwardRequest(conn, req, reader)
}

func (p *Proxy) forwardRequest(clientConn net.Conn, req *http.Request, clientReader *bufio.Reader) {
	// Connect to Docker daemon
	dockerConn, err := net.Dial("unix", p.hostSocket)
	if err != nil {
		p.logError("failed to connect to Docker daemon: %v", err)
		p.sendError(clientConn, http.StatusBadGateway, "failed to connect to Docker daemon")
		return
	}
	defer func() { _ = dockerConn.Close() }()

	// Forward the request
	if err := req.Write(dockerConn); err != nil {
		p.logError("failed to forward request %s %s: %v", req.Method, req.URL.Path, err)
		p.sendError(clientConn, http.StatusBadGateway, "failed to forward request")
		return
	}

	// Read response
	dockerReader := bufio.NewReader(dockerConn)
	resp, err := http.ReadResponse(dockerReader, req)
	if err != nil {
		p.logError("failed to read response for %s %s: %v", req.Method, req.URL.Path, err)
		p.sendError(clientConn, http.StatusBadGateway, "failed to read response")
		return
	}

	// Check for connection upgrade (exec/attach)
	if resp.StatusCode == http.StatusSwitchingProtocols {
		// Write response headers
		if err := resp.Write(clientConn); err != nil {
			p.logError("failed to write upgrade response: %v", err)
			return
		}
		// Bidirectional copy
		p.hijackConnection(clientConn, dockerConn, clientReader, dockerReader)
		return
	}

	// Write response to client
	if err := resp.Write(clientConn); err != nil {
		p.logError("failed to write response for %s %s: %v", req.Method, req.URL.Path, err)
	}
}

func (p *Proxy) hijackConnection(client, docker net.Conn, clientReader, dockerReader *bufio.Reader) {
	var g errgroup.Group

	// Client -> Docker
	g.Go(func() error {
		defer func() {
			if tc, ok := docker.(*net.UnixConn); ok {
				_ = tc.CloseWrite()
			}
		}()

		// First copy any buffered data
		if clientReader.Buffered() > 0 {
			buffered := make([]byte, clientReader.Buffered())
			n, err := clientReader.Read(buffered)
			if err != nil && err != io.EOF {
				p.logError("failed to read buffered client data: %v", err)
				return err
			}
			if _, err := docker.Write(buffered[:n]); err != nil {
				p.logError("failed to write buffered data to docker: %v", err)
				return err
			}
		}
		if _, err := io.Copy(docker, client); err != nil {
			// Connection reset is normal when exec session ends
			if !isConnectionClosed(err) {
				p.logError("client->docker copy error: %v", err)
				return err
			}
		}
		return nil
	})

	// Docker -> Client
	g.Go(func() error {
		defer func() {
			if tc, ok := client.(*net.UnixConn); ok {
				_ = tc.CloseWrite()
			}
		}()

		// First copy any buffered data
		if dockerReader.Buffered() > 0 {
			buffered := make([]byte, dockerReader.Buffered())
			n, err := dockerReader.Read(buffered)
			if err != nil && err != io.EOF {
				p.logError("failed to read buffered docker data: %v", err)
				return err
			}
			if _, err := client.Write(buffered[:n]); err != nil {
				p.logError("failed to write buffered data to client: %v", err)
				return err
			}
		}
		if _, err := io.Copy(client, docker); err != nil {
			// Connection reset is normal when exec session ends
			if !isConnectionClosed(err) {
				p.logError("docker->client copy error: %v", err)
				return err
			}
		}
		return nil
	})

	// Wait for both goroutines; errors are already logged
	_ = g.Wait()
}

// isConnectionClosed returns true if the error indicates a closed connection.
// These errors are expected when a connection is terminated normally.
func isConnectionClosed(err error) bool {
	if err == nil || err == io.EOF {
		return true
	}
	// Check for common connection close errors
	if netErr, ok := err.(*net.OpError); ok {
		return netErr.Err.Error() == "use of closed network connection"
	}
	return false
}

func (p *Proxy) sendError(conn net.Conn, status int, message string) {
	resp := &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
	}
	resp.Header.Set("Content-Type", "text/plain")
	body := []byte(message + "\n")
	resp.ContentLength = int64(len(body))
	if err := resp.Write(conn); err != nil {
		p.logError("failed to write error response header: %v", err)
		return
	}
	if _, err := conn.Write(body); err != nil {
		p.logError("failed to write error response body: %v", err)
	}
}
