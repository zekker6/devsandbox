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

// SchemaFunc reports the struct type one entry of a free-form table decodes
// into. [tools.<name>] and [proxy.credentials.<name>] are map[string]any in
// Config because the tool or injector that owns the section parses it, so the
// owning package names the type instead and the same reflection that checks the
// rest of the file applies to them too.
//
// Returning false means the entry name itself is unrecognized - a misspelled
// tool name, say - and the whole entry is reported.
type SchemaFunc func(entry string) (reflect.Type, bool)

// freeFormSchemas resolves the entries of free-form tables, keyed by the dotted
// path of the table itself ("tools", "proxy.credentials"). Written from package
// init functions, read afterwards.
var freeFormSchemas = map[string]SchemaFunc{}

// RegisterFreeFormSchema installs the schema resolver for the free-form table
// at path. The package that parses the section registers it from init, so a
// path with no resolver means that package is not linked into this binary: the
// section is then left unchecked rather than reported wholesale.
func RegisterFreeFormSchema(path string, resolve SchemaFunc) {
	freeFormSchemas[path] = resolve
}

// toolsPath is the dotted path of the free-form [tools.<name>] table.
const toolsPath = "tools"

// toolDeclaresKey reports whether the [tools.<name>] section describes key.
//
// It exists so a value check can be skipped for a key that section does not
// have: such a key is pruned and reported as ignored, and judging its value on
// top of that tells the user the same key both does nothing and is fatal. An
// unrecognized tool name answers false for the same reason - the whole entry
// was already reported.
func toolDeclaresKey(name, key string) bool {
	resolve, ok := freeFormSchemas[toolsPath]
	if !ok {
		return true // nothing to judge sections against, so nothing pruned them either
	}
	schema, known := resolve(name)
	if !known {
		return false
	}
	if deref(schema).Kind() != reflect.Struct {
		return false
	}
	_, declared := tomlFieldIndex(deref(schema), key)
	return declared
}

// pruneUnknownKeys decodes data into a generic tree and removes every key that
// does not resolve to a field of Config, returning the surviving tree and the
// removed key paths.
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

	msg := fmt.Sprintf("%s in %s, ignored: %s", label, path, strings.Join(unknown, ", "))
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
	case reflect.Map:
		// A map takes any key, so only the package that owns the section can
		// say which entries and keys are real.
		if resolve, ok := freeFormSchemas[path]; ok {
			pruneFreeForm(table, resolve, path, unknown)
			return
		}
	case reflect.Struct:
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

// pruneFreeForm checks a free-form table - one whose entries the user names,
// such as [tools.<name>] - against the type its owning package declares for
// them.
func pruneFreeForm(table map[string]any, resolve SchemaFunc, path string, unknown *[]string) {
	for name, val := range table {
		schema, ok := resolve(name)
		if !ok {
			delete(table, name)
			*unknown = append(*unknown, joinKey(path, name))
			continue
		}
		pruneChild(val, schema, joinKey(path, name), unknown)
	}
}

// childType returns the type a key inside dst decodes into.
func childType(dst reflect.Type, key string) (reflect.Type, bool) {
	if dst.Kind() == reflect.Map {
		return dst.Elem(), true // any key is valid, the value type is fixed
	}
	return tomlField(dst, key)
}

// tomlField resolves a TOML key to the type of the struct field it decodes
// into.
func tomlField(t reflect.Type, key string) (reflect.Type, bool) {
	index, ok := tomlFieldIndex(t, key)
	if !ok {
		return nil, false
	}
	return t.FieldByIndex(index).Type, true
}

// tomlFieldIndex resolves a TOML key to a struct field, mirroring the decoder's
// matching: the toml tag when set and the field name otherwise, exact match
// first and case-insensitive second. The result indexes t the way
// reflect.Value.FieldByIndex expects, so it reaches fields promoted from an
// embedded struct.
func tomlFieldIndex(t reflect.Type, key string) ([]int, bool) {
	exact, fold := lookupTOMLField(t, key)
	if exact != nil {
		return exact, true
	}
	return fold, fold != nil
}

// lookupTOMLField returns the shallowest exact and case-insensitive matches for
// key in t.
//
// Depth is what settles two fields of the same name: the decoder flattens the
// struct before it matches anything, and a field of t itself shadows one
// promoted from an embedded struct. Returning the first match found while
// walking would invert that for an embedded field declared before the outer one
// - the pruner would then check the key against one field's type while the
// decoder wrote the other.
func lookupTOMLField(t reflect.Type, key string) (exact, fold []int) {
	var embeds []reflect.StructField

	for f := range t.Fields() {
		if f.PkgPath != "" && !f.Anonymous {
			continue // unexported
		}

		tag := f.Tag.Get("toml")
		if tag == "-" {
			continue
		}
		if f.Anonymous && tag == "" {
			// Embedded struct: the decoder promotes its fields, but only
			// where t declares nothing of its own by that name.
			if deref(f.Type).Kind() == reflect.Struct {
				embeds = append(embeds, f)
			}
			continue
		}

		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		if name == key {
			if exact == nil {
				exact = f.Index
			}
			continue
		}
		if fold == nil && strings.EqualFold(name, key) {
			fold = f.Index
		}
	}

	// An exact match on t's own fields settles it; a case-insensitive one does
	// not, since the decoder prefers an exact match at any depth over a folded
	// one.
	if exact != nil {
		return exact, fold
	}

	for _, f := range embeds {
		childExact, childFold := lookupTOMLField(deref(f.Type), key)
		if exact == nil && childExact != nil {
			exact = append([]int{f.Index[0]}, childExact...)
		}
		if fold == nil && childFold != nil {
			fold = append([]int{f.Index[0]}, childFold...)
		}
	}
	return exact, fold
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
