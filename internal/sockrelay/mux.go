// Package sockrelay multiplexes unix-socket connections over a single byte
// stream, so a sandbox whose home is not a native bind mount (Docker on macOS,
// krun) can still reach the filtered proxy sockets devsandbox runs on the host.
//
// The stream is the stdio of a `docker exec -i` the host owns. Nothing listens
// on the host for it, so the only process that can speak the protocol is the
// one the host started. The guest end is untrusted all the same: every frame
// the host reads is bounded, every name the guest opens is answered by the
// host's own allowlist, and any malformed frame ends the whole relay rather
// than being skipped.
//
// Wire format, both directions, big-endian:
//
//	type(1) stream(4) length(4) payload(length)
//
// Stream ids are allocated by the side that opens the stream and partitioned
// by Role so the two sides can never collide: the guest uses odd ids, the host
// even. Stream 0 carries only READY.
package sockrelay

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

const (
	frameOpen  byte = 1 // payload: socket name; opens a stream
	frameData  byte = 2 // payload: bytes for the stream
	frameEOF   byte = 3 // sender writes no more on this stream (half-close)
	frameClose byte = 4 // stream torn down; payload optional reason
	frameReady byte = 5 // stream 0: the guest has created its listeners

	headerLen = 1 + 4 + 4

	// MaxPayload bounds a single frame. The host reads frames from the sandbox,
	// so the bound is what keeps a hostile guest from making it allocate.
	MaxPayload = 64 * 1024
	// MaxNameLen bounds an OPEN payload.
	MaxNameLen = 255
)

// Role selects the stream-id parity a mux allocates from.
type Role int

const (
	// RoleHost dials upstream sockets on the guest's OPEN and allocates even ids.
	RoleHost Role = iota
	// RoleGuest opens a stream per accepted local connection and allocates odd ids.
	RoleGuest
)

// OpenFunc answers a peer's OPEN by connecting to the named upstream. A nil
// OpenFunc denies every OPEN, which is the guest's configuration: only the
// sandbox side ever opens streams.
type OpenFunc func(name string) (net.Conn, error)

// ErrProtocol reports a frame the peer must never have sent. The relay ends on
// it: a peer that violates the framing once is not given a second frame.
var ErrProtocol = errors.New("sockrelay: protocol violation")

// Mux runs one end of the relay.
type Mux struct {
	rw     io.ReadWriteCloser
	role   Role
	onOpen OpenFunc

	writeMu sync.Mutex

	mu      sync.Mutex
	streams map[uint32]*stream
	nextID  uint32
	closed  bool

	readyOnce sync.Once
	ready     chan struct{}
}

type stream struct {
	conn net.Conn
	// remoteEOF is set once the peer sent EOF, localEOF once our pump did; the
	// stream is forgotten when both are set or on CLOSE from either side.
	remoteEOF, localEOF bool
}

// New wraps rw. onOpen may be nil to deny every peer-initiated stream.
func New(rw io.ReadWriteCloser, role Role, onOpen OpenFunc) *Mux {
	m := &Mux{
		rw:      rw,
		role:    role,
		onOpen:  onOpen,
		streams: make(map[uint32]*stream),
		ready:   make(chan struct{}),
	}
	switch role {
	case RoleGuest:
		m.nextID = 1
	default:
		m.nextID = 2
	}
	return m
}

// Ready is closed once the peer sent READY.
func (m *Mux) Ready() <-chan struct{} { return m.ready }

// SendReady tells the peer this side's listeners exist.
func (m *Mux) SendReady() error { return m.writeFrame(frameReady, 0, nil) }

// Open registers conn as a new stream named name and starts relaying it. The
// peer's OpenFunc decides whether the name is served; a refusal closes conn.
func (m *Mux) Open(name string, conn net.Conn) error {
	if len(name) == 0 || len(name) > MaxNameLen {
		_ = conn.Close()
		return fmt.Errorf("sockrelay: invalid stream name %q", name)
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = conn.Close()
		return net.ErrClosed
	}
	id := m.nextID
	m.nextID += 2
	m.streams[id] = &stream{conn: conn}
	m.mu.Unlock()

	if err := m.writeFrame(frameOpen, id, []byte(name)); err != nil {
		m.forget(id)
		_ = conn.Close()
		return err
	}
	go m.pump(id, conn)
	return nil
}

// Run reads frames until the peer closes the stream, ctx ends, or a protocol
// violation occurs. It returns nil on a clean peer EOF.
func (m *Mux) Run(ctx context.Context) error {
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = m.Close()
		case <-stop:
		}
	}()
	defer func() { _ = m.Close() }()

	hdr := make([]byte, headerLen)
	for {
		if _, err := io.ReadFull(m.rw, hdr); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		typ := hdr[0]
		id := binary.BigEndian.Uint32(hdr[1:5])
		n := binary.BigEndian.Uint32(hdr[5:9])
		if n > MaxPayload {
			return fmt.Errorf("%w: frame of %d bytes exceeds %d", ErrProtocol, n, MaxPayload)
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(m.rw, payload); err != nil {
			return fmt.Errorf("sockrelay: read payload: %w", err)
		}
		if err := m.handle(typ, id, payload); err != nil {
			return err
		}
	}
}

// Close ends the relay: the byte stream and every stream conn are closed.
func (m *Mux) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	conns := make([]net.Conn, 0, len(m.streams))
	for _, s := range m.streams {
		conns = append(conns, s.conn)
	}
	m.streams = map[uint32]*stream{}
	m.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	return m.rw.Close()
}

func (m *Mux) handle(typ byte, id uint32, payload []byte) error {
	switch typ {
	case frameReady:
		if id != 0 || len(payload) != 0 {
			return fmt.Errorf("%w: malformed READY", ErrProtocol)
		}
		m.readyOnce.Do(func() { close(m.ready) })
		return nil
	case frameOpen:
		return m.handleOpen(id, payload)
	case frameData, frameEOF, frameClose:
		if id == 0 {
			return fmt.Errorf("%w: stream 0 carries only READY", ErrProtocol)
		}
	default:
		return fmt.Errorf("%w: unknown frame type %d", ErrProtocol, typ)
	}

	s := m.lookup(id)
	if s == nil {
		// A frame for a stream already torn down on this side is a race the
		// protocol allows (both ends may close concurrently), not a violation.
		return nil
	}
	switch typ {
	case frameData:
		if _, err := s.conn.Write(payload); err != nil {
			m.closeStream(id, "write: "+err.Error())
		}
	case frameEOF:
		halfClose(s.conn)
		m.mu.Lock()
		s.remoteEOF = true
		both := s.localEOF
		m.mu.Unlock()
		if both {
			m.forget(id)
			_ = s.conn.Close()
		}
	case frameClose:
		m.forget(id)
		_ = s.conn.Close()
	}
	return nil
}

func (m *Mux) handleOpen(id uint32, payload []byte) error {
	if id == 0 || !m.peerOwns(id) {
		return fmt.Errorf("%w: OPEN on stream %d not allocated by the peer", ErrProtocol, id)
	}
	if len(payload) == 0 || len(payload) > MaxNameLen {
		return fmt.Errorf("%w: OPEN name of %d bytes", ErrProtocol, len(payload))
	}
	if m.lookup(id) != nil {
		return fmt.Errorf("%w: OPEN on stream %d already open", ErrProtocol, id)
	}
	if m.onOpen == nil {
		return m.writeFrame(frameClose, id, []byte("denied"))
	}
	conn, err := m.onOpen(string(payload))
	if err != nil {
		// A refused name is answered, not fatal: the guest asking for a name
		// outside the allowlist costs it that one connection and nothing else.
		return m.writeFrame(frameClose, id, []byte("denied"))
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = conn.Close()
		return net.ErrClosed
	}
	m.streams[id] = &stream{conn: conn}
	m.mu.Unlock()
	go m.pump(id, conn)
	return nil
}

// pump copies conn's reads into DATA frames until EOF or error.
func (m *Mux) pump(id uint32, conn net.Conn) {
	buf := make([]byte, MaxPayload)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			if werr := m.writeFrame(frameData, id, buf[:n]); werr != nil {
				m.forget(id)
				_ = conn.Close()
				return
			}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			_ = m.writeFrame(frameEOF, id, nil)
			m.mu.Lock()
			s, ok := m.streams[id]
			both := ok && s.remoteEOF
			if ok {
				s.localEOF = true
			}
			m.mu.Unlock()
			if both {
				m.forget(id)
				_ = conn.Close()
			}
			return
		}
		m.closeStream(id, err.Error())
		return
	}
}

func (m *Mux) closeStream(id uint32, reason string) {
	s := m.lookup(id)
	if s == nil {
		return
	}
	m.forget(id)
	_ = s.conn.Close()
	_ = m.writeFrame(frameClose, id, []byte(reason))
}

func (m *Mux) peerOwns(id uint32) bool {
	peerOdd := m.role == RoleHost // the guest allocates odd ids
	return (id%2 == 1) == peerOdd
}

func (m *Mux) lookup(id uint32) *stream {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.streams[id]
}

func (m *Mux) forget(id uint32) {
	m.mu.Lock()
	delete(m.streams, id)
	m.mu.Unlock()
}

func (m *Mux) writeFrame(typ byte, id uint32, payload []byte) error {
	if len(payload) > MaxPayload {
		return fmt.Errorf("sockrelay: payload of %d bytes exceeds %d", len(payload), MaxPayload)
	}
	frame := make([]byte, headerLen+len(payload))
	frame[0] = typ
	binary.BigEndian.PutUint32(frame[1:5], id)
	binary.BigEndian.PutUint32(frame[5:9], uint32(len(payload)))
	copy(frame[headerLen:], payload)
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	_, err := m.rw.Write(frame)
	return err
}

// halfClose shuts the write side of conn when it can, which is what a unix
// socket peer expecting request/response waits on. A conn without CloseWrite
// is left open; the peer then sees EOF only at CLOSE.
func halfClose(conn net.Conn) {
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}
