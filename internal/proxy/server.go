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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/elazarl/goproxy"

	"devsandbox/internal/logging"
)

const (
	ProxyLogPrefix = "proxy"
	ProxyLogSuffix = ".log.gz"
)

type Server struct {
	config              *Config
	ca                  *CA
	proxy               *goproxy.ProxyHttpServer
	listener            net.Listener
	server              *http.Server
	reqLogger           *RequestLogger
	proxyLogger         *RotatingFileWriter
	filterEngine        *FilterEngine
	askServer           *AskServer
	askQueue            *AskQueue
	credentialInjectors []CredentialInjector
	wg                  sync.WaitGroup
	mu                  sync.Mutex
	running             bool
	requestID           uint64
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

	// Use shared dispatcher if provided, otherwise create one from config.
	// Track ownership so we know who is responsible for closing it.
	dispatcher := cfg.Dispatcher
	ownsDispatcher := false
	if dispatcher == nil && len(cfg.LogReceivers) > 0 {
		dispatcher, err = logging.NewDispatcherFromConfig(cfg.LogReceivers, cfg.LogAttributes, cfg.InternalLogDir)
		if err != nil {
			_ = proxyLogger.Close()
			return nil, fmt.Errorf("failed to create log dispatcher: %w", err)
		}
		ownsDispatcher = true
	}

	// Create request logger for persisting full request/response data
	reqLogger, err := NewRequestLogger(cfg.LogDir, dispatcher, ownsDispatcher)
	if err != nil {
		_ = proxyLogger.Close()
		if ownsDispatcher && dispatcher != nil {
			_ = dispatcher.Close()
		}
		return nil, fmt.Errorf("failed to create request logger: %w", err)
	}

	// Create filter engine if configured
	var filterEngine *FilterEngine
	if cfg.Filter != nil && cfg.Filter.IsEnabled() {
		filterEngine, err = NewFilterEngine(cfg.Filter)
		if err != nil {
			_ = proxyLogger.Close()
			_ = reqLogger.Close()
			return nil, fmt.Errorf("failed to create filter engine: %w", err)
		}
	}

	// Set up ask mode if configured (default_action = ask)
	var askServer *AskServer
	var askQueue *AskQueue
	if cfg.Filter != nil && cfg.Filter.DefaultAction == FilterActionAsk {
		askServer, err = NewAskServer(cfg.LogDir)
		if err != nil {
			_ = proxyLogger.Close()
			_ = reqLogger.Close()
			return nil, fmt.Errorf("failed to create ask server: %w", err)
		}

		timeout := time.Duration(cfg.Filter.GetAskTimeout()) * time.Second
		askQueue = NewAskQueue(askServer, filterEngine, timeout)
	}

	s := &Server{
		config:              cfg,
		ca:                  ca,
		proxy:               proxy,
		reqLogger:           reqLogger,
		proxyLogger:         proxyLogger,
		filterEngine:        filterEngine,
		askServer:           askServer,
		askQueue:            askQueue,
		credentialInjectors: cfg.CredentialInjectors,
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
	// Set up request logging and filtering
	s.proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		// Capture request for logging (before credential injection to avoid logging tokens)
		entry, reqBody := s.reqLogger.LogRequest(req)
		ctx.UserData = entry

		// Inject credentials for matching domains
		for _, injector := range s.credentialInjectors {
			if injector.Match(req) {
				injector.Inject(req)
				break // first match wins
			}
		}

		if s.filterEngine == nil || !s.filterEngine.IsEnabled() {
			return req, nil
		}
		decision := s.filterEngine.Match(req)

		// Log the filter decision
		if entry != nil {
			entry.FilterAction = string(decision.Action)
			entry.FilterReason = decision.Reason
		}

		switch decision.Action {
		case FilterActionBlock:
			// Block the request
			resp := BlockResponse(req, decision.Reason)
			// Log as blocked
			if entry != nil {
				s.reqLogger.LogResponse(entry, resp, entry.Timestamp)
				_ = s.reqLogger.Log(entry)
			}
			return nil, resp

		case FilterActionAsk:
			// Handle ask mode
			if s.askQueue != nil {
				action := s.handleAskMode(req, entry, reqBody)
				if action == FilterActionBlock {
					resp := BlockResponse(req, "blocked by user")
					if entry != nil {
						entry.FilterAction = string(FilterActionBlock)
						entry.FilterReason = "blocked by user decision"
						s.reqLogger.LogResponse(entry, resp, entry.Timestamp)
						_ = s.reqLogger.Log(entry)
					}
					return nil, resp
				}
				// User allowed - continue with request
				if entry != nil {
					entry.FilterAction = string(FilterActionAllow)
					entry.FilterReason = "allowed by user decision"
				}
			} else {
				// No ask queue configured, use default action
				defaultAction := s.filterEngine.Config().GetDefaultAction()
				if defaultAction == FilterActionBlock {
					resp := BlockResponse(req, "ask mode not available, using default block")
					if entry != nil {
						s.reqLogger.LogResponse(entry, resp, entry.Timestamp)
						_ = s.reqLogger.Log(entry)
					}
					return nil, resp
				}
			}
		}

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

	bindAddr := s.config.GetBindAddress()
	for i := 0; i < MaxPortRetries; i++ {
		if port > 65535 {
			break
		}
		addr := fmt.Sprintf("%s:%d", bindAddr, port)
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
		if err := s.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			log.Printf("proxy server error: %v", err)
		}
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

	// Close ask mode resources
	if s.askQueue != nil {
		_ = s.askQueue.Close()
	}
	if s.askServer != nil {
		_ = s.askServer.Close()
	}

	// Close loggers to flush remaining data
	if s.reqLogger != nil {
		_ = s.reqLogger.Close()
	}
	if s.proxyLogger != nil {
		_ = s.proxyLogger.Close()
	}

	return nil
}

// handleAskMode prompts the user for a decision on the request.
// Returns the filter action and logs unanswered requests to internal logs.
func (s *Server) handleAskMode(req *http.Request, entry *RequestLog, reqBody []byte) FilterAction {
	// Generate unique request ID
	id := atomic.AddUint64(&s.requestID, 1)

	// Build ask request
	askReq := &AskRequest{
		ID:     fmt.Sprintf("%d", id),
		Method: req.Method,
		URL:    req.URL.String(),
		Host:   req.Host,
		Path:   req.URL.Path,
	}

	// Add selected headers
	if req.Header != nil {
		askReq.Headers = make(map[string]string)
		for _, h := range []string{"Content-Type", "Authorization", "User-Agent"} {
			if v := req.Header.Get(h); v != "" {
				// Redact sensitive headers
				if h == "Authorization" {
					askReq.Headers[h] = "[REDACTED]"
				} else {
					askReq.Headers[h] = v
				}
			}
		}
	}

	// Add body preview (first 200 bytes)
	if len(reqBody) > 0 {
		preview := string(reqBody)
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		askReq.Body = preview
	}

	// Request approval from user
	action, err := s.askQueue.RequestApproval(askReq)
	if err != nil {
		// Log unanswered request to internal logs
		var reason string
		if errors.Is(err, ErrNoMonitor) {
			reason = "no monitor connected"
		} else if errors.Is(err, ErrTimeout) {
			reason = "request timed out (30s) waiting for user response"
		} else {
			reason = err.Error()
		}

		// Log to internal proxy logs
		s.proxy.Logger.Printf("UNANSWERED: %s %s - %s (rejected)", req.Method, req.URL.String(), reason)

		// Update entry with rejection reason
		if entry != nil {
			entry.FilterReason = fmt.Sprintf("unanswered: %s", reason)
		}

		return FilterActionBlock
	}

	return action
}

// AskServer returns the ask server if ask mode is enabled.
func (s *Server) AskServer() *AskServer {
	return s.askServer
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

func (s *Server) Config() *Config {
	return s.config
}

func (s *Server) CA() *CA {
	return s.ca
}

func (s *Server) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}
