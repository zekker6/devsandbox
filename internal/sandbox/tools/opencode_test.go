package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCode_Bindings_NoEnvVar(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG_DIR", "")

	o := &OpenCode{}
	bindings := o.Bindings("/home/test", "/tmp/sandbox")

	expected := map[string]BindingCategory{
		"/home/test/.config/opencode":      CategoryConfig,
		"/home/test/.local/share/opencode": CategoryData,
		"/home/test/.cache/opencode":       CategoryCache,
		"/home/test/.cache/oh-my-opencode": CategoryCache,
	}

	seen := make(map[string]bool)
	for _, b := range bindings {
		if want, ok := expected[b.Source]; ok {
			seen[b.Source] = true
			if b.Category != want {
				t.Errorf("%s: want category %q, got %q", b.Source, want, b.Category)
			}
		}
	}

	for src := range expected {
		if !seen[src] {
			t.Errorf("missing default binding %s", src)
		}
	}
}

func TestOpenCode_Bindings_OpencodeConfigDir(t *testing.T) {
	customDir := "/etc/opencode-config"
	t.Setenv("OPENCODE_CONFIG_DIR", customDir)

	o := &OpenCode{}
	bindings := o.Bindings("/home/test", "/tmp/sandbox")

	var foundCustom, foundDefault bool
	for _, b := range bindings {
		switch b.Source {
		case customDir:
			foundCustom = true
			if b.Category != CategoryConfig {
				t.Errorf("OPENCODE_CONFIG_DIR binding: expected category %q, got %q", CategoryConfig, b.Category)
			}
			if !b.Optional {
				t.Error("OPENCODE_CONFIG_DIR binding should be optional")
			}
		case "/home/test/.config/opencode":
			foundDefault = true
		}
	}

	if !foundCustom {
		t.Error("expected OPENCODE_CONFIG_DIR binding when env var is set")
	}
	// Unlike codex/pi, opencode loads OPENCODE_CONFIG_DIR *in addition to*
	// the global ~/.config/opencode — both must remain mounted.
	if !foundDefault {
		t.Error("~/.config/opencode must still be mounted when OPENCODE_CONFIG_DIR is set (additive, not replacement)")
	}
}

func TestOpenCode_Environment_NoEnvVar(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG_DIR", "")

	o := &OpenCode{}
	envVars := o.Environment("/home/test", "/tmp/sandbox")

	for _, env := range envVars {
		if env.Name == "OPENCODE_CONFIG_DIR" {
			t.Error("OPENCODE_CONFIG_DIR should not be exported when host env var is empty")
		}
	}
}

func TestOpenCode_Environment_WithOpencodeConfigDir(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG_DIR", "/etc/opencode-config")

	o := &OpenCode{}
	envVars := o.Environment("/home/test", "/tmp/sandbox")

	var found bool
	for _, env := range envVars {
		if env.Name == "OPENCODE_CONFIG_DIR" {
			found = true
			if !env.FromHost {
				t.Error("OPENCODE_CONFIG_DIR should use FromHost to pass through the host value")
			}
		}
	}

	if !found {
		t.Error("expected OPENCODE_CONFIG_DIR env var when set on host")
	}
}

func TestOpenCode_Available_OpencodeConfigDir(t *testing.T) {
	tmpDir := t.TempDir()
	customDir := filepath.Join(tmpDir, "opencode-config")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("OPENCODE_CONFIG_DIR", customDir)

	o := &OpenCode{}
	emptyHome := filepath.Join(tmpDir, "empty-home")
	if err := os.MkdirAll(emptyHome, 0o755); err != nil {
		t.Fatal(err)
	}

	if !o.Available(emptyHome) {
		t.Error("OpenCode should be available when OPENCODE_CONFIG_DIR points to existing directory")
	}
}

func TestOpenCode_Check_OpencodeConfigDir(t *testing.T) {
	tmpDir := t.TempDir()
	customDir := filepath.Join(tmpDir, "opencode-config")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("OPENCODE_CONFIG_DIR", customDir)

	o := &OpenCode{}
	emptyHome := filepath.Join(tmpDir, "empty-home")
	if err := os.MkdirAll(emptyHome, 0o755); err != nil {
		t.Fatal(err)
	}

	result := o.Check(emptyHome)

	var foundCustom bool
	for _, p := range result.ConfigPaths {
		if p == customDir {
			foundCustom = true
		}
	}

	if !foundCustom {
		t.Errorf("Check() should include OPENCODE_CONFIG_DIR in config paths, got: %v", result.ConfigPaths)
	}
}
