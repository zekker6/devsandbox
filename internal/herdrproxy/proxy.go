package herdrproxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"syscall"

	"devsandbox/internal/cmdpattern"
	"devsandbox/internal/socketproxy"
)

// Logger matches socketproxy.Logger and the existing tools.ErrorLogger.
type Logger = socketproxy.Logger

// Proxy is a filtering proxy for the herdr control socket.
//
// Unlike the kitty proxy, which handles exactly one frame per connection, herdr
// multiplexes: a single connection carries many requests, responses may arrive
// out of order, and subscriptions stream indefinitely. The proxy therefore runs
// two concurrent pumps over one upstream connection rather than a
// request/response exchange.
type Proxy struct {
	upstreamPath string

	filter     *Filter
	ownedTabs  *cmdpattern.OwnedSet[string]
	ownedPanes *cmdpattern.OwnedSet[string]

	server *socketproxy.Server
	logger Logger
}

// New creates a proxy listening at listenPath that forwards approved requests
// to upstreamPath. The owned sets are shared with the filter so ownership
// captured from responses immediately governs subsequent requests.
func New(upstreamPath, listenPath string, filter *Filter, ownedTabs, ownedPanes *cmdpattern.OwnedSet[string]) *Proxy {
	p := &Proxy{
		upstreamPath: upstreamPath,
		filter:       filter,
		ownedTabs:    ownedTabs,
		ownedPanes:   ownedPanes,
	}
	p.server = socketproxy.NewServer(listenPath, 0o600, "herdr-proxy", p.handle)
	return p
}

func (p *Proxy) SetLogger(l Logger) {
	p.logger = l
	p.server.SetLogger(l)
}

func (p *Proxy) Start(ctx context.Context) error { return p.server.Start(ctx) }

// Stop shuts the listener down and closes in-flight connections. Relocated
// scripts are owned by the tool layer, which cleans them up after Stop.
func (p *Proxy) Stop() error { return p.server.Stop() }

func (p *Proxy) logErr(format string, args ...any) {
	if p.logger != nil {
		p.logger.LogErrorf("herdr-proxy", format, args...)
	}
}

func (p *Proxy) logInf(format string, args ...any) {
	if p.logger != nil {
		p.logger.LogInfof("herdr-proxy", format, args...)
	}
}

// handle bridges one client connection to a dedicated upstream connection.
func (p *Proxy) handle(ctx context.Context, client net.Conn) {
	upstream, err := net.Dial("unix", p.upstreamPath)
	if err != nil {
		p.logErr("dial upstream: %v", err)
		_ = WriteLine(client, denyResponse("", "upstream unreachable"))
		return
	}
	defer func() { _ = upstream.Close() }()

	// Closing both ends when the context is cancelled unblocks either pump;
	// a blocking read is not interrupted by cancellation alone.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = upstream.Close()
			_ = client.Close()
		case <-stop:
		}
	}()

	corr := newCorrelator()

	// A single writer mutex: the request pump writes denials directly to the
	// client while the response pump relays upstream replies to it.
	var clientMu sync.Mutex
	writeClient := func(line []byte) error {
		clientMu.Lock()
		defer clientMu.Unlock()
		return WriteLine(client, line)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		// Closing upstream ends the response pump once the client is done.
		defer func() { _ = upstream.Close() }()
		p.pumpRequests(client, upstream, corr, writeClient)
	}()
	go func() {
		defer wg.Done()
		// Closing the client ends the request pump if upstream drops first.
		defer func() { _ = client.Close() }()
		p.pumpResponses(upstream, corr, writeClient)
	}()
	wg.Wait()
}

// pumpRequests filters each client request, forwarding what is permitted and
// answering the rest with a denial the client can correlate.
func (p *Proxy) pumpRequests(client net.Conn, upstream net.Conn, corr *correlator, writeClient func([]byte) error) {
	r := bufio.NewReader(client)
	for {
		line, err := ReadLine(r)
		if err != nil {
			if !errors.Is(err, io.EOF) && !isClosed(err) {
				p.logErr("read client: %v", err)
			}
			return
		}

		d := p.filter.Decide(line)
		if !d.Allow {
			p.logInf("deny method=%s reason=%s", d.Method, d.Reason)
			if err := writeClient(denyResponse(d.ID, d.Reason)); err != nil {
				return
			}
			continue
		}

		// Track before forwarding so a fast response cannot arrive before the
		// correlation entry exists.
		if err := corr.Track(d.ID, d.Method); err != nil {
			p.logInf("deny method=%s reason=%s", d.Method, err.Error())
			if werr := writeClient(denyResponse(d.ID, err.Error())); werr != nil {
				return
			}
			continue
		}

		out := line
		if d.Rewritten != nil {
			out = d.Rewritten
		}
		p.logInf("allow method=%s reason=%s", d.Method, d.Reason)
		if err := WriteLine(upstream, out); err != nil {
			if !isClosed(err) {
				p.logErr("write upstream: %v", err)
			}
			return
		}
	}
}

// pumpResponses relays server lines to the client, capturing ownership before
// each line is forwarded.
func (p *Proxy) pumpResponses(upstream net.Conn, corr *correlator, writeClient func([]byte) error) {
	r := bufio.NewReader(upstream)
	for {
		line, err := ReadLine(r)
		if err != nil {
			if !errors.Is(err, io.EOF) && !isClosed(err) {
				p.logErr("read upstream: %v", err)
			}
			return
		}

		// Ownership must be recorded BEFORE the response reaches the client.
		// The launcher issues pane.send_input the moment it sees the
		// tab.create reply, so relaying first would race the request pump and
		// intermittently deny a legitimate call.
		p.captureOwnership(line, corr)

		if err := writeClient(line); err != nil {
			if !isClosed(err) {
				p.logErr("write client: %v", err)
			}
			return
		}
	}
}

// captureOwnership records the tab and pane a tab.create produced.
func (p *Proxy) captureOwnership(line []byte, corr *correlator) {
	id := responseID(line)
	method, ok := corr.Resolve(id)
	if !ok || method != methodTabCreate {
		return
	}

	tabID, paneID, err := ExtractTabCreateIDs(line)
	if err != nil {
		// A failed tab.create is normal; nothing becomes owned.
		p.logInf("tab.create produced no ownership: %v", err)
		return
	}
	if p.ownedTabs != nil {
		p.ownedTabs.Add(tabID)
	}
	if p.ownedPanes != nil {
		p.ownedPanes.Add(paneID)
	}
	p.logInf("track owned tab=%s pane=%s", tabID, paneID)
}

// isClosed reports whether err is the ordinary "peer went away" condition,
// which is expected during shutdown and not worth logging as an error.
func isClosed(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, syscall.EPIPE)
}
