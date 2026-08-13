package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

type decodeTarget struct {
	Name     string          `toml:"name"`
	Enabled  bool            `toml:"enabled"`
	Count    int             `toml:"count"`
	Ratio    float64         `toml:"ratio"`
	Tags     []string        `toml:"tags"`
	Nested   *decodeNested   `toml:"nested"`
	Inline   decodeNested    `toml:"inline"`
	Freeform map[string]any  `toml:"freeform"`
	Named    map[string]bool `toml:"named"`
}

type decodeNested struct {
	Env string `toml:"env"`
}

// section parses a TOML document the way a config file reaches DecodeSection.
func section(t *testing.T, doc string) map[string]any {
	t.Helper()
	var table map[string]any
	if err := toml.Unmarshal([]byte(doc), &table); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return table
}

func TestDecodeSection(t *testing.T) {
	got := decodeTarget{}
	err := DecodeSection(section(t, `
name = "x"
enabled = true
count = 7
ratio = 2
tags = ["a", "b"]
freeform = { anything = 1 }
named = { a = true }

[nested]
env = "TOKEN"

[inline]
env = "OTHER"
`), &got)
	if err != nil {
		t.Fatalf("DecodeSection: %v", err)
	}

	want := decodeTarget{
		Name:     "x",
		Enabled:  true,
		Count:    7,
		Ratio:    2,
		Tags:     []string{"a", "b"},
		Nested:   &decodeNested{Env: "TOKEN"},
		Inline:   decodeNested{Env: "OTHER"},
		Freeform: map[string]any{"anything": int64(1)},
		Named:    map[string]bool{"a": true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decoded = %#v, want %#v", got, want)
	}
}

// A value of the wrong type is what this whole path exists to catch: ignoring
// it applies a default the user did not ask for.
func TestDecodeSection_RejectsMistypedValues(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want string
	}{
		{name: "string field", doc: "name = 5", want: "name: expected a string, got an integer"},
		{name: "bool field", doc: `enabled = "yes"`, want: "enabled: expected a boolean, got a string"},
		{name: "int field", doc: "count = 1.5", want: "count: expected an integer, got a float"},
		{name: "array field", doc: `tags = "a"`, want: "tags: expected an array of a string, got a string"},
		{name: "array element", doc: `tags = ["a", 2]`, want: "tags[1]: expected a string, got an integer"},
		{name: "table field", doc: `nested = "x"`, want: "nested: expected a table, got a string"},
		{name: "key in sub-table", doc: "[nested]\nenv = true", want: "nested.env: expected a string, got a boolean"},
		{name: "map value", doc: `named = { a = "x" }`, want: "named.a: expected a boolean, got a string"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got decodeTarget
			err := DecodeSection(section(t, tt.doc), &got)
			if err == nil {
				t.Fatalf("decoded %q without error: %#v", tt.doc, got)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

// A value TOML accepts can still be too large for the field it names.
func TestDecodeSection_RejectsOutOfRangeInteger(t *testing.T) {
	var got struct {
		Small int8 `toml:"small"`
	}
	err := DecodeSection(section(t, "small = 300"), &got)
	if err == nil {
		t.Fatalf("decoded out of range value: %#v", got)
	}
	if !strings.Contains(err.Error(), "small: 300 is out of range") {
		t.Errorf("error = %q, want it to name the field and the value", err)
	}
}

// Half-applying a section would enforce something the user never wrote.
func TestDecodeSection_LeavesTargetAloneOnError(t *testing.T) {
	got := decodeTarget{Name: "default"}
	if err := DecodeSection(section(t, "name = \"new\"\ncount = \"x\"\n"), &got); err == nil {
		t.Fatal("expected an error")
	}
	if got.Name != "default" {
		t.Errorf("name = %q, want the untouched default", got.Name)
	}
}

// Unknown keys are warned about, not rejected: a typo must not abort a launch.
func TestDecodeSection_IgnoresUnknownKeys(t *testing.T) {
	got := decodeTarget{}
	if err := DecodeSection(section(t, "name = \"x\"\nbogus = 1\n"), &got); err != nil {
		t.Fatalf("DecodeSection: %v", err)
	}
	if got.Name != "x" {
		t.Errorf("name = %q, want x", got.Name)
	}
}

func TestDecodeSection_NilSection(t *testing.T) {
	got := decodeTarget{Name: "default"}
	if err := DecodeSection(nil, &got); err != nil {
		t.Fatalf("DecodeSection: %v", err)
	}
	if got.Name != "default" {
		t.Errorf("name = %q, want the untouched default", got.Name)
	}
}

func TestDecodeSection_RejectsBadTarget(t *testing.T) {
	if err := DecodeSection(map[string]any{}, decodeTarget{}); err == nil {
		t.Error("expected an error for a non-pointer target")
	}
	var nilTarget *decodeTarget
	if err := DecodeSection(map[string]any{}, nilTarget); err == nil {
		t.Error("expected an error for a nil pointer target")
	}
}

// The embedded case: a tool config embeds tools.MountModeConfig for mount_mode.
func TestDecodeSection_EmbeddedStruct(t *testing.T) {
	type embedded struct {
		Mode string `toml:"mode"`
	}
	type outer struct {
		embedded
		Extra string `toml:"extra"`
	}

	var got outer
	if err := DecodeSection(section(t, "mode = \"readwrite\"\nextra = \"x\"\n"), &got); err != nil {
		t.Fatalf("DecodeSection: %v", err)
	}
	if got.Mode != "readwrite" || got.Extra != "x" {
		t.Errorf("decoded = %#v, want both fields set", got)
	}

	if err := DecodeSection(section(t, "mode = 1\n"), &got); err == nil {
		t.Error("a mistyped promoted field was accepted")
	}
}

// A named type is not assignable from the value the decoder produces, so every
// scalar kind needs a case of its own. Without one, a section declaring
// `type Mode string` failed the whole load with "expected a string, got a
// string".
func TestDecodeSection_NamedScalarTypes(t *testing.T) {
	type mode string
	type flag bool

	var got struct {
		Mode mode `toml:"mode"`
		Flag flag `toml:"flag"`
	}
	if err := DecodeSection(section(t, "mode = \"auto\"\nflag = true\n"), &got); err != nil {
		t.Fatalf("DecodeSection: %v", err)
	}
	if got.Mode != "auto" || got.Flag != true {
		t.Errorf("decoded = %#v, want mode=auto flag=true", got)
	}

	err := DecodeSection(section(t, "mode = 1\n"), &got)
	if err == nil {
		t.Fatal("a mistyped named field was accepted")
	}
	if want := "mode: expected a string, got an integer"; !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to contain %q", err, want)
	}
}

// The decoder promotes the fields of an embedded pointer struct too, and
// reflect.Value.FieldByIndex panics on a nil one rather than allocating it.
func TestDecodeSection_EmbeddedPointerStruct(t *testing.T) {
	type Embedded struct {
		Mode string `toml:"mode"`
	}
	type outer struct {
		*Embedded
		Extra string `toml:"extra"`
	}

	var got outer
	if err := DecodeSection(section(t, "mode = \"readwrite\"\nextra = \"x\"\n"), &got); err != nil {
		t.Fatalf("DecodeSection: %v", err)
	}
	if got.Embedded == nil {
		t.Fatalf("decoded = %#v, want the embedded pointer allocated", got)
	}
	if got.Mode != "readwrite" || got.Extra != "x" {
		t.Errorf("decoded = %#v, want both fields set", got)
	}
}

func TestValidateFreeForm(t *testing.T) {
	registerTestSchema(t, "tools", func(entry string) (reflect.Type, bool) {
		if entry != "mise" {
			return nil, false
		}
		return reflect.TypeFor[testToolSection](), true
	})

	tests := []struct {
		name    string
		entries map[string]any
		want    string
	}{
		{
			name:    "recognized section",
			entries: map[string]any{"mise": map[string]any{"ignore_global_config": true}},
		},
		{
			name:    "unknown tool is left to the unknown-key warning",
			entries: map[string]any{"gti": map[string]any{"whatever": 1}},
		},
		{
			name:    "mistyped key",
			entries: map[string]any{"mise": map[string]any{"ignore_global_config": "yes"}},
			want:    "[tools.mise]: ignore_global_config: expected a boolean, got a string",
		},
		{
			name:    "section that is not a table",
			entries: map[string]any{"mise": "readonly"},
			want:    "[tools.mise]: expected a table, got a string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFreeForm("tools", tt.entries)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.want != "" && err == nil:
				t.Fatal("expected an error")
			case tt.want != "" && !strings.Contains(err.Error(), tt.want):
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

// A path with no registered type belongs to a package this binary does not
// link, and must not be judged.
func TestValidateFreeForm_UnregisteredPath(t *testing.T) {
	if err := validateFreeForm("nowhere", map[string]any{"x": map[string]any{"y": 1}}); err != nil {
		t.Errorf("unregistered path was judged: %v", err)
	}
}
