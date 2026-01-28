package proxy

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"

	"github.com/elazarl/goproxy"
)

type Server struct {
	config   *Config
	ca       *CA
	proxy    *goproxy.ProxyHttpServer
	listener net.Listener
	server   *http.Server
	logger   *log.Logger
	wg       sync.WaitGroup
	mu       sync.Mutex
	running  bool
}

func NewServer(cfg *Config) (*Server, error) {
	ca, err := LoadOrCreateCA(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to load/create CA: %w", err)
	}

	proxy := goproxy.NewProxyHttpServer()

	var logger *log.Logger
	if cfg.LogEnabled {
		logger = log.New(os.Stderr, "[proxy] ", log.LstdFlags)
		proxy.Verbose = true
	}

	s := &Server{
		config: cfg,
		ca:     ca,
		proxy:  proxy,
		logger: logger,
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
	if s.logger == nil {
		return
	}

	s.proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		s.logger.Printf(">> %s %s", req.Method, req.URL)
		return req, nil
	})

	s.proxy.OnResponse().DoFunc(func(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
		if resp != nil {
			s.logger.Printf("<< %d %s", resp.StatusCode, ctx.Req.URL)
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

	addr := fmt.Sprintf("127.0.0.1:%d", s.config.Port)

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	s.listener = listener
	s.server = &http.Server{
		Handler: s.proxy,
	}

	s.running = true

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			if s.logger != nil {
				s.logger.Printf("server error: %v", err)
			}
		}
	}()

	if s.logger != nil {
		s.logger.Printf("Proxy server started on %s", addr)
	}

	return nil
}

func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	s.running = false

	if s.listener != nil {
		s.listener.Close()
	}

	s.wg.Wait()

	if s.logger != nil {
		s.logger.Printf("Proxy server stopped")
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
