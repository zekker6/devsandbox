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

	"devsandbox/internal/notice"
)

// ErrNoMonitor indicates no monitor is connected to handle ask requests.
var ErrNoMonitor = errors.New("no monitor connected")

// ErrTimeout indicates the request timed out waiting for user response.
var ErrTimeout = errors.New("request timed out waiting for user response")

// AskMode represents how the AskServer operates.
type AskMode string

const (
	// AskModeServer means AskServer owns the socket and listens for monitor connections.
	AskModeServer AskMode = "server"
	// AskModeClient means AskServer connects to a pre-existing monitor socket as a client.
	AskModeClient AskMode = "client"
)

// AskProto is the ask socket protocol every hello names. A peer speaking
// another version is refused rather than guessed at, so bump it when the
// message shapes change.
const AskProto = "devsandbox-ask/1"

// AskRole says which end of the ask socket a process is.
type AskRole string

const (
	AskRoleProxy   AskRole = "proxy"
	AskRoleMonitor AskRole = "monitor"
)

// peer returns the role a connection from r must find on the other end.
func (r AskRole) peer() AskRole {
	if r == AskRoleProxy {
		return AskRoleMonitor
	}
	return AskRoleProxy
}

// AskHello is the first JSON object each end sends on an ask socket
// connection, before any request or response.
//
// The socket path is per project, and which side listens depends only on who
// started first: a proxy that finds the socket live dials it and took whatever
// answered as its monitor, while a listening proxy admitted whoever dialed.
// Two concurrent sessions of one project therefore registered each other -
// one's requests were decoded by the other as responses, and a matching id
// resolved a pending request nobody had been prompted for. The hello names
// the speaker's role so each side can require the opposite one before it
// exchanges a single request.
type AskHello struct {
	Proto string  `json:"proto"`
	Role  AskRole `json:"role"`
}

// askHelloTimeout bounds how long either end waits for the peer's hello. A
// peer that never sends one is refused, not admitted after a grace period.
var askHelloTimeout = 5 * time.Second

// errAskHelloTimeout is the handshake failure for a peer that sent nothing
// before the hello deadline. A build that predates the hello never sends one,
// so this cause names an older monitor or session, not a wrong-role peer.
var errAskHelloTimeout = errors.New("no hello from peer")

// askReconnectInterval is how often a client-mode AskServer re-dials the
// socket after its monitor disconnects.
var askReconnectInterval = time.Second

// AskHandshake exchanges hellos on a freshly opened connection. The dialer
// (initiator) sends its hello first and then requires the listener's; the
// listener reads first and answers only a peer it accepts, so a refused peer
// learns nothing. dec and enc must be the pair the message loop uses
// afterwards: the decoder buffers past the hello, and a second decoder on
// conn would lose those bytes.
func AskHandshake(conn net.Conn, dec *json.Decoder, enc *json.Encoder, role AskRole, initiator bool) error {
	return askHandshake(conn, dec, enc, role, initiator, askHelloTimeout)
}

func askHandshake(conn net.Conn, dec *json.Decoder, enc *json.Encoder, role AskRole, initiator bool, timeout time.Duration) error {
	hello := AskHello{Proto: AskProto, Role: role}
	if initiator {
		if err := enc.Encode(hello); err != nil {
			return fmt.Errorf("send hello: %w", err)
		}
	}

	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("set hello deadline: %w", err)
	}
	var peer AskHello
	err := dec.Decode(&peer)
	if clearErr := conn.SetReadDeadline(time.Time{}); err == nil && clearErr != nil {
		return fmt.Errorf("clear hello deadline: %w", clearErr)
	}
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return fmt.Errorf("%w within %s", errAskHelloTimeout, timeout)
		}
		return fmt.Errorf("read hello: %w", err)
	}
	if peer.Proto != AskProto {
		return fmt.Errorf("peer sent no %q hello (got %q)", AskProto, peer.Proto)
	}
	if want := role.peer(); peer.Role != want {
		return fmt.Errorf("peer is a %q, not a %q", peer.Role, want)
	}

	if !initiator {
		if err := enc.Encode(hello); err != nil {
			return fmt.Errorf("send hello: %w", err)
		}
	}
	return nil
}

// askDeadError is what the user is told when the socket's owner refused the
// handshake. It names the fix rather than the mechanism, and the fix depends
// on the cause: a peer that sent no hello at all is a monitor or session from
// a build that predates the handshake, while a peer that answered with the
// wrong role is a concurrent session of the same project - the socket path is
// per project, and a monitor started first is what both sessions would have
// found.
func askDeadError(socketPath string, cause error) error {
	if errors.Is(cause, errAskHelloTimeout) {
		return fmt.Errorf("ask mode: the ask socket %s is owned by a monitor or session from an older devsandbox build, not a monitor this build can talk to (%v); every ask-mode request in this session will be blocked. Run `devsandbox proxy monitor` from this build, and start it before launching concurrent sessions", socketPath, cause)
	}
	return fmt.Errorf("ask mode: the ask socket %s is owned by another devsandbox session, not a monitor (%v); every ask-mode request in this session will be blocked. Start `devsandbox proxy monitor` before launching concurrent sessions", socketPath, cause)
}

// AskRequest is sent from the proxy to the monitor for user approval.
type AskRequest struct {
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Host    string            `json:"host"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	Timeout int               `json:"timeout,omitempty"` // Seconds until auto-reject; 0 = 30s default
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

// askLogger is where the ask server and queue report a peer they refused and
// a monitor answer they refused to act on. It is the method set of
// goproxy.Logger so the proxy's own logger fits.
type askLogger interface {
	Printf(format string, v ...any)
}

// AskServer manages connections from monitor clients and routes approval requests.
type AskServer struct {
	mode       AskMode
	socketPath string
	logger     askLogger

	// Captured at construction so the goroutines below never read the
	// package-level knobs a test may be resetting.
	helloTimeout      time.Duration
	reconnectInterval time.Duration

	// Server mode fields
	listener   net.Listener
	monitors   []*monitorConn
	monitorsMu sync.RWMutex

	// Client mode fields
	conn     net.Conn
	encoder  *json.Encoder
	decoder  *json.Decoder
	clientMu sync.Mutex
	// handshakeErr records that the socket's owner did not identify as a
	// monitor. Once set, no monitor is ever connected again: Ask reports
	// ErrNoMonitor and the reconnect loop has stopped.
	handshakeErr error

	// Shared fields
	pending   map[string]chan AskResponse
	pendingMu sync.Mutex

	closed bool
	mu     sync.Mutex
}

// NewAskServer creates a new ask mode server. A peer it refuses on the socket
// is reported through logger.
// If an existing monitor socket is detected and responsive, connects as a client.
// Otherwise, creates its own socket and listens for monitor connections (server mode).
func NewAskServer(sandboxRoot string, logger askLogger) (*AskServer, error) {
	socketDir := AskSocketDir(sandboxRoot)
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create socket directory: %w", err)
	}

	socketPath := AskSocketPath(sandboxRoot)

	// Check if a monitor is already listening on the socket
	if _, err := os.Stat(socketPath); err == nil {
		conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
		if err == nil {
			// Socket is live - join it as a client
			return newAskClient(socketPath, conn, logger), nil
		}
		// Stale socket — remove and fall through to server mode
		_ = os.Remove(socketPath)
	}

	// Server mode: create socket and listen
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on socket: %w", err)
	}

	server := &AskServer{
		mode:         AskModeServer,
		socketPath:   socketPath,
		logger:       logger,
		helloTimeout: askHelloTimeout,
		listener:     listener,
		pending:      make(map[string]chan AskResponse),
	}

	go server.acceptConnections()

	return server, nil
}

// newAskClient joins a live socket. Whoever owns it has to identify as a
// monitor before a single request is sent; otherwise the server starts in the
// dead state, where every Ask reports no monitor and nothing re-dials. That
// is not a launch failure - the socket's owner could trigger one at will -
// so the refusal is exposed through HandshakeError for the caller to report.
func newAskClient(socketPath string, conn net.Conn, logger askLogger) *AskServer {
	server := &AskServer{
		mode:              AskModeClient,
		socketPath:        socketPath,
		logger:            logger,
		helloTimeout:      askHelloTimeout,
		reconnectInterval: askReconnectInterval,
		pending:           make(map[string]chan AskResponse),
	}

	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)
	if err := askHandshake(conn, dec, enc, AskRoleProxy, true, server.helloTimeout); err != nil {
		_ = conn.Close()
		server.handshakeErr = askDeadError(socketPath, err)
		return server
	}

	server.conn = conn
	server.decoder = dec
	server.encoder = enc
	go server.handleClientResponses()
	return server
}

// Mode returns the operating mode of the AskServer.
func (s *AskServer) Mode() AskMode {
	return s.mode
}

// SocketPath returns the path to the Unix socket.
func (s *AskServer) SocketPath() string {
	return s.socketPath
}

// HasMonitor returns true if at least one monitor is connected (server mode)
// or the client connection is still alive (client mode).
func (s *AskServer) HasMonitor() bool {
	if s.mode == AskModeClient {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.conn != nil && !s.closed
	}
	s.monitorsMu.RLock()
	defer s.monitorsMu.RUnlock()
	return len(s.monitors) > 0
}

// HandshakeError reports why a client-mode AskServer has no monitor and will
// not get one: the socket was live, but its owner did not identify as a
// monitor. It is nil while a monitor is connected or being re-dialed. The
// caller owns telling the user about a refusal at launch, where a Warn lands
// on the confirmation gate; a refusal during a reconnect is raised as an Alert
// here, because by then the workload owns the terminal.
func (s *AskServer) HandshakeError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handshakeErr
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

		go s.admitMonitor(conn)
	}
}

// admitMonitor registers conn as a monitor once it has identified as one. The
// handshake runs off the accept loop so a silent peer holds up nobody else,
// and the registration checks closed under the monitors lock so a connection
// admitted while Close runs is closed rather than stranded. A refused peer is
// told nothing, so the log line is the only record of why a monitor - one
// from an older build, say - never got a request.
func (s *AskServer) admitMonitor(conn net.Conn) {
	monitor := &monitorConn{
		conn:    conn,
		encoder: json.NewEncoder(conn),
		decoder: json.NewDecoder(conn),
	}
	if err := askHandshake(conn, monitor.decoder, monitor.encoder, AskRoleProxy, false, s.helloTimeout); err != nil {
		_ = conn.Close()
		s.logger.Printf("ask mode: refused a connection on %s: %v", s.socketPath, err)
		return
	}

	s.monitorsMu.Lock()
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		s.monitorsMu.Unlock()
		_ = conn.Close()
		return
	}
	s.monitors = append(s.monitors, monitor)
	s.monitorsMu.Unlock()

	s.handleMonitor(monitor)
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

// handleClientResponses reads responses from the monitor in client mode.
// If the monitor disconnects, it retries connecting until Close() is called
// or a re-dial reaches something that is not a monitor.
func (s *AskServer) handleClientResponses() {
	for {
		// Read responses from the current connection
		for {
			var resp AskResponse
			if err := s.decoder.Decode(&resp); err != nil {
				s.mu.Lock()
				closed := s.closed
				s.mu.Unlock()
				if closed {
					return
				}
				break // Connection lost, enter reconnect loop
			}

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
				}
				close(ch)
			}
		}

		// Monitor disconnected — mark dead and cancel all pending requests
		s.mu.Lock()
		s.conn = nil
		s.mu.Unlock()

		s.pendingMu.Lock()
		for id, ch := range s.pending {
			close(ch)
			delete(s.pending, id)
		}
		s.pendingMu.Unlock()

		// Reconnect loop: retry until the socket is available again
		for {
			s.mu.Lock()
			if s.closed {
				s.mu.Unlock()
				return
			}
			s.mu.Unlock()

			time.Sleep(s.reconnectInterval)

			conn, err := net.DialTimeout("unix", s.socketPath, 2*time.Second)
			if err != nil {
				continue
			}

			// The path is the same one the monitor used to own, and a
			// concurrent session's proxy may have taken it since. Whatever
			// answered has to prove it is a monitor before it gets a request.
			dec := json.NewDecoder(conn)
			enc := json.NewEncoder(conn)
			if herr := askHandshake(conn, dec, enc, AskRoleProxy, true, s.helloTimeout); herr != nil {
				_ = conn.Close()
				s.refuseSocketOwner(herr)
				return
			}

			// Reconnected — update connection state
			s.mu.Lock()
			if s.closed {
				s.mu.Unlock()
				_ = conn.Close()
				return
			}
			s.conn = conn
			s.mu.Unlock()

			s.clientMu.Lock()
			s.encoder = enc
			s.clientMu.Unlock()

			s.decoder = dec

			break // Back to reading responses
		}
	}
}

// refuseSocketOwner puts a client-mode server into the dead state after a
// re-dial reached something other than a monitor, and tells the user. The
// workload is running by now, so a plain Warn would be diverted to the log
// file; only an Alert reaches the terminal.
func (s *AskServer) refuseSocketOwner(cause error) {
	deadErr := askDeadError(s.socketPath, cause)
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if !closed {
		notice.Alert("%v", deadErr)
	}
	// Published after the Alert, so whoever observes the dead state through
	// HandshakeError also finds the notice already written.
	s.mu.Lock()
	s.handshakeErr = deadErr
	s.mu.Unlock()
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
	if s.mode == AskModeClient {
		return s.askClient(ctx, req)
	}
	return s.askServer(ctx, req)
}

// askClient sends a request to the monitor over the client connection and waits for a response.
func (s *AskServer) askClient(ctx context.Context, req *AskRequest) (AskResponse, error) {
	s.mu.Lock()
	if s.closed || s.conn == nil {
		s.mu.Unlock()
		return AskResponse{}, ErrNoMonitor
	}
	s.mu.Unlock()

	ch := make(chan AskResponse, 1)
	s.pendingMu.Lock()
	s.pending[req.ID] = ch
	s.pendingMu.Unlock()

	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, req.ID)
		s.pendingMu.Unlock()
	}()

	s.clientMu.Lock()
	err := s.encoder.Encode(req)
	s.clientMu.Unlock()
	if err != nil {
		return AskResponse{}, ErrNoMonitor
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return AskResponse{}, ErrNoMonitor
		}
		return resp, nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return AskResponse{}, ErrTimeout
		}
		return AskResponse{}, ctx.Err()
	}
}

// askServer is the original Ask logic for server mode.
func (s *AskServer) askServer(ctx context.Context, req *AskRequest) (AskResponse, error) {
	s.monitorsMu.RLock()
	monitors := make([]*monitorConn, len(s.monitors))
	copy(monitors, s.monitors)
	s.monitorsMu.RUnlock()

	if len(monitors) == 0 {
		return AskResponse{}, ErrNoMonitor
	}

	ch := make(chan AskResponse, 1)
	s.pendingMu.Lock()
	s.pending[req.ID] = ch
	s.pendingMu.Unlock()

	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, req.ID)
		s.pendingMu.Unlock()
	}()

	for _, monitor := range monitors {
		if err := monitor.encoder.Encode(req); err != nil {
			continue
		}
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return AskResponse{}, ErrNoMonitor
		}
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

	if s.mode == AskModeClient {
		if s.conn != nil {
			_ = s.conn.Close()
		}
		// Don't remove socket — monitor owns it
		return nil
	}

	// Server mode cleanup
	s.monitorsMu.Lock()
	for _, monitor := range s.monitors {
		_ = monitor.conn.Close()
	}
	s.monitors = nil
	s.monitorsMu.Unlock()

	// Cancel all pending requests so blocked Ask() calls return immediately
	s.pendingMu.Lock()
	for id, ch := range s.pending {
		close(ch)
		delete(s.pending, id)
	}
	s.pendingMu.Unlock()

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
	logger       askLogger
}

// NewAskQueue creates a new ask queue.
func NewAskQueue(server *AskServer, engine *FilterEngine, timeout time.Duration, logger askLogger) *AskQueue {
	return &AskQueue{
		server:       server,
		filterEngine: engine,
		timeout:      timeout,
		logger:       logger,
	}
}

// RequestApproval blocks until user approves or denies the request.
// Returns ErrNoMonitor if no monitor is connected.
// Returns ErrTimeout if the request times out.
func (q *AskQueue) RequestApproval(req *AskRequest) (FilterAction, error) {
	req.Timeout = int(q.timeout.Seconds())

	ctx, cancel := context.WithTimeout(context.Background(), q.timeout)
	defer cancel()

	resp, err := q.server.Ask(ctx, req)
	if err != nil {
		// Return specific error for logging
		return FilterActionBlock, err
	}

	// Only the two decisions are acted on. Treating "anything but block" as
	// allow let a mistyped verb, a case variant or a newer monitor's vocabulary
	// approve a request nobody approved, and Remember would then have cached
	// that non-decision for the rest of the session.
	switch resp.Action {
	case FilterActionAllow, FilterActionBlock:
	default:
		q.logger.Printf("ask mode: monitor answered request %s with unrecognised action %q - blocked", req.ID, string(resp.Action))
		return FilterActionBlock, nil
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
