package socketproxy

import (
	"bytes"
	"encoding/json"
)

// StrictUnmarshal decodes raw into dst and rejects any field dst does not
// declare, so a parameter the filter does not model cannot ride along
// unchecked into the host process. Both socket proxies forward the original
// request bytes upstream on allow, which makes an undeclared field an
// unvalidated instruction to the host terminal rather than a harmless extra.
//
// An empty payload decodes as an empty object, which is how both protocols
// spell "no parameters".
func StrictUnmarshal(raw []byte, dst any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
