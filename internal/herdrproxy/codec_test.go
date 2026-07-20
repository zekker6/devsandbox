package herdrproxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadLineRoundTrip(t *testing.T) {
	// The exact wire shape captured from herdr v0.7.4.
	const wire = `{"id":"cli:pane:list","method":"pane.list","params":{}}`

	r := bufio.NewReader(strings.NewReader(wire + "\n"))
	got, err := ReadLine(r)
	if err != nil {
		t.Fatalf("ReadLine returned error: %v", err)
	}
	if string(got) != wire {
		t.Errorf("ReadLine = %q, want %q", got, wire)
	}

	if _, err := ReadLine(r); !errors.Is(err, io.EOF) {
		t.Errorf("second ReadLine error = %v, want io.EOF", err)
	}
}

func TestReadLineMultipleRequestsOneConnection(t *testing.T) {
	// herdr multiplexes: one connection carries many requests.
	lines := []string{
		`{"id":"a","method":"tab.create","params":{"cwd":"/w"}}`,
		`{"id":"b","method":"pane.send_input","params":{"pane_id":"p1"}}`,
		`{"id":"c","method":"tab.close","params":{"tab_id":"t1"}}`,
	}
	r := bufio.NewReader(strings.NewReader(strings.Join(lines, "\n") + "\n"))

	for i, want := range lines {
		got, err := ReadLine(r)
		if err != nil {
			t.Fatalf("ReadLine %d returned error: %v", i, err)
		}
		if string(got) != want {
			t.Errorf("ReadLine %d = %q, want %q", i, got, want)
		}
	}
	if _, err := ReadLine(r); !errors.Is(err, io.EOF) {
		t.Errorf("trailing ReadLine error = %v, want io.EOF", err)
	}
}

func TestReadLineSkipsBlankLines(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("\n\n" + `{"id":"a","method":"ping","params":{}}` + "\n\n"))
	got, err := ReadLine(r)
	if err != nil {
		t.Fatalf("ReadLine returned error: %v", err)
	}
	if !strings.Contains(string(got), `"ping"`) {
		t.Errorf("ReadLine = %q, want the ping request", got)
	}
	if _, err := ReadLine(r); !errors.Is(err, io.EOF) {
		t.Errorf("ReadLine after blanks error = %v, want io.EOF", err)
	}
}

func TestReadLineFinalLineWithoutNewline(t *testing.T) {
	// The herdr CLI writes one request and closes without a trailing newline.
	const wire = `{"id":"a","method":"ping","params":{}}`
	r := bufio.NewReader(strings.NewReader(wire))

	got, err := ReadLine(r)
	if err != nil {
		t.Fatalf("ReadLine returned error: %v", err)
	}
	if string(got) != wire {
		t.Errorf("ReadLine = %q, want %q", got, wire)
	}
}

func TestReadLinePreservesEscapedNewlinesInsideStrings(t *testing.T) {
	// A JSON string may contain \n as two escape characters; that is not a
	// line terminator and must not split the message.
	wire := `{"id":"a","method":"pane.send_input","params":{"text":"line1\nline2"}}`
	r := bufio.NewReader(strings.NewReader(wire + "\n"))

	got, err := ReadLine(r)
	if err != nil {
		t.Fatalf("ReadLine returned error: %v", err)
	}
	if string(got) != wire {
		t.Fatalf("ReadLine = %q, want %q", got, wire)
	}

	req, err := parseRequest(got)
	if err != nil {
		t.Fatalf("parseRequest returned error: %v", err)
	}
	var params struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if params.Text != "line1\nline2" {
		t.Errorf("text = %q, want %q", params.Text, "line1\nline2")
	}
}

func TestReadLineRejectsOversizedLine(t *testing.T) {
	huge := `{"id":"a","method":"ping","params":{"pad":"` + strings.Repeat("x", maxLineBytes+16) + `"}}`
	r := bufio.NewReader(strings.NewReader(huge + "\n"))

	if _, err := ReadLine(r); err == nil {
		t.Fatal("ReadLine accepted a line over maxLineBytes, want an error")
	} else if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("ReadLine error = %v, want it to mention the size limit", err)
	}
}

func TestWriteLine(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteLine(&buf, []byte(`{"id":"a"}`)); err != nil {
		t.Fatalf("WriteLine returned error: %v", err)
	}
	if got := buf.String(); got != `{"id":"a"}`+"\n" {
		t.Errorf("WriteLine wrote %q, want a single newline-terminated line", got)
	}
}

func TestParseRequest(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
		method  string
	}{
		{
			name:   "captured tab.create",
			raw:    `{"id":"cli:tab:create","method":"tab.create","params":{"workspace_id":"ws1","cwd":"/work","focus":true,"label":"rev"}}`,
			method: "tab.create",
		},
		{
			name:   "captured pane.send_input",
			raw:    `{"id":"cli:request","method":"pane.send_input","params":{"pane_id":"pane7","text":"sh /x/l","keys":["Enter"]}}`,
			method: "pane.send_input",
		},
		{name: "malformed json", raw: `{"id":`, wantErr: true},
		{name: "missing method", raw: `{"id":"a","params":{}}`, wantErr: true},
		{name: "empty object", raw: `{}`, wantErr: true},
		{name: "not an object", raw: `["a"]`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := parseRequest([]byte(tt.raw))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseRequest(%q) = nil error, want an error", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRequest returned error: %v", err)
			}
			if req.Method != tt.method {
				t.Errorf("method = %q, want %q", req.Method, tt.method)
			}
		})
	}
}

func TestDenyResponseIsValidAndCorrelated(t *testing.T) {
	raw := denyResponse("cli:pane:read", "method not permitted")

	var resp errorResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("denyResponse produced unparseable JSON %q: %v", raw, err)
	}
	if resp.ID != "cli:pane:read" {
		t.Errorf("id = %q, want the request id echoed back", resp.ID)
	}
	if !strings.Contains(resp.Error.Message, "method not permitted") {
		t.Errorf("message = %q, want it to carry the deny reason", resp.Error.Message)
	}
	if resp.Error.Code == "" {
		t.Error("error code is empty, want a non-empty code so clients can branch on it")
	}
	if bytes.Contains(raw, []byte("\n")) {
		t.Error("denyResponse contains a newline, which would corrupt the NDJSON framing")
	}
}

func TestResponseID(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "success response", raw: `{"id":"a","result":{"tab":{"tab_id":"t1"}}}`, want: "a"},
		{name: "error response", raw: `{"id":"b","error":{"code":"x","message":"y"}}`, want: "b"},
		{name: "no id", raw: `{"result":{}}`, want: ""},
		{name: "malformed", raw: `{"id":`, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := responseID([]byte(tt.raw)); got != tt.want {
				t.Errorf("responseID(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
