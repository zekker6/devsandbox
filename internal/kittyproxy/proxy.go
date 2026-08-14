package kittyproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"

	"devsandbox/internal/socketproxy"
)

// Logger matches dockerproxy.Logger and the existing tools.ErrorLogger.
type Logger = socketproxy.Logger

// Proxy is a filtering proxy for the kitty remote-control socket.
type Proxy struct {
	upstreamPath string

	filter *Filter
	owned  *OwnedSet

	server *socketproxy.Server
	logger Logger
}

// New creates a proxy that listens at listenPath and forwards approved frames
// to upstreamPath. The filter and owned set must be non-nil and shared with the
// caller (the caller may inspect owned for tests).
func New(upstreamPath, listenPath string, filter *Filter, owned *OwnedSet) *Proxy {
	p := &Proxy{
		upstreamPath: upstreamPath,
		filter:       filter,
		owned:        owned,
	}
	p.server = socketproxy.NewServer(listenPath, 0o600, "kitty-proxy", p.handle)
	return p
}

// SetLogger sets the logger used for allow/deny records and errors.
func (p *Proxy) SetLogger(l Logger) {
	p.logger = l
	p.server.SetLogger(l)
}

// Start begins listening. Returns an error if the socket cannot be created.
func (p *Proxy) Start(ctx context.Context) error { return p.server.Start(ctx) }

// Stop gracefully shuts down the proxy.
func (p *Proxy) Stop() error { return p.server.Stop() }

func (p *Proxy) logErr(format string, args ...any) {
	if p.logger != nil {
		p.logger.LogErrorf("kitty-proxy", format, args...)
	}
}
func (p *Proxy) logInf(format string, args ...any) {
	if p.logger != nil {
		p.logger.LogInfof("kitty-proxy", format, args...)
	}
}

func (p *Proxy) handle(_ context.Context, conn net.Conn) {
	r := bufio.NewReader(conn)
	payload, err := ReadFrame(r)
	if err != nil {
		if !errors.Is(err, net.ErrClosed) {
			p.logErr("read frame: %v", err)
		}
		return
	}
	d := p.filter.Decide(payload)
	// %q on the command name: it comes off the socket, so a denial for an
	// unsupported command would otherwise write the sandbox's own newlines and
	// escape sequences into the log file and on to remote receivers.
	if !d.Allow {
		p.logInf("deny cmd=%q reason=%s", d.Cmd, d.Reason)
		_ = WriteFrame(conn, denyResponse(d.Reason))
		return
	}
	p.logInf("allow cmd=%q reason=%s", d.Cmd, d.Reason)
	p.forward(conn, payload, d)
}

func (p *Proxy) forward(client net.Conn, payload []byte, d Decision) {
	upstream, err := net.Dial("unix", p.upstreamPath)
	if err != nil {
		p.logErr("dial upstream: %v", err)
		_ = WriteFrame(client, denyResponse(fmt.Sprintf("upstream unreachable: %v", err)))
		return
	}
	defer func() { _ = upstream.Close() }()

	if err := WriteFrame(upstream, payload); err != nil {
		p.logErr("write upstream: %v", err)
		_ = WriteFrame(client, denyResponse("write upstream failed"))
		return
	}
	upR := bufio.NewReader(upstream)
	resp, err := ReadFrame(upR)
	if err != nil {
		p.logErr("read upstream: %v", err)
		_ = WriteFrame(client, denyResponse("read upstream failed"))
		return
	}

	resp = p.postProcessResponse(d, resp)
	if err := WriteFrame(client, resp); err != nil {
		p.logErr("write client: %v", err)
	}
}

// postProcessResponse runs response-side actions: capture launched window ids
// and filter ls responses to the owned set.
func (p *Proxy) postProcessResponse(d Decision, resp []byte) []byte {
	switch d.Cmd {
	case "launch":
		id, err := ExtractLaunchedWindowID(resp)
		if err != nil {
			// Not fatal - the window exists either way - but it never enters
			// OwnedSet, so every later owned command on it is denied. Silence
			// here left that with no record to explain it.
			p.logErr("track launched window: %v", err)
			return resp
		}
		p.owned.Add(id)
		p.logInf("track owned id=%d", id)
	case "ls":
		// Fail closed: the unfiltered body is every OS window, tab, title, cwd,
		// foreground cmdline and per-window env on the host.
		filtered, err := FilterLsResponse(resp, p.owned)
		if err != nil {
			p.logErr("filter ls response: %v", err)
			return denyResponse("ls response could not be filtered")
		}
		return filtered
	}
	return resp
}

func denyResponse(reason string) []byte {
	body, _ := json.Marshal(kittyResponse{Error: "kitty-proxy: " + reason})
	return body
}
