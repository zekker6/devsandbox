package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"
)

// ErrNoMonitor indicates no monitor is connected to handle ask requests.
var ErrNoMonitor = errors.New("no monitor connected")

// ErrTimeout indicates the request timed out waiting for user response.
var ErrTimeout = errors.New("request timed out waiting for user response")

// AskRequest is sent from the proxy to the monitor for user approval.
type AskRequest struct {
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Host    string            `json:"host"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// AskResponse is sent from the monitor back to the proxy.
type AskResponse struct {
	ID        string       `json:"id"`
	Action    FilterAction `json:"action"`
	Remember  bool         `json:"remember"`  // Remember for session
	Permanent bool         `json:"permanent"` // Add to config (future)
}

// monitorConn represents a connected monitor client.
type monitorConn struct {
	conn    net.Conn
	encoder *json.Encoder
	decoder *json.Decoder
}

// AskServer manages connections from monitor clients and routes approval requests.
type AskServer struct {
	socketPath string
	listener   net.Listener

	// Connected monitors
	monitors   []*monitorConn
	monitorsMu sync.RWMutex

	// Pending requests waiting for response
	pending   map[string]chan AskResponse
	pendingMu sync.Mutex

	closed bool
	mu     sync.Mutex
}

// NewAskServer creates a new ask mode server.
func NewAskServer(sandboxRoot string) (*AskServer, error) {
	socketDir := AskSocketDir(sandboxRoot)
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create socket directory: %w", err)
	}

	socketPath := AskSocketPath(sandboxRoot)

	// Remove stale socket
	_ = os.Remove(socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on socket: %w", err)
	}

	server := &AskServer{
		socketPath: socketPath,
		listener:   listener,
		pending:    make(map[string]chan AskResponse),
	}

	go server.acceptConnections()

	return server, nil
}

// SocketPath returns the path to the Unix socket.
func (s *AskServer) SocketPath() string {
	return s.socketPath
}

// HasMonitor returns true if at least one monitor is connected.
func (s *AskServer) HasMonitor() bool {
	s.monitorsMu.RLock()
	defer s.monitorsMu.RUnlock()
	return len(s.monitors) > 0
}

// acceptConnections handles incoming connections from monitors.
func (s *AskServer) acceptConnections() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return
			}
			continue
		}

		monitor := &monitorConn{
			conn:    conn,
			encoder: json.NewEncoder(conn),
			decoder: json.NewDecoder(conn),
		}

		s.monitorsMu.Lock()
		s.monitors = append(s.monitors, monitor)
		s.monitorsMu.Unlock()

		go s.handleMonitor(monitor)
	}
}

// handleMonitor reads responses from a connected monitor.
func (s *AskServer) handleMonitor(monitor *monitorConn) {
	defer func() {
		_ = monitor.conn.Close()
		s.removeMonitor(monitor)
	}()

	for {
		var resp AskResponse
		if err := monitor.decoder.Decode(&resp); err != nil {
			return
		}

		// Deliver response to waiting request
		s.pendingMu.Lock()
		ch, ok := s.pending[resp.ID]
		if ok {
			delete(s.pending, resp.ID)
		}
		s.pendingMu.Unlock()

		if ok {
			select {
			case ch <- resp:
			default:
				// Channel full or closed, response already handled
			}
			close(ch)
		}
	}
}

// removeMonitor removes a disconnected monitor from the list.
func (s *AskServer) removeMonitor(monitor *monitorConn) {
	s.monitorsMu.Lock()
	defer s.monitorsMu.Unlock()

	for i, m := range s.monitors {
		if m == monitor {
			s.monitors = append(s.monitors[:i], s.monitors[i+1:]...)
			return
		}
	}
}

// Ask sends a request to connected monitors and waits for a response.
func (s *AskServer) Ask(ctx context.Context, req *AskRequest) (AskResponse, error) {
	// Check if any monitor is connected
	s.monitorsMu.RLock()
	monitors := make([]*monitorConn, len(s.monitors))
	copy(monitors, s.monitors)
	s.monitorsMu.RUnlock()

	if len(monitors) == 0 {
		return AskResponse{}, ErrNoMonitor
	}

	// Create response channel
	ch := make(chan AskResponse, 1)

	s.pendingMu.Lock()
	s.pending[req.ID] = ch
	s.pendingMu.Unlock()

	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, req.ID)
		s.pendingMu.Unlock()
	}()

	// Send request to all connected monitors
	for _, monitor := range monitors {
		if err := monitor.encoder.Encode(req); err != nil {
			// Monitor disconnected, will be cleaned up by handleMonitor
			continue
		}
	}

	// Wait for response or timeout
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return AskResponse{}, ErrTimeout
		}
		return AskResponse{}, ctx.Err()
	}
}

// Close shuts down the ask server.
func (s *AskServer) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()

	// Close all monitor connections
	s.monitorsMu.Lock()
	for _, monitor := range s.monitors {
		_ = monitor.conn.Close()
	}
	s.monitors = nil
	s.monitorsMu.Unlock()

	if s.listener != nil {
		_ = s.listener.Close()
	}
	_ = os.Remove(s.socketPath)

	return nil
}

// AskQueue manages pending approval requests for ask mode.
type AskQueue struct {
	server       *AskServer
	filterEngine *FilterEngine
	timeout      time.Duration
}

// NewAskQueue creates a new ask queue.
func NewAskQueue(server *AskServer, engine *FilterEngine, timeout time.Duration) *AskQueue {
	return &AskQueue{
		server:       server,
		filterEngine: engine,
		timeout:      timeout,
	}
}

// RequestApproval blocks until user approves or denies the request.
// Returns ErrNoMonitor if no monitor is connected.
// Returns ErrTimeout if the request times out.
func (q *AskQueue) RequestApproval(req *AskRequest) (FilterAction, error) {
	ctx, cancel := context.WithTimeout(context.Background(), q.timeout)
	defer cancel()

	resp, err := q.server.Ask(ctx, req)
	if err != nil {
		// Return specific error for logging
		return FilterActionBlock, err
	}

	// Cache decision if requested
	if resp.Remember && q.filterEngine != nil {
		q.filterEngine.CacheDecision(req.Host, resp.Action)
	}

	return resp.Action, nil
}

// Close cleans up the ask queue.
func (q *AskQueue) Close() error {
	return nil
}
