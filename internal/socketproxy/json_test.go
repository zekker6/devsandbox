package socketproxy

import (
	"encoding/json"
	"strings"
	"testing"
)

type testPayload struct {
	Args []string        `json:"args"`
	Type string          `json:"type"`
	Skip string          `json:"-"`
	Raw  json.RawMessage `json:"raw,omitempty"`
	Bare string
}

func TestStrictUnmarshal_AcceptsDeclaredFields(t *testing.T) {
	var p testPayload
	if err := StrictUnmarshal([]byte(`{"args":["a","b"],"type":"overlay","Bare":"x"}`), &p); err != nil {
		t.Fatalf("StrictUnmarshal rejected a well-formed payload: %v", err)
	}
	if len(p.Args) != 2 || p.Type != "overlay" || p.Bare != "x" {
		t.Fatalf("decoded payload = %+v", p)
	}
}

func TestStrictUnmarshal_EmptyPayloadIsEmptyObject(t *testing.T) {
	var p testPayload
	if err := StrictUnmarshal(nil, &p); err != nil {
		t.Fatalf("empty payload rejected: %v", err)
	}
	if p.Args != nil || p.Type != "" {
		t.Fatalf("empty payload decoded to %+v", p)
	}
}

// TestStrictUnmarshal_RejectsCaseVariantDuplicateKey is the reproducer for the
// parser differential: encoding/json matches field names case-insensitively and
// takes the last match, so the struct saw the second `ARGS` while the untouched
// bytes forwarded to the host carry a first `args` its own parser reads.
// Validating one list and shipping the other made the argv allowlist decide
// nothing.
func TestStrictUnmarshal_RejectsCaseVariantDuplicateKey(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"upper-case duplicate", `{"args":["/bin/sh","-c","x"],"ARGS":["/usr/bin/revdiff"],"type":"overlay"}`},
		{"mixed-case duplicate", `{"Args":["/usr/bin/revdiff"],"args":["/bin/sh"]}`},
		{"case variant alone", `{"ARGS":["/usr/bin/revdiff"]}`},
		{"exact duplicate", `{"args":["a"],"args":["b"]}`},
		{"case-variant scalar", `{"TYPE":"overlay"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p testPayload
			err := StrictUnmarshal([]byte(tc.raw), &p)
			if err == nil {
				t.Fatalf("accepted %s, decoded to %+v", tc.raw, p)
			}
			if !strings.Contains(err.Error(), "unknown field") && !strings.Contains(err.Error(), "duplicate field") {
				t.Errorf("error does not name the offending key: %v", err)
			}
		})
	}
}

func TestStrictUnmarshal_RejectsUnknownField(t *testing.T) {
	var p testPayload
	if err := StrictUnmarshal([]byte(`{"args":["a"],"watcher":["/tmp/x.py"]}`), &p); err == nil {
		t.Fatal("accepted an undeclared field")
	}
}

// A `json:"-"` field is not addressable from the wire, so naming it must be an
// unknown field rather than a way to reach an unmodelled name.
func TestStrictUnmarshal_RejectsDashTaggedField(t *testing.T) {
	var p testPayload
	if err := StrictUnmarshal([]byte(`{"Skip":"x"}`), &p); err == nil {
		t.Fatal("accepted a field tagged json:\"-\"")
	}
}

// kitty's json.loads rejects trailing data, but the same relaxed-parser class as
// the duplicate keys above: the decoder stops at the first value and reports no
// error, so the filter would decide on one command and forward two.
func TestStrictUnmarshal_RejectsTrailingData(t *testing.T) {
	var p testPayload
	if err := StrictUnmarshal([]byte(`{"type":"overlay"} {"type":"window"}`), &p); err == nil {
		t.Fatalf("accepted trailing data, decoded to %+v", p)
	}
}

func TestStrictUnmarshal_RejectsNonObject(t *testing.T) {
	var p testPayload
	if err := StrictUnmarshal([]byte(`["args"]`), &p); err == nil {
		t.Fatal("accepted a JSON array as an object payload")
	}
}

func TestStrictUnmarshal_RejectsMalformedJSON(t *testing.T) {
	var p testPayload
	if err := StrictUnmarshal([]byte(`{"args":`), &p); err == nil {
		t.Fatal("accepted truncated JSON")
	}
}

// dec.More() reports false when the next byte is a stray closing delimiter, so
// `{...}}` read as a clean parse here while json.loads and serde both reject the
// whole line. The proxy being the more permissive of the two parsers is the
// shape every bug this file guards against has had.
func TestStrictUnmarshal_RejectsStrayClosingDelimiter(t *testing.T) {
	for _, raw := range []string{`{"type":"overlay"}}`, `{"type":"overlay"}]`} {
		var p testPayload
		if err := StrictUnmarshal([]byte(raw), &p); err == nil {
			t.Errorf("accepted %s, decoded to %+v", raw, p)
		}
	}
}

// A top-level null decodes into a struct pointer as a silent no-op, so the
// caller would vet a zero value it believes came off the wire.
func TestStrictUnmarshal_RejectsNull(t *testing.T) {
	var p testPayload
	if err := StrictUnmarshal([]byte(`null`), &p); err == nil {
		t.Fatal("accepted a top-level null as an object payload")
	}
}

// keysAreExact can only scan a struct's declared names. A non-struct target
// made it a no-op, degrading StrictUnmarshal to DisallowUnknownFields - which
// this package's doc comment explains is not enough.
func TestStrictUnmarshal_RejectsNonStructTarget(t *testing.T) {
	m := map[string]any{}
	if err := StrictUnmarshal([]byte(`{"args":["x"],"ARGS":["y"]}`), &m); err == nil {
		t.Fatal("accepted a non-struct target, leaving the key scan disabled")
	}
}
