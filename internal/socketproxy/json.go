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
	if dec.More() {
		return fmt.Errorf("unexpected trailing data after JSON value")
	}
	return nil
}

// keysAreExact reports whether every key of the top-level object is a
// byte-exact JSON name dst declares and appears at most once.
//
// A raw value that is not an object, or a dst that is not a struct pointer, is
// left to Decode: it produces the accurate type error, and there are no keys to
// confuse. Only the top level is scanned, because every nested value these
// payloads carry is either a scalar, an array of scalars, or a json.RawMessage
// that its own validator strict-decodes in turn.
func keysAreExact(raw []byte, dst any) error {
	declared, ok := declaredJSONNames(dst)
	if !ok {
		return nil
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, isDelim := tok.(json.Delim); !isDelim || delim != '{' {
		return nil
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
