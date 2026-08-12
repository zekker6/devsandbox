package config

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"time"
)

// DecodeSection fills dst from one entry of a free-form config table - a
// section devsandbox holds as map[string]any because the package that owns it
// parses it, such as [tools.<name>].
//
// A value that does not fit the field its key names is an error. Ignoring it
// would leave the field at a default the user did not ask for, which is the
// failure this whole path exists to prevent, so the section is rejected whole:
// dst is left untouched unless every key decodes.
//
// Keys dst does not describe are skipped rather than rejected. They are already
// reported as unknown keys, and reporting them twice - once as a warning and
// once as a fatal error - would make an ignorable typo abort the launch.
func DecodeSection(section map[string]any, dst any) error {
	ptr := reflect.ValueOf(dst)
	if ptr.Kind() != reflect.Pointer || ptr.IsNil() {
		return fmt.Errorf("decode target must be a non-nil pointer, got %T", dst)
	}

	scratch := reflect.New(ptr.Elem().Type()).Elem()
	scratch.Set(ptr.Elem()) // keep whatever defaults the caller set
	if err := decodeTable(section, scratch, ""); err != nil {
		return err
	}
	ptr.Elem().Set(scratch)
	return nil
}

// validateFreeForm decodes every entry of the free-form table at path into the
// type its owning package declares, so a key whose value does not fit is
// reported at load time rather than dropped by whichever package reads it next.
// A path with no registered type belongs to a package this binary does not
// link, and is left alone.
func validateFreeForm(path string, entries map[string]any) error {
	resolve, ok := freeFormSchemas[path]
	if !ok {
		return nil
	}

	for _, name := range slices.Sorted(maps.Keys(entries)) {
		schema, known := resolve(name)
		if !known {
			continue // unrecognized entry name, reported as an unknown key
		}
		section, isTable := entries[name].(map[string]any)
		if !isTable {
			return fmt.Errorf("[%s]: expected a table, got %s", joinKey(path, name), tomlValueType(entries[name]))
		}
		if err := DecodeSection(section, reflect.New(schema).Interface()); err != nil {
			return fmt.Errorf("[%s]: %w", joinKey(path, name), err)
		}
	}
	return nil
}

// decodeTable fills the struct dst from table. Keys are visited in order so a
// section with several problems always reports the same one.
func decodeTable(table map[string]any, dst reflect.Value, path string) error {
	if dst.Kind() != reflect.Struct {
		return fmt.Errorf("%s: cannot decode a table into %s", pathOrRoot(path), dst.Type())
	}

	for _, key := range slices.Sorted(maps.Keys(table)) {
		index, ok := tomlFieldIndex(dst.Type(), key)
		if !ok {
			continue // unknown key, reported by the unknown-key pass
		}
		if err := decodeValue(table[key], fieldByIndex(dst, index), joinKey(path, key)); err != nil {
			return err
		}
	}
	return nil
}

// decodeValue assigns one TOML value to the field it names.
func decodeValue(val any, dst reflect.Value, path string) error {
	dst = allocDeref(dst)

	// The TOML decoder produces string, bool, int64, float64, time.Time,
	// []any and map[string]any, so a field of any of those types takes the
	// value as it stands; the rest are conversions.
	if val != nil && reflect.TypeOf(val).AssignableTo(dst.Type()) {
		dst.Set(reflect.ValueOf(val))
		return nil
	}

	// A named type - type Mode string - is not assignable from the decoder's
	// string, so every kind the decoder can produce needs a case of its own
	// here. Without one such a field reaches the default branch and reports
	// "expected a string, got a string" while failing the whole load.
	switch dst.Kind() {
	case reflect.String:
		s, ok := val.(string)
		if !ok {
			return typeError(path, dst.Type(), val)
		}
		dst.SetString(s)
	case reflect.Bool:
		b, ok := val.(bool)
		if !ok {
			return typeError(path, dst.Type(), val)
		}
		dst.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, ok := val.(int64)
		if !ok {
			return typeError(path, dst.Type(), val)
		}
		if dst.OverflowInt(n) {
			return fmt.Errorf("%s: %d is out of range for %s", path, n, dst.Type())
		}
		dst.SetInt(n)
	case reflect.Float32, reflect.Float64:
		switch n := val.(type) {
		case float64:
			dst.SetFloat(n)
		case int64:
			dst.SetFloat(float64(n))
		default:
			return typeError(path, dst.Type(), val)
		}
	case reflect.Slice:
		return decodeSlice(val, dst, path)
	case reflect.Map:
		return decodeMap(val, dst, path)
	case reflect.Struct:
		table, ok := val.(map[string]any)
		if !ok {
			return typeError(path, dst.Type(), val)
		}
		return decodeTable(table, dst, path)
	default:
		return typeError(path, dst.Type(), val)
	}
	return nil
}

// decodeSlice fills a slice field. An array of tables reaches us as
// []map[string]any rather than []any, so the elements are walked reflectively
// instead of type-switched.
func decodeSlice(val any, dst reflect.Value, path string) error {
	src := reflect.ValueOf(val)
	if val == nil || src.Kind() != reflect.Slice {
		return typeError(path, dst.Type(), val)
	}

	out := reflect.MakeSlice(dst.Type(), src.Len(), src.Len())
	for i := range src.Len() {
		elem := src.Index(i)
		if err := decodeValue(elem.Interface(), out.Index(i), fmt.Sprintf("%s[%d]", path, i)); err != nil {
			return err
		}
	}
	dst.Set(out)
	return nil
}

// decodeMap fills a map field, whose keys the user names and whose values all
// decode into the same type.
func decodeMap(val any, dst reflect.Value, path string) error {
	table, ok := val.(map[string]any)
	if !ok {
		return typeError(path, dst.Type(), val)
	}
	if dst.Type().Key().Kind() != reflect.String {
		return fmt.Errorf("%s: cannot decode a table into %s", path, dst.Type())
	}

	out := reflect.MakeMapWithSize(dst.Type(), len(table))
	for _, key := range slices.Sorted(maps.Keys(table)) {
		elem := reflect.New(dst.Type().Elem()).Elem()
		if err := decodeValue(table[key], elem, joinKey(path, key)); err != nil {
			return err
		}
		out.SetMapIndex(reflect.ValueOf(key).Convert(dst.Type().Key()), elem)
	}
	dst.Set(out)
	return nil
}

// fieldByIndex reaches the field an index path names, allocating the embedded
// pointer structs it passes through. reflect.Value.FieldByIndex panics on a nil
// one rather than allocating, and tomlFieldIndex resolves keys through embedded
// pointers because the decoder promotes their fields too.
func fieldByIndex(v reflect.Value, index []int) reflect.Value {
	for i, x := range index {
		if i > 0 {
			v = allocDeref(v)
		}
		v = v.Field(x)
	}
	return v
}

// allocDeref walks pointer fields down to the value they address, allocating
// along the way. A *Source field left nil by the user stays nil; one the config
// names gets a value to decode into.
func allocDeref(v reflect.Value) reflect.Value {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		v = v.Elem()
	}
	return v
}

// typeError reports a value that does not fit the field its key names, in the
// vocabulary of the config file rather than of Go.
func typeError(path string, want reflect.Type, got any) error {
	return fmt.Errorf("%s: expected %s, got %s", path, tomlTypeOf(want), tomlValueType(got))
}

// tomlValueType names the TOML type of a decoded value.
func tomlValueType(val any) string {
	switch v := val.(type) {
	case nil:
		return "nothing"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case int64:
		return "an integer"
	case float64:
		return "a float"
	case time.Time:
		return "a datetime"
	case map[string]any:
		return "a table"
	default:
		if reflect.ValueOf(v).Kind() == reflect.Slice {
			return "an array"
		}
		return fmt.Sprintf("%T", v)
	}
}

// tomlTypeOf names the TOML type a Go field accepts.
func tomlTypeOf(t reflect.Type) string {
	switch deref(t).Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "an integer"
	case reflect.Float32, reflect.Float64:
		return "a float"
	case reflect.Slice, reflect.Array:
		return "an array of " + tomlTypeOf(deref(t).Elem())
	case reflect.Map, reflect.Struct:
		return "a table"
	default:
		return t.String()
	}
}

// pathOrRoot names the section itself when an error is not about one key.
func pathOrRoot(path string) string {
	if path == "" {
		return "section"
	}
	return path
}
