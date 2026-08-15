package proxy

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/elazarl/goproxy"

	"devsandbox/internal/logging"
	"devsandbox/internal/notice"
)

const (
	ProxyLogPrefix = "proxy"
	// ProxyLogSuffix names the *active* file, which holds plain text.
	// ProxyLogArchiveSuffix names a rotation, which is gzipped. Spelling the
	// active file `.log.gz` and leaving ArchiveSuffix empty is what this
	// replaces: nothing ever compressed, every file was plain text under a
	// `.gz` name, and `devsandbox logs internal --type proxy` opened it with a
	// gzip reader and reported no entries.
	ProxyLogSuffix        = ".log"
	ProxyLogArchiveSuffix = ".log.gz"
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
	redactionEngine     *RedactionEngine
	askServer           *AskServer
	askQueue            *AskQueue
	credentialInjectors []CredentialInjector
	dispatcher          *logging.Dispatcher
	bypassedHosts       sync.Map // dedupe for proxy.mitm.bypass events (host → struct{}{})
	wg                  sync.WaitGroup
	mu                  sync.Mutex
	running             bool
	requestID           uint64
	debug               bool // DEVSANDBOX_DEBUG: log per-request lifecycle to the internal proxy log
}

// RequestCount returns the count of non-skipped requests handled by this
// server's request logger. Used by session.end audit events.
func (s *Server) RequestCount() int64 {
	if s == nil || s.reqLogger == nil {
		return 0
	}
	return s.reqLogger.RequestCount()
}

// validateCredentialRedactionConflicts checks that no credential injector's resolved
// value would be caught by a redaction rule. This prevents the confusing situation
// where credential injection adds a token and redaction immediately blocks/modifies it.
func validateCredentialRedactionConflicts(injectors []CredentialInjector, engine *RedactionEngine) error {
	if engine == nil || !engine.IsEnabled() || len(injectors) == 0 {
		return nil
	}

	var errs []string
	for _, injector := range injectors {
		value := injector.ResolvedValue()
		if value == "" {
			continue
		}

		matches := engine.MatchesValue(value)
		if len(matches) > 0 {
			errs = append(errs, fmt.Sprintf(
				"credential injector %q conflicts with redaction rules %v: "+
					"injected credential value matches redaction rules that would block or modify it; "+
					"either remove the conflicting redaction rules or disable credential injection for this domain",
				injector.Name(), matches,
			))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// ErrUnenforceableFilterScope reports a filter rule whose scope cannot be
// evaluated in the configured mode. Callers match on it with errors.Is to tell
// a refused configuration apart from a runtime failure.
var ErrUnenforceableFilterScope = errors.New("filter rule scope cannot be enforced")

// validateFilterScopes refuses a configuration whose filter rules the proxy
// cannot honor. With MITM off, HTTPS never becomes an HTTP request the proxy
// can inspect: all it sees is CONNECT host:port. Host-scoped rules are
// enforceable there, path- and url-scoped ones are not. Starting anyway would
// enforce less than the config file says, so the launch is refused naming the
// rule instead.
func validateFilterScopes(cfg *Config) error {
	if cfg.MITM || cfg.Filter == nil || !cfg.Filter.IsEnabled() {
		return nil
	}
	for i, rule := range cfg.Filter.Rules {
		scope := rule.GetScope()
		if scope == FilterScopeHost {
			continue
		}
		return fmt.Errorf(
			"%w: rule %d (pattern %q, scope %q): an HTTPS CONNECT carries only host:port, "+
				"so %s-scoped rules cannot be evaluated while MITM is disabled; "+
				"use scope = \"host\" or enable MITM",
			ErrUnenforceableFilterScope, i+1, rule.Pattern, scope, scope)
	}
	return nil
}

func NewServer(cfg *Config) (*Server, error) {
	// Refuse an unenforceable configuration before creating anything.
	if err := validateFilterScopes(cfg); err != nil {
		return nil, err
	}

	var ca *CA
	if cfg.MITM {
		var err error
		ca, err = LoadOrCreateCA(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to load/create CA: %w", err)
		}
	}

	proxy := goproxy.NewProxyHttpServer()

	// Create rotating file writer for goproxy's internal logs (warnings, errors)
	proxyLogger, err := NewRotatingFileWriter(RotatingFileWriterConfig{
		Dir:           cfg.InternalLogDir,
		Prefix:        ProxyLogPrefix,
		Suffix:        ProxyLogSuffix,
		ArchiveSuffix: ProxyLogArchiveSuffix,
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

	// Create log-skip engine before the request logger so it can be threaded in.
	// nil cfg.LogSkip → empty engine (no-op skip).
	skipEngine, err := NewLogSkipEngine(cfg.LogSkip)
	if err != nil {
		_ = proxyLogger.Close()
		if ownsDispatcher && dispatcher != nil {
			_ = dispatcher.Close()
		}
		return nil, fmt.Errorf("failed to create log-skip engine: %w", err)
	}

	// Create request logger for persisting full request/response data
	reqLogger, err := NewRequestLogger(cfg.LogDir, dispatcher, ownsDispatcher, skipEngine,
		WithMaxBodyLogBytes(cfg.GetMaxLogBodyBytes()))
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

	// Create redaction engine if configured
	var redactionEngine *RedactionEngine
	if cfg.Redaction != nil && cfg.Redaction.IsEnabled() {
		redactionEngine, err = NewRedactionEngine(cfg.Redaction, cfg.ProjectDir)
		if err != nil {
			_ = proxyLogger.Close()
			_ = reqLogger.Close()
			return nil, fmt.Errorf("failed to create redaction engine: %w", err)
		}
	}

	// Cross-validate: credential injectors must not conflict with redaction rules
	if err := validateCredentialRedactionConflicts(cfg.CredentialInjectors, redactionEngine); err != nil {
		_ = proxyLogger.Close()
		_ = reqLogger.Close()
		return nil, fmt.Errorf("credential/redaction conflict: %w", err)
	}

	// Set up ask mode if anything can reach it - the default action, or any
	// single rule.
	//
	// Keying this on the default action alone left `action = "ask"` on a rule
	// with any other default with no queue to ask: the request was allowed
	// through unprompted while its log entry recorded FilterAction="ask",
	// so the audit trail asserted a user had approved a request nobody saw.
	// IsEnabled() is part of the condition because it is part of filterEngine's:
	// a config with an ask rule but no default_action does no filtering at all,
	// so an ask server built for it is a socket and an accept goroutine nothing
	// can reach - and a failure creating it would abort a launch that was never
	// going to ask anything.
	var askServer *AskServer
	var askQueue *AskQueue
	if cfg.Filter.IsEnabled() && cfg.Filter.usesAskAction() {
		askServer, err = NewAskServer(cfg.SandboxBase)
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
		redactionEngine:     redactionEngine,
		askServer:           askServer,
		askQueue:            askQueue,
		credentialInjectors: cfg.CredentialInjectors,
		dispatcher:          dispatcher,
		debug:               os.Getenv("DEVSANDBOX_DEBUG") != "",
	}

	s.setupMITM()
	s.setupLogging()

	return s, nil
}

func (s *Server) setupMITM() {
	if !s.config.MITM {
		// Transparent mode: the tunnel is not intercepted, so this hook is the
		// only place a filter rule can be applied to HTTPS. goproxy routes
		// CONNECT to handleHttps, which never reaches the OnRequest DoFunc
		// installed by setupLogging.
		s.proxy.OnRequest().HandleConnectFunc(func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
			if resp := s.filterConnect(host); resp != nil {
				// goproxy writes ctx.Resp to the client before closing the
				// tunnel, so the sandbox sees the 403 and its reason rather
				// than a bare connection reset.
				ctx.Resp = resp
				return goproxy.RejectConnect, host
			}

			// Dedupe: emit one proxy.mitm.bypass per host per session.
			// host arrives as "example.com:443"; NormalizeHost strips the port
			// and canonicalizes the name so aliased spellings share one entry.
			cleanHost := NormalizeHost(host)
			if _, loaded := s.bypassedHosts.LoadOrStore(cleanHost, struct{}{}); !loaded {
				s.emitMITMBypass(cleanHost)
			}
			return goproxy.OkConnect, host
		})
		return
	}

	// MITM mode: intercept all HTTPS connections. In debug mode, log each
	// CONNECT so we can confirm a host is actually being intercepted (vs.
	// tunneled or never reaching the proxy at all).
	if s.debug {
		s.proxy.OnRequest().HandleConnectFunc(func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
			s.debugf("CONNECT %s -> MITM", host)
			return goproxy.MitmConnect, host
		})
	} else {
		s.proxy.OnRequest().HandleConnect(goproxy.AlwaysMitm)
	}

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

// connectRequest builds the request that represents a CONNECT tunnel. It
// carries everything a CONNECT actually states - the method and host:port -
// and nothing it does not, so filtering, audit events, ask mode and the
// request log all describe the tunnel rather than a request invented for them.
func connectRequest(hostport string) *http.Request {
	return &http.Request{
		Method: http.MethodConnect,
		Host:   hostport,
		URL:    &url.URL{Scheme: "https", Host: hostport},
	}
}

// filterConnect evaluates a CONNECT target against the filter and records one
// request-log entry for it. It returns the response to send before closing the
// tunnel when the request is refused, and nil when it may proceed.
//
// Every CONNECT is logged, not only refused ones: without an entry here the
// only trace of an HTTPS tunnel is the proxy.mitm.bypass audit event, which is
// deduped to one per host per session and so cannot show a per-connection
// decision.
func (s *Server) filterConnect(hostport string) *http.Response {
	req := connectRequest(hostport)
	entry, _ := s.reqLogger.LogRequest(req)

	var resp *http.Response
	if s.filterEngine != nil && s.filterEngine.IsEnabled() {
		resp = s.applyFilterDecision(req, entry, nil, s.filterEngine.MatchHost(hostport))
	}

	if entry != nil {
		if resp != nil {
			s.reqLogger.LogResponse(entry, resp, entry.Timestamp)
		}
		_ = s.reqLogger.Log(entry)
	}
	return resp
}

// applyFilterDecision records a filter decision on the log entry and resolves
// ask mode, returning the response to send when the request must be refused
// and nil when it may proceed. Shared by the plain-HTTP handler and the
// transparent-mode CONNECT handler so the two cannot drift in how a decision
// is applied; they differ only in how the decision is reached.
func (s *Server) applyFilterDecision(req *http.Request, entry *RequestLog, reqBody []byte, decision FilterDecision) *http.Response {
	s.emitFilterDecision(req, decision)

	if entry != nil {
		entry.FilterAction = string(decision.Action)
		entry.FilterReason = decision.Reason
	}

	switch decision.Action {
	case FilterActionBlock:
		return BlockResponse(req, decision.Reason)

	case FilterActionAsk:
		if s.askQueue == nil {
			// No ask queue configured, use default action
			if s.filterEngine.Config().GetDefaultAction() == FilterActionBlock {
				return BlockResponse(req, "ask mode not available, using default block")
			}
			return nil
		}
		if s.handleAskMode(req, entry, reqBody) == FilterActionBlock {
			if entry != nil {
				entry.FilterAction = string(FilterActionBlock)
				entry.FilterReason = "blocked by user decision"
			}
			return BlockResponse(req, "blocked by user")
		}
		// User allowed - continue with request
		if entry != nil {
			entry.FilterAction = string(FilterActionAllow)
			entry.FilterReason = "allowed by user decision"
		}
	}

	return nil
}

// finalizeEntry detaches the request-log entry from the goproxy context after a
// request hook has already written it.
//
// goproxy's handleHttp calls filterResponse unconditionally, including for the
// response a request hook short-circuited with (http.go), so the OnResponse
// hook below runs for a blocked request too. Without this the entry is written
// a second time and counted twice by RequestCount, while the CONNECT path -
// which never reaches the response hook - counts once, leaving the two paths
// disagreeing about how many requests the session made.
func finalizeEntry(ctx *goproxy.ProxyCtx) {
	if ctx != nil {
		ctx.UserData = nil
	}
}

func (s *Server) setupLogging() {
	// Set up request logging and filtering
	s.proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		// Capture request for logging (before credential injection to avoid logging tokens)
		entry, reqBody := s.reqLogger.LogRequest(req)
		ctx.UserData = entry

		// goproxy can dispatch HTTPS requests with a nil URL when its own
		// url.Parse fallback fails. Every downstream step here (credential
		// injection, filtering, redaction, ask mode) dereferences req.URL,
		// so we reject with 403 rather than panic.
		// https://github.com/elazarl/goproxy/blob/v1.8.3/https.go#L272-L274
		if req.URL == nil {
			resp := BlockResponse(req, "malformed request: missing URL")
			if entry != nil {
				entry.FilterAction = string(FilterActionBlock)
				entry.FilterReason = "malformed request: missing URL"
				s.reqLogger.LogResponse(entry, resp, entry.Timestamp)
				_ = s.reqLogger.Log(entry)
			}
			finalizeEntry(ctx)
			return nil, resp
		}

		if s.debug {
			s.debugf("request: %s %s%s", req.Method, req.URL.Host, req.URL.Path)
		}

		// Inject credentials for matching domains
		for _, injector := range s.credentialInjectors {
			if injector.Match(req) {
				if injector.Inject(req) {
					host := req.URL.Host
					if host == "" {
						host = req.Host
					}
					s.emitCredentialInjected(host, injector.Name(), injector.Header())
				}
				break // first match wins
			}
		}

		// Apply filter rules if configured
		if s.filterEngine != nil && s.filterEngine.IsEnabled() {
			if resp := s.applyFilterDecision(req, entry, reqBody, s.filterEngine.Match(req)); resp != nil {
				if entry != nil {
					s.reqLogger.LogResponse(entry, resp, entry.Timestamp)
					_ = s.reqLogger.Log(entry)
				}
				finalizeEntry(ctx)
				return nil, resp
			}
		}

		// Redaction scan (after filter allows the request)
		// Re-read the body from req: what LogRequest captured is a bounded
		// prefix, and credential injection runs in between. The read is bounded
		// in bytes and time, and a body it cannot take whole is blocked - a scan
		// of part of a body proves nothing about the rest of it.
		if s.redactionEngine != nil && s.redactionEngine.IsEnabled() {
			scanBody := reqBody
			if req.Body != nil {
				freshBody, err := s.redactionEngine.ReadScanBody(req)
				if err != nil {
					resp := BlockResponse(req, redactionReadBlockReason(err))
					if entry != nil {
						entry.RedactionAction = "block"
						entry.Error = "redaction body read error: " + err.Error()
						s.reqLogger.LogResponse(entry, resp, entry.Timestamp)
						_ = s.reqLogger.Log(entry)
					}
					finalizeEntry(ctx)
					return nil, resp
				}
				scanBody = freshBody
			}

			result := s.redactionEngine.Scan(req, scanBody)
			if result.Matched {
				s.emitRedactionApplied(req, result)
				// Build rule names list for logging and response
				ruleNames := make([]string, len(result.Matches))
				for i, m := range result.Matches {
					ruleNames[i] = m.RuleName
				}

				// Log match details
				if entry != nil {
					entry.RedactionAction = string(result.Action)
					entry.RedactionMatches = ruleNames
				}

				switch result.Action {
				case RedactionActionBlock:
					resp := BlockResponse(req, "request blocked: secret pattern detected in outgoing request")
					if entry != nil {
						// Update entry with redacted values so secrets don't persist in logs
						if result.Body != nil {
							entry.RequestBody, entry.RequestBodyTruncated = s.reqLogger.CaptureBody(result.Body)
						}
						if result.URL != "" {
							entry.URL = result.URL
						}
						if result.Headers != nil {
							entry.RequestHeaders, entry.RequestHeadersTruncated = captureHeaders(result.Headers)
						}
						s.reqLogger.LogResponse(entry, resp, entry.Timestamp)
						_ = s.reqLogger.Log(entry)
					}
					finalizeEntry(ctx)
					return nil, resp

				case RedactionActionRedact:
					// Replace request body with redacted content
					req.Body = io.NopCloser(bytes.NewReader(result.Body))
					req.ContentLength = int64(len(result.Body))
					req.Header.Del("Content-Length") // Let Go derive from ContentLength

					// Replace URL — if parse fails, the redaction placeholder
					// created an invalid URL. Block rather than leak the secret.
					parsedURL, parseErr := url.Parse(result.URL)
					if parseErr != nil {
						resp := BlockResponse(req, fmt.Sprintf(
							"secret detected but redacted URL is unparseable: %v", parseErr))
						if entry != nil {
							entry.RedactionAction = string(RedactionActionBlock)
							// Use redacted values so secrets don't persist in logs
							if result.Body != nil {
								entry.RequestBody, entry.RequestBodyTruncated = s.reqLogger.CaptureBody(result.Body)
							}
							if result.URL != "" {
								entry.URL = result.URL
							}
							if result.Headers != nil {
								entry.RequestHeaders, entry.RequestHeadersTruncated = captureHeaders(result.Headers)
							}
							s.reqLogger.LogResponse(entry, resp, entry.Timestamp)
							_ = s.reqLogger.Log(entry)
						}
						finalizeEntry(ctx)
						return nil, resp
					}
					req.URL = parsedURL

					// Replace headers
					for k, vals := range result.Headers {
						req.Header[k] = vals
					}

					// Update log entry with redacted values
					if entry != nil {
						if result.Body != nil {
							entry.RequestBody, entry.RequestBodyTruncated = s.reqLogger.CaptureBody(result.Body)
						}
						if result.URL != "" {
							entry.URL = result.URL
						}
						if result.Headers != nil {
							entry.RequestHeaders, entry.RequestHeadersTruncated = captureHeaders(result.Headers)
						}
					}

				case RedactionActionLog:
					// Log-only: request proceeds unmodified, warning is in the log entry
				}
			}
		}

		return req, nil
	})

	s.proxy.OnResponse().DoFunc(func(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
		entry, ok := ctx.UserData.(*RequestLog)
		if !ok {
			return resp
		}

		// Read debug fields before wrapping. The body is captured asynchronously
		// (see LogResponseStreaming) so the response headers reach the client
		// immediately; time_to_headers is how long upstream took to return them.
		var ct string
		var streaming bool
		if s.debug && resp != nil {
			ct = resp.Header.Get("Content-Type")
			streaming = isStreamingResponse(resp)
		}

		// Records status/headers now and streams the body through to the client
		// while capturing a bounded prefix; the log entry is written when the
		// body closes. Never buffers the body before relaying headers.
		s.reqLogger.LogResponseStreaming(entry, resp, entry.Timestamp)

		if s.debug {
			s.debugf("response: %s %s status=%d content-type=%q streaming=%t time_to_headers=%s (body streamed, not buffered)",
				entry.Method, stripQuery(entry.URL), entry.StatusCode, ct, streaming,
				entry.Duration.Round(time.Millisecond))
		}

		return resp
	})
}

// debugf writes a per-request lifecycle line to the internal proxy log when
// DEVSANDBOX_DEBUG is set. Lines carry timestamps (log.LstdFlags) so request
// and response events can be correlated and timed. View with
// `devsandbox logs internal --type proxy`.
func (s *Server) debugf(format string, args ...any) {
	if s.proxy != nil && s.proxy.Logger != nil {
		s.proxy.Logger.Printf("DEBUG "+format, args...)
	}
}

// stripQuery returns the URL with any query string removed. Debug logs must not
// carry query parameters, which can contain tokens.
func stripQuery(rawURL string) string {
	if i := strings.IndexByte(rawURL, '?'); i >= 0 {
		return rawURL[:i]
	}
	return rawURL
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
		Handler:           s.proxy,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	s.running = true

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			notice.Error("proxy server error: %v", err)
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
		// The authority the request is actually sent to, not the Host header
		// the sandbox wrote: the prompt must name the destination the user is
		// being asked to approve. See RequestHost.
		Host: RequestHost(req),
		Path: req.URL.Path,
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
