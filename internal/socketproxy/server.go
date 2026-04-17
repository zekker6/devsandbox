// Package socketproxy provides a reusable UNIX domain socket server that
// wraps the listener lifecycle shared by filtering proxies (docker, kitty,
// and future tool proxies). It owns socket creation, permissions, the accept
// loop, and graceful drain on Stop; callers supply a Handler that implements
// the protocol-specific framing and filtering.
package socketproxy

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"os"
	"sync"
	"time"
)

// Logger captures the subset of logging the server needs. It matches
// logging.ErrorLogger in the main codebase.
type Logger interface {
	LogErrorf(component, format string, args ...any)
	LogInfof(component, format string, args ...any)
}

// Handler processes a single accepted connection. The server closes the
// connection after Handler returns, so handlers must not close it themselves.
// The provided context is cancelled when Stop is called.
type Handler func(ctx context.Context, conn net.Conn)

// defaultDrainTimeout is how long Stop waits for in-flight handlers before
// giving up and returning an error.
const defaultDrainTimeout = 5 * time.Second

// Server is a UDS listener that dispatches accepted connections to a Handler.
// Exactly one instance manages one socket; it is not reusable after Stop.
type Server struct {
	listenPath string
	mode       fs.FileMode
	component  string
	handler    Handler

	drainTimeout time.Duration

	listener net.Listener
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	logger   Logger
}

// NewServer constructs a Server. listenPath is the UDS path to create, mode
// is the file permission applied to the socket, component is used as the
// logging prefix, and handler is invoked per accepted connection.
func NewServer(listenPath string, mode fs.FileMode, component string, handler Handler) *Server {
	return &Server{
		listenPath:   listenPath,
		mode:         mode,
		component:    component,
		handler:      handler,
		drainTimeout: defaultDrainTimeout,
	}
}

// SetLogger installs the logger used by the accept loop. If nil, errors are
// not logged.
func (s *Server) SetLogger(l Logger) { s.logger = l }

// Start creates the socket, begins listening, and launches the accept loop.
// It returns an error if the socket cannot be created or chmodded.
func (s *Server) Start(ctx context.Context) error {
	_ = os.Remove(s.listenPath)

	l, err := net.Listen("unix", s.listenPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.listenPath, err)
	}
	if err := os.Chmod(s.listenPath, s.mode); err != nil {
		_ = l.Close()
		return fmt.Errorf("chmod %s: %w", s.listenPath, err)
	}
	s.listener = l
	s.ctx, s.cancel = context.WithCancel(ctx)

	s.wg.Add(1)
	go s.acceptLoop()
	return nil
}

// Stop closes the listener, signals handlers via context cancellation, and
// waits up to drainTimeout for in-flight handlers to return.
func (s *Server) Stop() error {
	if s.cancel != nil {
		s.cancel()
	}
	if s.listener != nil {
		_ = s.listener.Close()
	}

	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()

	select {
	case <-done:
		return nil
	case <-time.After(s.drainTimeout):
		return fmt.Errorf("%s: timeout waiting for connections to close", s.component)
	}
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.ctx.Done():
				return
			default:
				s.logErr("accept: %v", err)
				continue
			}
		}
		s.wg.Add(1)
		go s.runHandler(conn)
	}
}

func (s *Server) runHandler(conn net.Conn) {
	defer s.wg.Done()
	defer func() { _ = conn.Close() }()
	s.handler(s.ctx, conn)
}

func (s *Server) logErr(format string, args ...any) {
	if s.logger != nil {
		s.logger.LogErrorf(s.component, format, args...)
	}
}
