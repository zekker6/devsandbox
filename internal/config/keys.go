package config

import (
	"bytes"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"devsandbox/internal/notice"
	"github.com/BurntSushi/toml"
)

// maxReportedUnknownKeys bounds how many key names a single warning lists.
// The remainder is counted rather than dropped silently.
const maxReportedUnknownKeys = 10

// pruneUnknownKeys decodes data into a generic tree and removes every key that
// does not resolve to a field of Config, returning the surviving tree and the
// removed key paths.
//
// Free-form sections are kept as-is: [tools.<name>] and
// [proxy.credentials.<name>] decode into map[string]any, so each tool and
// credential injector parses its own schema and this package cannot judge it.
func pruneUnknownKeys(data []byte) (map[string]any, []string, error) {
	var tree map[string]any
	if err := toml.Unmarshal(data, &tree); err != nil {
		return nil, nil, err
	}

	var unknown []string
	pruneTable(tree, reflect.TypeFor[Config](), "", &unknown)

	// Array-of-table elements share one key path, so the same unknown key can
	// be collected once per element.
	slices.Sort(unknown)
	return tree, slices.Compact(unknown), nil
}

// renderTOML encodes a pruned tree back to TOML.
func renderTOML(tree map[string]any) (string, error) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(tree); err != nil {
		return "", fmt.Errorf("failed to render config: %w", err)
	}
	return buf.String(), nil
}

// reportUnknownKeys warns about keys in data that Config does not describe.
// Call it only after data has decoded into Config successfully: a decode error
// here is the same error the caller's own decode reports, so it is dropped
// rather than surfaced twice.
func reportUnknownKeys(path string, data []byte) {
	_, unknown, err := pruneUnknownKeys(data)
	if err != nil {
		return
	}
	warnUnknownKeys(path, unknown)
}

// unknownKeysWarned keeps each distinct warning to one per process. A single
// command can load the same config more than once - devsandbox doctor does -
// and repeating the warning makes it read like two distinct problems.
var unknownKeysWarned sync.Map

// warnUnknownKeys reports unrecognized keys. Such keys are ignored by the
// decoder, so a typo silently disables the setting it was meant to configure.
func warnUnknownKeys(path string, unknown []string) {
	if len(unknown) == 0 {
		return
	}

	label := "unknown config key"
	if len(unknown) > 1 {
		label += "s"
	}

	listed := unknown
	suffix := ""
	if len(listed) > maxReportedUnknownKeys {
		listed = listed[:maxReportedUnknownKeys]
		suffix = fmt.Sprintf(" (+%d more)", len(unknown)-maxReportedUnknownKeys)
	}

	msg := fmt.Sprintf("%s in %s, ignored: %s%s", label, path, strings.Join(listed, ", "), suffix)
	if _, seen := unknownKeysWarned.LoadOrStore(msg, struct{}{}); seen {
		return
	}
	notice.Warn("%s", msg)
}

// pruneTable deletes from table every key the destination type does not
// describe, recursing into the values it keeps. dst is the Go type the table
// decodes into; path is the dotted key path of the table itself.
func pruneTable(table map[string]any, dst reflect.Type, path string, unknown *[]string) {
	dst = deref(dst)

	switch dst.Kind() {
	case reflect.Interface:
		return // free-form subtree, every key belongs to someone else's schema
	case reflect.Map, reflect.Struct:
	default:
		// A scalar destination cannot hold a table. The keys are not unknown,
		// the value shape is wrong, and the decode into Config reports that.
		return
	}

	for key, val := range table {
		child, ok := childType(dst, key)
		if !ok {
			delete(table, key)
			*unknown = append(*unknown, joinKey(path, key))
			continue
		}
		pruneChild(val, child, joinKey(path, key), unknown)
	}
}

// pruneChild recurses into whatever tables are reachable from val.
func pruneChild(val any, dst reflect.Type, path string, unknown *[]string) {
	switch v := val.(type) {
	case map[string]any:
		pruneTable(v, dst, path, unknown)
	case []map[string]any:
		// Array of tables: every element decodes into the slice element type
		// and the key path carries no index, so all elements share one path.
		for _, elem := range v {
			pruneTable(elem, elemType(dst), path, unknown)
		}
	case []any:
		for _, elem := range v {
			if m, ok := elem.(map[string]any); ok {
				pruneTable(m, elemType(dst), path, unknown)
			}
		}
	}
}

// childType returns the type a key inside dst decodes into.
func childType(dst reflect.Type, key string) (reflect.Type, bool) {
	if dst.Kind() == reflect.Map {
		return dst.Elem(), true // any key is valid, the value type is fixed
	}
	return tomlField(dst, key)
}

// tomlField resolves a TOML key to a struct field, mirroring the decoder's
// matching: the toml tag when set and the field name otherwise, exact match
// first and case-insensitive second.
func tomlField(t reflect.Type, key string) (reflect.Type, bool) {
	var fallback reflect.Type
	var haveFallback bool

	for f := range t.Fields() {
		if f.PkgPath != "" && !f.Anonymous {
			continue // unexported
		}

		tag := f.Tag.Get("toml")
		if tag == "-" {
			continue
		}
		if f.Anonymous && tag == "" {
			// Embedded struct: the decoder promotes its fields.
			if embedded := deref(f.Type); embedded.Kind() == reflect.Struct {
				if child, ok := tomlField(embedded, key); ok {
					return child, true
				}
			}
			continue
		}

		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		if name == key {
			return f.Type, true
		}
		if !haveFallback && strings.EqualFold(name, key) {
			fallback, haveFallback = f.Type, true
		}
	}

	return fallback, haveFallback
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func elemType(t reflect.Type) reflect.Type {
	t = deref(t)
	if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		return t.Elem()
	}
	return t
}

// bareKeyPattern matches TOML keys that need no quoting.
var bareKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// joinKey appends key to a dotted path, quoting keys that are not bare. A key
// comes from an untrusted file and ends up on the terminal, so a key holding
// control characters must not be able to forge output around the trust prompt.
func joinKey(path, key string) string {
	if !bareKeyPattern.MatchString(key) {
		key = strconv.Quote(key)
	}
	if path == "" {
		return key
	}
	return path + "." + key
}
