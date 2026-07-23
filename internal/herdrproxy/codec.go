// Package herdrproxy provides a filtering proxy for the herdr terminal-workspace
// control socket.
//
// SECURITY MODEL: the proxy enforces an allowlist of capabilities declared by
// enabled tools. Every method not explicitly permitted is denied. Mutating
// operations are scoped to tabs and panes the sandbox itself created (ownership
// tracking), the programs a launch may run are pinned to resolved absolute
// paths, and launch scripts are validated and relocated to a host-only
// directory before execution. The host herdr socket is NOT bind-mounted into
// the sandbox; only the proxy socket is.
package herdrproxy

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// maxLineBytes caps a single protocol line, bounding memory use from a
// malformed or hostile peer. Matches the kitty proxy's frame cap.
const maxLineBytes = 4 << 20 // 4 MiB

// herdr speaks newline-delimited JSON over a unix socket: one complete JSON
// object per line, in both directions, with no framing envelope. Verified
// against herdr v0.7.4 (protocol 16), e.g.
//
//	{"id":"cli:pane:list","method":"pane.list","params":{}}\n

// request is one client-to-server call. Params stays raw so the filter can
// apply per-method validation without a union type over 86 methods.
type request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// errorBody matches herdr's error envelope.
type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// errorResponse is what the proxy synthesizes for a denied request. Shaping it
// exactly like a server error means the herdr CLI reports the denial normally
// instead of hanging waiting for a reply that never comes.
type errorResponse struct {
	ID    string    `json:"id"`
	Error errorBody `json:"error"`
}

// ReadLine returns one newline-delimited payload from r, without the newline.
//
// Blank lines are skipped rather than surfaced as empty payloads. Returns
// io.EOF cleanly at end of stream; returns an error if a line exceeds
// maxLineBytes or the stream ends mid-line.
func ReadLine(r *bufio.Reader) ([]byte, error) {
	for {
		line, err := readLimited(r)
		if err != nil {
			return nil, err
		}
		line = trimLineEnding(line)
		if len(line) == 0 {
			continue
		}
		return line, nil
	}
}

// readLimited reads one line, refusing to buffer more than maxLineBytes.
func readLimited(r *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		chunk, err := r.ReadSlice('\n')
		out = append(out, chunk...)

		// Check the cap on every path, not just the buffer-full one: the chunk
		// that finally carries the delimiter would otherwise slip past
		// unmeasured, letting a caller exceed the limit by a whole buffer.
		if len(out) > maxLineBytes {
			return nil, fmt.Errorf("herdr line exceeds %d bytes", maxLineBytes)
		}

		switch {
		case err == nil:
			return out, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(trimLineEnding(out)) == 0 {
				// Clean end of stream: nothing buffered but the terminator.
				return nil, io.EOF
			}
			// A final line without a trailing newline is still a complete
			// message; herdr's CLI closes the connection right after writing.
			return out, nil
		default:
			return nil, err
		}
	}
}

func trimLineEnding(b []byte) []byte {
	return []byte(strings.TrimRight(string(b), "\r\n"))
}

// WriteLine writes payload followed by a newline.
func WriteLine(w io.Writer, payload []byte) error {
	if _, err := w.Write(payload); err != nil {
		return err
	}
	_, err := w.Write([]byte{'\n'})
	return err
}

// parseRequest decodes one request line.
func parseRequest(raw []byte) (request, error) {
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		return request{}, fmt.Errorf("parse request: %w", err)
	}
	if req.Method == "" {
		return request{}, fmt.Errorf("request has no method")
	}
	return req, nil
}

// denyResponse builds the error line returned for a denied request. The id is
// echoed so the client correlates the denial with the call it made.
func denyResponse(id, reason string) []byte {
	body, err := json.Marshal(errorResponse{
		ID:    id,
		Error: errorBody{Code: "forbidden", Message: "herdr-proxy: " + reason},
	})
	if err != nil {
		// Cannot happen for these types, but never return a malformed line:
		// the client would block waiting for a parseable reply.
		return []byte(`{"id":"","error":{"code":"forbidden","message":"herdr-proxy: denied"}}`)
	}
	return body
}

// responseID extracts the correlation id from a server line, so the response
// pump can match a reply to the request that produced it. Lines without a
// usable id return "".
func responseID(raw []byte) string {
	var envelope struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ""
	}
	return envelope.ID
}
