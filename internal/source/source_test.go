package source

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSource_Resolve_Value(t *testing.T) {
	s := Source{Value: "literal"}
	if got, err := s.Resolve(); err != nil || got != "literal" {
		t.Fatalf("Resolve() = %q, %v; want \"literal\", nil", got, err)
	}
}

func TestSource_Resolve_Env(t *testing.T) {
	t.Run("env set returns value", func(t *testing.T) {
		t.Setenv("TEST_SRC_VAR", "from-env")
		s := Source{Env: "TEST_SRC_VAR"}
		got, err := s.Resolve()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "from-env" {
			t.Errorf("Resolve() = %q, want %q", got, "from-env")
		}
	})

	t.Run("env unset returns empty, no error", func(t *testing.T) {
		if err := os.Unsetenv("TEST_SRC_MISSING"); err != nil {
			t.Fatal(err)
		}
		s := Source{Env: "TEST_SRC_MISSING"}
		got, err := s.Resolve()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("Resolve() = %q, want empty", got)
		}
	})

	t.Run("env set to empty returns empty", func(t *testing.T) {
		t.Setenv("TEST_SRC_EMPTY", "")
		s := Source{Env: "TEST_SRC_EMPTY"}
		got, err := s.Resolve()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("Resolve() = %q, want empty", got)
		}
	})
}

func TestSource_Resolve_File(t *testing.T) {
	t.Run("reads file and trims", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "secret")
		if err := os.WriteFile(path, []byte("  contents\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		s := Source{File: path}
		got, err := s.Resolve()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "contents" {
			t.Errorf("Resolve() = %q, want %q", got, "contents")
		}
	})

	t.Run("missing file returns error", func(t *testing.T) {
		s := Source{File: "/nonexistent/path/xyz"}
		_, err := s.Resolve()
		if err == nil {
			t.Error("expected error for missing file")
		}
	})
}

func TestSource_Resolve_Priority(t *testing.T) {
	t.Setenv("TEST_SRC_PRIO", "from-env")
	s := Source{Value: "from-value", Env: "TEST_SRC_PRIO"}
	got, err := s.Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "from-value" {
		t.Errorf("Resolve() = %q, want %q (value > env)", got, "from-value")
	}
}

func TestSource_Resolve_Priority_EnvBeatsFile(t *testing.T) {
	t.Setenv("TEST_SRC_EVF", "from-env")
	// File path is intentionally garbage — if priority breaks, we'd see a read error.
	s := Source{Env: "TEST_SRC_EVF", File: "/nonexistent/xyz"}
	got, err := s.Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "from-env" {
		t.Errorf("Resolve() = %q, want %q (env > file)", got, "from-env")
	}
}

func TestSource_IsZero(t *testing.T) {
	if !(&Source{}).IsZero() {
		t.Error("expected zero Source to be IsZero=true")
	}
	if (&Source{Value: "x"}).IsZero() {
		t.Error("expected non-zero Source to be IsZero=false")
	}
}

func TestParse(t *testing.T) {
	t.Run("nil returns nil", func(t *testing.T) {
		if got := Parse(nil); got != nil {
			t.Errorf("expected nil, got %+v", got)
		}
	})

	t.Run("no source key returns nil", func(t *testing.T) {
		if got := Parse(map[string]any{"enabled": true}); got != nil {
			t.Errorf("expected nil, got %+v", got)
		}
	})

	t.Run("reads all fields", func(t *testing.T) {
		got := Parse(map[string]any{
			"source": map[string]any{
				"value": "v",
				"env":   "E",
				"file":  "F",
			},
		})
		if got == nil {
			t.Fatal("expected non-nil")
		}
		if got.Value != "v" || got.Env != "E" || got.File != "F" {
			t.Errorf("Parse() = %+v, want {Value:v Env:E File:F}", got)
		}
	})
}
