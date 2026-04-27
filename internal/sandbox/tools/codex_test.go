package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCodex_Bindings_DefaultPath(t *testing.T) {
	t.Setenv("CODEX_HOME", "")

	c := &Codex{}
	bindings := c.Bindings("/home/test", "/tmp/sandbox")

	var foundDefault bool
	for _, b := range bindings {
		if b.Source == "/home/test/.codex" {
			foundDefault = true
			if b.Category != CategoryConfig {
				t.Errorf("~/.codex binding: expected category %q, got %q", CategoryConfig, b.Category)
			}
		}
	}

	if !foundDefault {
		t.Error("expected ~/.codex binding when CODEX_HOME is not set")
	}
}

func TestCodex_Bindings_CodexHome(t *testing.T) {
	customDir := "/etc/codex-home"
	t.Setenv("CODEX_HOME", customDir)

	c := &Codex{}
	bindings := c.Bindings("/home/test", "/tmp/sandbox")

	var foundCustom, foundDefault bool
	for _, b := range bindings {
		switch b.Source {
		case customDir:
			foundCustom = true
			if b.Category != CategoryConfig {
				t.Errorf("CODEX_HOME binding: expected category %q, got %q", CategoryConfig, b.Category)
			}
			if !b.Optional {
				t.Error("CODEX_HOME binding should be optional")
			}
		case "/home/test/.codex":
			foundDefault = true
		}
	}

	if !foundCustom {
		t.Error("expected CODEX_HOME binding when env var is set")
	}
	if foundDefault {
		t.Error("default ~/.codex should NOT be mounted when CODEX_HOME is set")
	}
}

func TestCodex_Environment_NoEnvVar(t *testing.T) {
	t.Setenv("CODEX_HOME", "")

	c := &Codex{}
	envVars := c.Environment("/home/test", "/tmp/sandbox")

	for _, env := range envVars {
		if env.Name == "CODEX_HOME" {
			t.Error("CODEX_HOME should not be exported when host env var is empty")
		}
	}
}

func TestCodex_Environment_WithCodexHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "/etc/codex-home")

	c := &Codex{}
	envVars := c.Environment("/home/test", "/tmp/sandbox")

	var found bool
	for _, env := range envVars {
		if env.Name == "CODEX_HOME" {
			found = true
			if !env.FromHost {
				t.Error("CODEX_HOME should use FromHost to pass through the host value")
			}
		}
	}

	if !found {
		t.Error("expected CODEX_HOME env var when set on host")
	}
}

func TestCodex_Available_CodexHome(t *testing.T) {
	tmpDir := t.TempDir()
	customDir := filepath.Join(tmpDir, "codex-home")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CODEX_HOME", customDir)
	t.Setenv("PATH", "")

	c := &Codex{}
	emptyHome := filepath.Join(tmpDir, "empty-home")
	if err := os.MkdirAll(emptyHome, 0o755); err != nil {
		t.Fatal(err)
	}

	if !c.Available(emptyHome) {
		t.Error("Codex should be available when CODEX_HOME points to existing directory")
	}
}

func TestCodex_Check_CodexHome(t *testing.T) {
	tmpDir := t.TempDir()
	customDir := filepath.Join(tmpDir, "codex-home")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CODEX_HOME", customDir)

	c := &Codex{}
	emptyHome := filepath.Join(tmpDir, "empty-home")
	if err := os.MkdirAll(emptyHome, 0o755); err != nil {
		t.Fatal(err)
	}

	result := c.Check(emptyHome)

	var foundCustom bool
	for _, p := range result.ConfigPaths {
		if p == customDir {
			foundCustom = true
		}
	}

	if !foundCustom {
		t.Errorf("Check() should include CODEX_HOME in config paths, got: %v", result.ConfigPaths)
	}
}
