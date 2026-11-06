package sockrelay

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// pair wires a guest mux and a host mux over an in-memory pipe. The host dials
// through onOpen; the guest opens streams for the connections it accepts.
func pair(t *testing.T, onOpen OpenFunc) (guest, host *Mux, done func()) {
	t.Helper()
	g, h := net.Pipe()
	guest = New(g, nil)
	host = New(h, onOpen)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = guest.Run(ctx) }()
	go func() { defer wg.Done(); _ = host.Run(ctx) }()
	return guest, host, func() {
		cancel()
		_ = guest.Close()
		_ = host.Close()
		wg.Wait()
	}
}

// echoUpstream returns an OpenFunc whose conn echoes every byte back, and a
// channel that reports the names it was asked for.
func echoUpstream() (OpenFunc, <-chan string) {
	names := make(chan string, 8)
	return func(name string) (net.Conn, error) {
		select {
		case names <- name:
		default: // a test that opens many streams does not read them all
		}
		a, b := net.Pipe()
		go func() {
			defer func() { _ = b.Close() }()
			_, _ = io.Copy(b, b)
		}()
		return a, nil
	}, names
}

func readWithTimeout(t *testing.T, c net.Conn, n int) []byte {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, n)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	return buf
}

func TestMux_GuestStreamReachesHostUpstreamAndBack(t *testing.T) {
	open, names := echoUpstream()
	guest, _, done := pair(t, open)
	defer done()

	local, remote := net.Pipe()
	if err := guest.Open("herdr.sock", remote); err != nil {
		t.Fatalf("open: %v", err)
	}

	go func() { _, _ = local.Write([]byte("ping")) }()
	if got := readWithTimeout(t, local, 4); string(got) != "ping" {
		t.Fatalf("echo = %q, want ping", got)
	}
	select {
	case n := <-names:
		if n != "herdr.sock" {
			t.Fatalf("upstream name = %q", n)
		}
	case <-time.After(time.Second):
		t.Fatal("host never dialed upstream")
	}
}

func TestMux_HalfCloseReachesUpstreamWriteSide(t *testing.T) {
	// A request/response client shuts its write side after the request and
	// waits for the answer, so EOF must cross the relay as a half-close and
	// the response must still come back on the open read side.
	upstreamGotEOF := make(chan struct{})
	open := func(string) (net.Conn, error) {
		// A real upstream is a unix socket, which supports half-close;
		// net.Pipe does not, so the upstream here is the half-close pipe.
		a, b := halfClosePipe()
		go func() {
			defer func() { _ = b.Close() }()
			buf, _ := io.ReadAll(b) // returns on half-close
			close(upstreamGotEOF)
			_, _ = b.Write(append([]byte("reply:"), buf...))
		}()
		return a, nil
	}
	guest, _, done := pair(t, open)
	defer done()

	local, remote := halfClosePipe()
	if err := guest.Open("herdr.sock", remote); err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := local.Write([]byte("req")); err != nil {
		t.Fatal(err)
	}
	if err := local.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-upstreamGotEOF:
	case <-time.After(2 * time.Second):
		t.Fatal("half-close never reached upstream")
	}
	if got := readWithTimeout(t, local, len("reply:req")); string(got) != "reply:req" {
		t.Fatalf("reply = %q", got)
	}
}

func TestMux_DeniedNameClosesGuestConn(t *testing.T) {
	open := func(name string) (net.Conn, error) { return nil, errors.New("denied") }
	guest, _, done := pair(t, open)
	defer done()

	local, remote := net.Pipe()
	if err := guest.Open("evil.sock", remote); err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = local.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := local.Read(make([]byte, 1)); err == nil {
		t.Fatal("denied stream still readable; want it closed")
	}
}

func TestMux_ReadySignalCrossesPipe(t *testing.T) {
	open, _ := echoUpstream()
	guest, host, done := pair(t, open)
	defer done()

	select {
	case <-host.Ready():
		t.Fatal("ready before guest signalled")
	default:
	}
	if err := guest.SendReady(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-host.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("ready never arrived")
	}
}

func TestMux_OversizedFrameTearsDownTheChannel(t *testing.T) {
	// The host reads untrusted bytes from the guest: a frame over the limit
	// must end the whole relay, never be buffered or skipped.
	open, _ := echoUpstream()
	raw, h := net.Pipe()
	host := New(h, open)
	runErr := make(chan error, 1)
	go func() { runErr <- host.Run(context.Background()) }()

	var hdr bytes.Buffer
	hdr.WriteByte(frameData)
	_ = binary.Write(&hdr, binary.BigEndian, uint32(1))
	_ = binary.Write(&hdr, binary.BigEndian, uint32(MaxPayload+1))
	_ = raw.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, _ = raw.Write(hdr.Bytes())

	select {
	case err := <-runErr:
		if err == nil {
			t.Fatal("Run returned nil on an oversized frame")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("host kept running after an oversized frame")
	}
}

func TestMux_OpenOnReservedStreamZeroIsRefused(t *testing.T) {
	// Stream 0 carries READY only; an OPEN there is a protocol violation and
	// ends the relay.
	open, _ := echoUpstream()
	raw, h := net.Pipe()
	host := New(h, open)
	runErr := make(chan error, 1)
	go func() { runErr <- host.Run(context.Background()) }()

	var f bytes.Buffer
	f.WriteByte(frameOpen)
	_ = binary.Write(&f, binary.BigEndian, uint32(0))
	_ = binary.Write(&f, binary.BigEndian, uint32(len("x")))
	f.WriteString("x")
	_ = raw.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, _ = raw.Write(f.Bytes())

	select {
	case err := <-runErr:
		if err == nil {
			t.Fatal("Run returned nil on an OPEN for stream 0")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("host kept running after an OPEN for stream 0")
	}
}

func TestMux_StalledStreamIsClosedWhileOthersKeepFlowing(t *testing.T) {
	// A client that stops reading must cost only its own stream: the write
	// to it times out, that stream is closed, and the read loop goes on
	// serving the rest instead of blocking behind it.
	defer func(d time.Duration) { streamWriteTimeout = d }(streamWriteTimeout)
	streamWriteTimeout = 100 * time.Millisecond

	open, _ := echoUpstream()
	guest, _, done := pair(t, open)
	defer done()

	stalledLocal, stalledRemote := net.Pipe()
	if err := guest.Open("herdr.sock", stalledRemote); err != nil {
		t.Fatal(err)
	}
	// The echo comes back as DATA for the stalled stream; nobody reads
	// stalledLocal, so the guest's write to stalledRemote blocks.
	if _, err := stalledLocal.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}

	time.Sleep(3 * streamWriteTimeout)

	healthyLocal, healthyRemote := net.Pipe()
	if err := guest.Open("herdr.sock", healthyRemote); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = healthyLocal.Write([]byte("pong")) }()
	if got := readWithTimeout(t, healthyLocal, 4); string(got) != "pong" {
		t.Fatalf("healthy stream echo = %q", got)
	}

	_ = stalledLocal.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := stalledLocal.Read(make([]byte, 1)); err == nil {
		t.Fatal("stalled stream is still open; want it closed after the write timeout")
	}
}

func TestMux_StreamCapRefusesExtraOpens(t *testing.T) {
	// Every OPEN costs the host a descriptor, a goroutine and a buffer in the
	// process that also runs the HTTP proxy, so the guest cannot open an
	// unbounded number of them.
	open, _ := echoUpstream()
	guest, _, done := pair(t, open)
	defer done()

	locals := make([]net.Conn, 0, MaxStreams)
	for i := 0; i < MaxStreams; i++ {
		l, r := net.Pipe()
		if err := guest.Open("herdr.sock", r); err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		locals = append(locals, l)
	}
	// Prove the cap is counted on the host: the first MaxStreams streams are
	// live, the next is refused there and closed back here.
	go func() { _, _ = locals[0].Write([]byte("ok")) }()
	if got := readWithTimeout(t, locals[0], 2); string(got) != "ok" {
		t.Fatalf("stream under the cap does not flow: %q", got)
	}
	extraLocal, extraRemote := net.Pipe()
	if err := guest.Open("herdr.sock", extraRemote); err != nil {
		t.Fatal(err)
	}
	_ = extraLocal.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := extraLocal.Read(make([]byte, 1)); err == nil {
		t.Fatal("stream over the cap stayed open")
	}
	for _, l := range locals {
		_ = l.Close()
	}
}

func TestMux_PeerEOFEndsRun(t *testing.T) {
	open, _ := echoUpstream()
	raw, h := net.Pipe()
	host := New(h, open)
	runErr := make(chan error, 1)
	go func() { runErr <- host.Run(context.Background()) }()
	_ = raw.Close()
	select {
	case err := <-runErr:
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("Run on peer EOF = %v, want nil or EOF", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the peer closed")
	}
}

// halfClosePipe is a net.Pipe whose local end supports CloseWrite, so a test
// can drive the request/response shape unix sockets have.
type halfCloser struct {
	net.Conn
	w io.Closer
}

func (h *halfCloser) CloseWrite() error { return h.w.Close() }

func halfClosePipe() (local *halfCloser, remote net.Conn) {
	// Two unidirectional pipes composed into one duplex conn per side.
	ar, aw := io.Pipe() // local -> remote
	br, bw := io.Pipe() // remote -> local
	l := &pipeConn{r: br, w: aw}
	r := &pipeConn{r: ar, w: bw}
	return &halfCloser{Conn: l, w: aw}, r
}

type pipeConn struct {
	r  *io.PipeReader
	w  *io.PipeWriter
	dl time.Time
}

func (p *pipeConn) Read(b []byte) (int, error) {
	if !p.dl.IsZero() {
		timer := time.AfterFunc(time.Until(p.dl), func() { _ = p.r.CloseWithError(errors.New("read deadline")) })
		defer timer.Stop()
	}
	return p.r.Read(b)
}
func (p *pipeConn) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *pipeConn) Close() error {
	_ = p.r.Close()
	return p.w.Close()
}
func (p *pipeConn) CloseWrite() error                 { return p.w.Close() }
func (p *pipeConn) LocalAddr() net.Addr               { return pipeAddr{} }
func (p *pipeConn) RemoteAddr() net.Addr              { return pipeAddr{} }
func (p *pipeConn) SetDeadline(t time.Time) error     { p.dl = t; return nil }
func (p *pipeConn) SetReadDeadline(t time.Time) error { p.dl = t; return nil }
func (p *pipeConn) SetWriteDeadline(time.Time) error  { return nil }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }
