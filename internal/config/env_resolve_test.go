package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devsandbox/internal/source"
)

func TestResolveSandboxEnvironment(t *testing.T) {
	t.Run("resolves value/env/file", func(t *testing.T) {
		t.Setenv("HOST_X", "host-value")
		dir := t.TempDir()
		path := filepath.Join(dir, "tok")
		if err := os.WriteFile(path, []byte("file-value\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		env := map[string]source.Source{
			"FROM_VAL":  {Value: "literal"},
			"FROM_ENV":  {Env: "HOST_X"},
			"FROM_FILE": {File: path},
		}

		got, err := ResolveSandboxEnvironment(env)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := map[string]string{
			"FROM_VAL":  "literal",
			"FROM_ENV":  "host-value",
			"FROM_FILE": "file-value",
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("got[%q] = %q, want %q", k, got[k], v)
			}
		}
	})

	t.Run("skips env source when host var unset", func(t *testing.T) {
		if err := os.Unsetenv("MISSING_X"); err != nil {
			t.Fatal(err)
		}
		got, err := ResolveSandboxEnvironment(map[string]source.Source{
			"PRESENT": {Value: "v"},
			"ABSENT":  {Env: "MISSING_X"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := got["ABSENT"]; ok {
			t.Errorf("ABSENT should be skipped, got %q", got["ABSENT"])
		}
		if got["PRESENT"] != "v" {
			t.Errorf("PRESENT = %q, want v", got["PRESENT"])
		}
	})

	t.Run("errors on unreadable file", func(t *testing.T) {
		_, err := ResolveSandboxEnvironment(map[string]source.Source{
			"BAD": {File: "/no/such/path/xyzzy"},
		})
		if err == nil || !strings.Contains(err.Error(), "BAD") {
			t.Errorf("expected error mentioning BAD, got %v", err)
		}
	})

	t.Run("empty input returns nil", func(t *testing.T) {
		got, err := ResolveSandboxEnvironment(nil)
		if err != nil || got != nil {
			t.Errorf("got=%v err=%v, want nil,nil", got, err)
		}
	})
}
