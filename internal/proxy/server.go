package proxy

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"syscall"

	"github.com/elazarl/goproxy"

	"devsandbox/internal/logging"
)

const (
	ProxyLogPrefix = "proxy"
	ProxyLogSuffix = ".log.gz"
)

type Server struct {
	config      *Config
	ca          *CA
	proxy       *goproxy.ProxyHttpServer
	listener    net.Listener
	server      *http.Server
	reqLogger   *RequestLogger
	proxyLogger *RotatingFileWriter
	wg          sync.WaitGroup
	mu          sync.Mutex
	running     bool
}

func NewServer(cfg *Config) (*Server, error) {
	ca, err := LoadOrCreateCA(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to load/create CA: %w", err)
	}

	proxy := goproxy.NewProxyHttpServer()

	// Create rotating file writer for goproxy's internal logs (warnings, errors)
	proxyLogger, err := NewRotatingFileWriter(RotatingFileWriterConfig{
		Dir:    cfg.InternalLogDir,
		Prefix: ProxyLogPrefix,
		Suffix: ProxyLogSuffix,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create proxy logger: %w", err)
	}

	// Route goproxy's internal warnings to rotating file
	proxy.Logger = log.New(proxyLogger, "", log.LstdFlags)

	// Create log dispatcher for remote forwarding (if configured)
	var dispatcher *logging.Dispatcher
	if len(cfg.LogReceivers) > 0 {
		dispatcher, err = logging.NewDispatcherFromConfig(cfg.LogReceivers, cfg.LogAttributes, cfg.InternalLogDir)
		if err != nil {
			_ = proxyLogger.Close()
			return nil, fmt.Errorf("failed to create log dispatcher: %w", err)
		}
	}

	// Create request logger for persisting full request/response data
	reqLogger, err := NewRequestLogger(cfg.LogDir, dispatcher)
	if err != nil {
		_ = proxyLogger.Close()
		if dispatcher != nil {
			_ = dispatcher.Close()
		}
		return nil, fmt.Errorf("failed to create request logger: %w", err)
	}

	s := &Server{
		config:      cfg,
		ca:          ca,
		proxy:       proxy,
		reqLogger:   reqLogger,
		proxyLogger: proxyLogger,
	}

	s.setupMITM()
	s.setupLogging()

	return s, nil
}

func (s *Server) setupMITM() {
	// Configure MITM for all HTTPS connections
	s.proxy.OnRequest().HandleConnect(goproxy.AlwaysMitm)

	// Set up certificate generation
	goproxy.GoproxyCa = tls.Certificate{
		Certificate: [][]byte{s.ca.Certificate.Raw},
		PrivateKey:  s.ca.PrivateKey,
		Leaf:        s.ca.Certificate,
	}

	// Use our CA for signing
	tlsConfig := goproxy.TLSConfigFromCA(&goproxy.GoproxyCa)
	goproxy.MitmConnect.TLSConfig = func(host string, ctx *goproxy.ProxyCtx) (*tls.Config, error) {
		return tlsConfig(host, ctx)
	}
}

func (s *Server) setupLogging() {
	// Set up request logging for persistence to files
	s.proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		// Capture request for logging
		entry, _ := s.reqLogger.LogRequest(req)
		ctx.UserData = entry

		return req, nil
	})

	s.proxy.OnResponse().DoFunc(func(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
		// Complete and persist log entry
		if entry, ok := ctx.UserData.(*RequestLog); ok {
			s.reqLogger.LogResponse(entry, resp, entry.Timestamp)
			_ = s.reqLogger.Log(entry)
		}

		return resp
	})
}

func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("server already running")
	}

	// Try to listen on the configured port, fall back to next ports if busy
	var listener net.Listener
	var err error
	port := s.config.Port

	for i := 0; i < MaxPortRetries; i++ {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		listener, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}

		// Check if error is "address already in use"
		if !isAddrInUse(err) {
			return fmt.Errorf("failed to listen on %s: %w", addr, err)
		}

		// Try next port
		port++
	}

	if listener == nil {
		return fmt.Errorf("failed to find available port after %d attempts (tried %d-%d)",
			MaxPortRetries, s.config.Port, port-1)
	}

	// Update config with actual port used
	s.config.Port = port

	s.listener = listener
	s.server = &http.Server{
		Handler: s.proxy,
	}

	s.running = true

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		// Server errors are silently ignored - logs are written to files
		_ = s.server.Serve(listener)
	}()

	return nil
}

// isAddrInUse checks if the error is "address already in use"
func isAddrInUse(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		var sysErr *os.SyscallError
		if errors.As(opErr.Err, &sysErr) {
			return errors.Is(sysErr.Err, syscall.EADDRINUSE)
		}
	}
	return false
}

func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	s.running = false

	if s.listener != nil {
		_ = s.listener.Close()
	}

	s.wg.Wait()

	// Close loggers to flush remaining data
	if s.reqLogger != nil {
		_ = s.reqLogger.Close()
	}
	if s.proxyLogger != nil {
		_ = s.proxyLogger.Close()
	}

	return nil
}

func (s *Server) Addr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *Server) Port() int {
	return s.config.Port
}

func (s *Server) CA() *CA {
	return s.ca
}

func (s *Server) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}
