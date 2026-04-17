// Package source resolves string values from pluggable sources:
// a literal value, a host environment variable, or a file on disk.
// Used by proxy credential injection and sandbox environment variables.
package source

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Source describes where a value comes from. When multiple fields are set,
// priority is Value > Env > File.
type Source struct {
	Value string // static value
	Env   string // host env var name
	File  string // file path (supports ~ expansion, whitespace trimmed)
}

// IsZero reports whether no source field is populated.
func (s *Source) IsZero() bool {
	return s == nil || (s.Value == "" && s.Env == "" && s.File == "")
}

// Resolve returns the value from the first non-empty field in priority
// order: Value, Env, File. A nil receiver returns ("", nil).
//
// When Env is set, Resolve returns os.Getenv — unset and set-to-empty
// both produce "". Callers that need to distinguish the two must use
// os.LookupEnv directly before calling Resolve.
//
// When File is set and the file cannot be read, Resolve returns a
// wrapped error. Missing env vars are not an error.
func (s *Source) Resolve() (string, error) {
	if s == nil {
		return "", nil
	}
	if s.Value != "" {
		return s.Value, nil
	}
	if s.Env != "" {
		return os.Getenv(s.Env), nil
	}
	if s.File != "" {
		path := ExpandHome(s.File)
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read source file %q: %w", s.File, err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	return "", nil
}

// Parse extracts the "source" sub-table from cfg. cfg should be the raw
// TOML map for a parent config section (e.g. the map for
// [proxy.credentials.github] or [sandbox.environment.GH_TOKEN]), not the
// source table itself. Returns nil when the "source" key is absent or
// not a map[string]any.
func Parse(cfg map[string]any) *Source {
	if cfg == nil {
		return nil
	}
	raw, ok := cfg["source"].(map[string]any)
	if !ok {
		return nil
	}
	src := &Source{}
	if v, ok := raw["value"].(string); ok {
		src.Value = v
	}
	if v, ok := raw["env"].(string); ok {
		src.Env = v
	}
	if v, ok := raw["file"].(string); ok {
		src.File = v
	}
	return src
}

// ExpandHome expands a leading ~ to the user's home directory.
func ExpandHome(path string) string {
	if path == "" || path[0] != '~' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if len(path) == 1 {
		return home
	}
	if path[1] != '/' {
		return path
	}
	return filepath.Join(home, path[2:])
}
