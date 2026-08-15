package socketproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// StrictUnmarshal decodes raw into dst and rejects any field dst does not
// declare, so a parameter the filter does not model cannot ride along
// unchecked into the host process. Both socket proxies forward the original
// request bytes upstream on allow, which makes an undeclared field an
// unvalidated instruction to the host terminal rather than a harmless extra.
//
// Because the original bytes are what the host parses, "declared" has to mean
// byte-exact. encoding/json matches field names case-insensitively and lets the
// last of several matching keys win, while the parsers on the other side of
// this proxy - Python's json.loads for kitty, serde for herdr - keep the keys
// distinct and read the one they name. So {"args":[...],"ARGS":[...]} decoded
// into a struct with an `args` tag validates the second list and forwards a
// request the host runs from the first, and DisallowUnknownFields does not
// object because it uses the same relaxed matching. keysAreExact closes that by
// scanning the raw keys itself: an unknown *or* case-variant *or* repeated key
// is a denial, and trailing data after the object is one too.
//
// An empty payload decodes as an empty object, which is how both protocols
// spell "no parameters".
func StrictUnmarshal(raw []byte, dst any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	if err := keysAreExact(raw, dst); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	// Not dec.More(): it reports false for a stray `}` or `]`, so `{...}}` read
	// as a clean parse here while the parsers on the other side of this proxy
	// reject the whole line. Comparing offsets refuses anything after the value.
	if rest := bytes.TrimSpace(raw[dec.InputOffset():]); len(rest) > 0 {
		return fmt.Errorf("unexpected trailing data after JSON value")
	}
	return nil
}

// keysAreExact reports whether every key of the top-level object is a
// byte-exact JSON name dst declares and appears at most once.
//
// Both "not an object" and "not a struct pointer" are refused here rather than
// left to Decode. Decode produces the accurate type error for most non-objects,
// but not for the two shapes that matter: a top-level `null` decodes into a
// struct pointer as a silent no-op, leaving the caller vetting a zero value it
// believes came off the wire, and a non-struct dst makes this scan a no-op that
// degrades StrictUnmarshal to DisallowUnknownFields - which this package's own
// doc comment explains is not enough. Only the top level is scanned, because
// every nested value these payloads carry is either a scalar, an array of
// scalars, or a json.RawMessage that its own validator strict-decodes in turn.
func keysAreExact(raw []byte, dst any) error {
	declared, ok := declaredJSONNames(dst)
	if !ok {
		return fmt.Errorf("strict decode target %T is not a struct pointer", dst)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	// The token itself is not quoted back: it is sandbox-supplied and this
	// error reaches a log line.
	if delim, isDelim := tok.(json.Delim); !isDelim || delim != '{' {
		return fmt.Errorf("json: expected an object")
	}

	seen := make(map[string]struct{}, len(declared))
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, isString := keyTok.(string)
		if !isString {
			return fmt.Errorf("malformed object key")
		}
		if _, known := declared[key]; !known {
			return fmt.Errorf("json: unknown field %q", key)
		}
		if _, dup := seen[key]; dup {
			return fmt.Errorf("json: duplicate field %q", key)
		}
		seen[key] = struct{}{}

		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return err
		}
	}
	return nil
}

// declaredJSONNames returns the exact JSON names of dst's exported fields.
// Embedded structs are deliberately not flattened: a payload that grows one
// would deny rather than admit an unscanned name, which is the direction this
// package fails in.
func declaredJSONNames(dst any) (map[string]struct{}, bool) {
	v := reflect.ValueOf(dst)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return nil, false
	}
	t := v.Elem().Type()
	if t.Kind() != reflect.Struct {
		return nil, false
	}

	names := make(map[string]struct{}, t.NumField())
	for f := range t.Fields() {
		if f.PkgPath != "" {
			continue
		}
		name := f.Name
		if tag := f.Tag.Get("json"); tag != "" {
			tagName, _, _ := strings.Cut(tag, ",")
			if tagName == "-" {
				continue
			}
			if tagName != "" {
				name = tagName
			}
		}
		names[name] = struct{}{}
	}
	return names, true
}
