package proxy

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// startTunnelProxy starts a running proxy with MITM disabled and the given
// filter configuration, and returns it.
func startTunnelProxy(t *testing.T, filter *FilterConfig) *Server {
	t.Helper()

	cfg := NewConfig(shortTempDir(t), 0)
	cfg.MITM = false
	cfg.Filter = filter

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })
	return srv
}

// sendCONNECT opens a raw connection to the proxy, sends a CONNECT for target
// and returns the proxy's answer to it. Raw TCP rather than an http.Client:
// the response to the CONNECT itself is the thing under test, and a client
// transport turns it into an opaque dial error.
func sendCONNECT(t *testing.T, proxyAddr, target string) *http.Response {
	t.Helper()

	conn, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		t.Fatalf("write CONNECT %s: %v", target, err)
	}

	req, err := http.NewRequest(http.MethodConnect, "https://"+target, nil)
	if err != nil {
		t.Fatalf("build CONNECT request: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("read CONNECT response for %s: %v", target, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestServerCONNECT_NoMITM_HostRuleBlocks is the regression test for HTTPS
// escaping the filter entirely in transparent mode. goproxy routes CONNECT to
// handleHttps, which never reaches the OnRequest DoFunc that evaluates filter
// rules, so before the fix every one of these tunnels was established despite
// a block rule naming the host - while the startup warning said filtering was
// "limited to host-level matching".
//
// The aliased spellings are here because the CONNECT path must canonicalize
// the host the same way the plain-HTTP path does.
func TestServerCONNECT_NoMITM_HostRuleBlocks(t *testing.T) {
	srv := startTunnelProxy(t, &FilterConfig{
		DefaultAction: FilterActionAllow,
		Rules: []FilterRule{{
			Pattern: "blocked.example.com",
			Action:  FilterActionBlock,
			Scope:   FilterScopeHost,
			Type:    PatternTypeExact,
			Reason:  "blocked by policy",
		}},
	})

	for _, target := range []string{
		"blocked.example.com:443",
		"BLOCKED.EXAMPLE.COM:443",
		"blocked.example.com.:443",
	} {
		t.Run(target, func(t *testing.T) {
			resp := sendCONNECT(t, srv.Addr(), target)
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("CONNECT %s: got status %d, want %d", target, resp.StatusCode, http.StatusForbidden)
			}
			if got := resp.Header.Get("X-Blocked-By"); got != "devsandbox" {
				t.Errorf("CONNECT %s: X-Blocked-By = %q, want %q", target, got, "devsandbox")
			}
		})
	}
}

// TestServerCONNECT_NoMITM_DefaultBlock asserts whitelist behavior on the
// CONNECT path: an unlisted host is refused by the default action, a listed
// one is tunneled.
func TestServerCONNECT_NoMITM_DefaultBlock(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	upstreamAddr := strings.TrimPrefix(upstream.URL, "https://")
	upstreamHost, _, err := net.SplitHostPort(upstreamAddr)
	if err != nil {
		t.Fatalf("split upstream address %q: %v", upstreamAddr, err)
	}

	srv := startTunnelProxy(t, &FilterConfig{
		DefaultAction: FilterActionBlock,
		Rules: []FilterRule{{
			Pattern: upstreamHost,
			Action:  FilterActionAllow,
			Scope:   FilterScopeHost,
			Type:    PatternTypeExact,
		}},
	})

	if resp := sendCONNECT(t, srv.Addr(), "unlisted.example.com:443"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("unlisted host: got status %d, want %d", resp.StatusCode, http.StatusForbidden)
	}

	if resp := sendCONNECT(t, srv.Addr(), upstreamAddr); resp.StatusCode != http.StatusOK {
		t.Errorf("listed host: got status %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

// TestNewServer_NoMITM_RefusesUnenforceableScopes covers the other half of the
// fix: a CONNECT carries only host:port, so path- and url-scoped rules cannot
// be evaluated with MITM off. Starting anyway would enforce less than the
// config file states, so construction fails naming the rule.
func TestNewServer_NoMITM_RefusesUnenforceableScopes(t *testing.T) {
	tests := []struct {
		name    string
		mitm    bool
		rules   []FilterRule
		wantErr bool
	}{
		{
			name:    "path scope without MITM",
			rules:   []FilterRule{{Pattern: "/admin/*", Action: FilterActionBlock, Scope: FilterScopePath}},
			wantErr: true,
		},
		{
			name:    "url scope without MITM",
			rules:   []FilterRule{{Pattern: "https://example.com/*", Action: FilterActionBlock, Scope: FilterScopeURL}},
			wantErr: true,
		},
		{
			name:    "host scope after an enforceable rule",
			rules:   []FilterRule{{Pattern: "*.example.com", Action: FilterActionAllow, Scope: FilterScopeHost}},
			wantErr: false,
		},
		{
			name:    "unset scope defaults to host",
			rules:   []FilterRule{{Pattern: "*.example.com", Action: FilterActionAllow}},
			wantErr: false,
		},
		{
			name:    "path scope with MITM enabled",
			mitm:    true,
			rules:   []FilterRule{{Pattern: "/admin/*", Action: FilterActionBlock, Scope: FilterScopePath}},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := NewConfig(shortTempDir(t), 0)
			cfg.MITM = tt.mitm
			cfg.Filter = &FilterConfig{DefaultAction: FilterActionAllow, Rules: tt.rules}

			srv, err := NewServer(cfg)
			if srv != nil {
				t.Cleanup(func() { _ = srv.Stop() })
			}

			if !tt.wantErr {
				if err != nil {
					t.Fatalf("NewServer failed: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatal("NewServer succeeded, want an unenforceable-scope error")
			}
			if srv != nil {
				t.Error("NewServer returned a server alongside the error")
			}
			if !errors.Is(err, ErrUnenforceableFilterScope) {
				t.Errorf("error %v does not wrap ErrUnenforceableFilterScope", err)
			}
			if !strings.Contains(err.Error(), tt.rules[0].Pattern) {
				t.Errorf("error %v does not name the offending rule %q", err, tt.rules[0].Pattern)
			}
		})
	}
}

// TestServerCONNECT_NoMITM_Logged asserts each CONNECT reaches the request log
// with its decision. Before the fix the only trace of an HTTPS tunnel was the
// proxy.mitm.bypass audit event, deduped to one per host per session, so no
// per-connection record existed at all.
func TestServerCONNECT_NoMITM_Logged(t *testing.T) {
	srv := startTunnelProxy(t, &FilterConfig{
		DefaultAction: FilterActionAllow,
		Rules: []FilterRule{{
			Pattern: "blocked.example.com",
			Action:  FilterActionBlock,
			Scope:   FilterScopeHost,
			Type:    PatternTypeExact,
			Reason:  "blocked by policy",
		}},
	})

	if resp := sendCONNECT(t, srv.Addr(), "blocked.example.com:443"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("got status %d, want %d", resp.StatusCode, http.StatusForbidden)
	}

	entries := readRequestLog(t, srv)
	if len(entries) != 1 {
		t.Fatalf("got %d log entries, want 1: %+v", len(entries), entries)
	}
	entry := entries[0]
	if entry.Method != http.MethodConnect {
		t.Errorf("method = %q, want %q", entry.Method, http.MethodConnect)
	}
	if entry.URL != "https://blocked.example.com:443" {
		t.Errorf("url = %q, want %q", entry.URL, "https://blocked.example.com:443")
	}
	if entry.FilterAction != string(FilterActionBlock) {
		t.Errorf("filter_action = %q, want %q", entry.FilterAction, FilterActionBlock)
	}
	if entry.FilterReason != "blocked by policy" {
		t.Errorf("filter_reason = %q, want %q", entry.FilterReason, "blocked by policy")
	}
	if entry.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want %d", entry.StatusCode, http.StatusForbidden)
	}
}

// readRequestLog returns every entry written to the server's active request log.
func readRequestLog(t *testing.T, srv *Server) []RequestLog {
	t.Helper()

	data, err := os.ReadFile(srv.reqLogger.writer.CurrentPath())
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}

	var entries []RequestLog
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var entry RequestLog
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("parse log line %q: %v", line, err)
		}
		entries = append(entries, entry)
	}
	return entries
}

// askMonitor connects to the server's ask socket and answers every request
// with the given action, asking for it to be remembered.
func askMonitor(t *testing.T, sandboxBase string, action FilterAction) net.Conn {
	t.Helper()

	conn, err := net.Dial("unix", AskSocketPath(sandboxBase))
	if err != nil {
		t.Fatalf("monitor dial failed: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)
	go func() {
		for {
			var req AskRequest
			if err := dec.Decode(&req); err != nil {
				return
			}
			_ = enc.Encode(AskResponse{ID: req.ID, Action: action, Remember: true})
		}
	}()
	return conn
}

func waitForMonitor(t *testing.T, srv *Server, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for srv.askServer.HasMonitor() != want {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for HasMonitor() == %v", want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// newAskTunnelServer builds (but does not start) a transparent-mode proxy in
// ask mode. The CONNECT decision is driven directly rather than over a socket:
// the ask exchange is the thing under test, and the tunnel itself adds nothing.
func newAskTunnelServer(t *testing.T) (*Server, string) {
	t.Helper()

	base := shortTempDir(t)
	cfg := NewConfig(base, 0)
	cfg.MITM = false
	cfg.Filter = &FilterConfig{
		DefaultAction:  FilterActionAsk,
		AskTimeout:     5,
		CacheDecisions: new(true),
	}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })
	return srv, base
}

// TestServerCONNECT_NoMITM_AskAllowedAndCached asserts ask mode reaches the
// CONNECT path and that a remembered decision is honored there afterwards -
// the same treatment the plain-HTTP path gets.
func TestServerCONNECT_NoMITM_AskAllowedAndCached(t *testing.T) {
	srv, base := newAskTunnelServer(t)

	conn := askMonitor(t, base, FilterActionAllow)
	waitForMonitor(t, srv, true)

	if resp := srv.filterConnect("ask.example.com:443"); resp != nil {
		t.Fatalf("CONNECT refused despite the monitor allowing it: %d", resp.StatusCode)
	}

	// With the monitor gone, only the cached decision can allow the tunnel:
	// an unanswerable ask is refused (ErrNoMonitor).
	_ = conn.Close()
	waitForMonitor(t, srv, false)

	if resp := srv.filterConnect("ASK.example.com.:443"); resp != nil {
		t.Errorf("cached decision not honored on the CONNECT path: got status %d", resp.StatusCode)
	}

	entries := readRequestLog(t, srv)
	if len(entries) != 2 {
		t.Fatalf("got %d log entries, want 2: %+v", len(entries), entries)
	}
	if entries[0].FilterReason != "allowed by user decision" {
		t.Errorf("first entry reason = %q, want %q", entries[0].FilterReason, "allowed by user decision")
	}
	if entries[1].FilterReason != "cached decision" {
		t.Errorf("second entry reason = %q, want %q", entries[1].FilterReason, "cached decision")
	}
}

// TestServerCONNECT_NoMITM_AskBlocked is the deny half: a user refusal must
// refuse the tunnel rather than being recorded and ignored.
func TestServerCONNECT_NoMITM_AskBlocked(t *testing.T) {
	srv, base := newAskTunnelServer(t)

	askMonitor(t, base, FilterActionBlock)
	waitForMonitor(t, srv, true)

	resp := srv.filterConnect("ask.example.com:443")
	if resp == nil {
		t.Fatal("CONNECT allowed despite the monitor blocking it")
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("got status %d, want %d", resp.StatusCode, http.StatusForbidden)
	}

	entries := readRequestLog(t, srv)
	if len(entries) != 1 {
		t.Fatalf("got %d log entries, want 1: %+v", len(entries), entries)
	}
	if entries[0].FilterAction != string(FilterActionBlock) {
		t.Errorf("filter_action = %q, want %q", entries[0].FilterAction, FilterActionBlock)
	}
	if entries[0].FilterReason != "blocked by user decision" {
		t.Errorf("filter_reason = %q, want %q", entries[0].FilterReason, "blocked by user decision")
	}
}

// TestServerCONNECT_NoMITM_NoFilter asserts the tunnel still works, and is
// still logged, when no filter is configured at all.
func TestServerCONNECT_NoMITM_NoFilter(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	srv := startTunnelProxy(t, nil)
	upstreamAddr := strings.TrimPrefix(upstream.URL, "https://")

	if resp := sendCONNECT(t, srv.Addr(), upstreamAddr); resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d, want %d", resp.StatusCode, http.StatusOK)
	}

	entries := readRequestLog(t, srv)
	if len(entries) != 1 {
		t.Fatalf("got %d log entries, want 1: %+v", len(entries), entries)
	}
	if entries[0].Method != http.MethodConnect {
		t.Errorf("method = %q, want %q", entries[0].Method, http.MethodConnect)
	}
	if entries[0].FilterAction != "" {
		t.Errorf("filter_action = %q, want it unset when no filter is configured", entries[0].FilterAction)
	}
}

// TestServerHTTP_BlockedRequestIsLoggedOnce covers the plain-HTTP half of
// applyFilterDecision, which had no end-to-end test at all, and pins the
// double-count it hid: goproxy calls filterResponse even for the response a
// request hook short-circuited with, so the OnResponse hook ran for a blocked
// request too and wrote its entry a second time. The CONNECT path logs once, so
// the two disagreed about how many requests the session made.
func TestServerHTTP_BlockedRequestIsLoggedOnce(t *testing.T) {
	cfg := NewConfig(shortTempDir(t), 0)
	cfg.MITM = false
	cfg.Filter = &FilterConfig{
		DefaultAction: FilterActionAllow,
		Rules: []FilterRule{
			{Pattern: "blocked.example.com", Type: PatternTypeExact, Scope: FilterScopeHost, Action: FilterActionBlock},
		},
	}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })

	proxyURL, err := url.Parse("http://" + srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   10 * time.Second,
	}
	resp, err := client.Get("http://blocked.example.com/secret")
	if err != nil {
		t.Fatalf("request through proxy failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}

	if got := srv.RequestCount(); got != 1 {
		t.Errorf("RequestCount() = %d, want 1", got)
	}
	entries := readRequestLog(t, srv)
	if len(entries) != 1 {
		t.Fatalf("got %d log entries, want 1: %+v", len(entries), entries)
	}
	if entries[0].FilterAction != string(FilterActionBlock) {
		t.Errorf("filter_action = %q, want %q", entries[0].FilterAction, FilterActionBlock)
	}
}

// TestNewServer_AskServerNotBuiltWhenFilteringIsOff pins the other half of the
// ask wiring. usesAskAction is about the rules; whether any of them is consulted
// is IsEnabled's question. A config with an ask rule and no default_action does
// no filtering at all, so an ask server built for it is a socket and an accept
// goroutine nothing can reach - and a failure creating it would abort a launch
// that was never going to ask anything.
func TestNewServer_AskServerNotBuiltWhenFilteringIsOff(t *testing.T) {
	cfg := NewConfig(shortTempDir(t), 0)
	cfg.MITM = false
	cfg.Filter = &FilterConfig{
		Rules: []FilterRule{{Pattern: "*.example.com", Action: FilterActionAsk}},
	}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })

	if srv.AskServer() != nil {
		t.Error("ask server built for a filter config that does no filtering")
	}
}
