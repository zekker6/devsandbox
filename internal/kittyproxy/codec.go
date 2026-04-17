package kittyproxy

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

const (
	// DCS introducer prefix kitty uses for remote-control commands.
	dcsIntroducer = "\x1bP@kitty-cmd"
	// String terminator (ST). Two bytes: ESC \\.
	st = "\x1b\\"
	// Cap on a single frame payload to avoid unbounded memory use from a malformed peer.
	maxFrameBytes = 4 << 20 // 4 MiB
)

// ReadFrame reads one kitty DCS-framed payload from r.
// Bytes that appear before a DCS introducer are silently discarded (some clients
// emit stray bytes between frames). Returns io.EOF cleanly at end of stream.
// Returns an error if a frame is started but not terminated, or exceeds maxFrameBytes.
func ReadFrame(r *bufio.Reader) ([]byte, error) {
	if err := skipUntilIntroducer(r); err != nil {
		return nil, err
	}
	return readUntilTerminator(r)
}

// WriteFrame writes payload as a single DCS-framed kitty command to w.
func WriteFrame(w io.Writer, payload []byte) error {
	if _, err := io.WriteString(w, dcsIntroducer); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	if _, err := io.WriteString(w, st); err != nil {
		return err
	}
	return nil
}

// skipUntilIntroducer advances r until the byte sequence dcsIntroducer is consumed.
// Returns io.EOF if the stream ends cleanly before any introducer is seen.
func skipUntilIntroducer(r *bufio.Reader) error {
	intro := []byte(dcsIntroducer)
	matched := 0
	sawAny := false
	for {
		b, err := r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) && !sawAny {
				return io.EOF
			}
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("unexpected EOF before DCS introducer")
			}
			return err
		}
		sawAny = true
		if b == intro[matched] {
			matched++
			if matched == len(intro) {
				return nil
			}
			continue
		}
		// Reset, but if this byte starts a new partial match, count it.
		if b == intro[0] {
			matched = 1
		} else {
			matched = 0
		}
	}
}

// readUntilTerminator returns bytes up to (but not including) the ST sequence.
func readUntilTerminator(r *bufio.Reader) ([]byte, error) {
	out := make([]byte, 0, 256)
	for {
		if len(out) > maxFrameBytes {
			return nil, fmt.Errorf("kitty frame exceeds %d bytes", maxFrameBytes)
		}
		b, err := r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("read frame body: %w", io.ErrUnexpectedEOF)
			}
			return nil, fmt.Errorf("read frame body: %w", err)
		}
		if b == 0x1b {
			next, err := r.ReadByte()
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil, fmt.Errorf("read frame terminator: %w", io.ErrUnexpectedEOF)
				}
				return nil, fmt.Errorf("read frame terminator: %w", err)
			}
			if next == '\\' {
				return out, nil
			}
			out = append(out, b, next)
			continue
		}
		out = append(out, b)
	}
}
