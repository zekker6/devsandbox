package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClaude_Bindings_DefaultPaths(t *testing.T) {
	// When CLAUDE_CONFIG_DIR is not set, default bindings should include ~/.claude
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	c := &Claude{}
	bindings := c.Bindings("/home/test", "/tmp/sandbox")

	foundDotClaude := false
	foundClaudeJSON := false
	for _, b := range bindings {
		if b.Source == "/home/test/.claude" {
			foundDotClaude = true
		}
		if b.Source == "/home/test/.claude.json" {
			foundClaudeJSON = true
		}
	}

	if !foundDotClaude {
		t.Error("expected ~/.claude binding when CLAUDE_CONFIG_DIR is not set")
	}
	if !foundClaudeJSON {
		t.Error("expected ~/.claude.json binding when CLAUDE_CONFIG_DIR is not set")
	}
}

func TestClaude_Bindings_CustomConfigDir(t *testing.T) {
	customDir := "/etc/claude-config"
	t.Setenv("CLAUDE_CONFIG_DIR", customDir)

	c := &Claude{}
	bindings := c.Bindings("/home/test", "/tmp/sandbox")

	foundCustom := false
	foundDotClaude := false
	foundClaudeJSON := false
	foundClaudeJSONBackup := false
	for _, b := range bindings {
		if b.Source == customDir {
			foundCustom = true
			if b.ReadOnly {
				t.Error("custom config dir binding should be read-write")
			}
			if !b.Optional {
				t.Error("custom config dir binding should be optional")
			}
		}
		if b.Source == "/home/test/.claude" {
			foundDotClaude = true
		}
		if b.Source == "/home/test/.claude.json" {
			foundClaudeJSON = true
		}
		if b.Source == "/home/test/.claude.json.backup" {
			foundClaudeJSONBackup = true
		}
	}

	if !foundCustom {
		t.Error("expected CLAUDE_CONFIG_DIR binding when env var is set")
	}
	if foundDotClaude {
		t.Error("~/.claude should NOT be mounted when CLAUDE_CONFIG_DIR is set")
	}
	if foundClaudeJSON {
		t.Error("~/.claude.json should NOT be mounted when CLAUDE_CONFIG_DIR is set")
	}
	if foundClaudeJSONBackup {
		t.Error("~/.claude.json.backup should NOT be mounted when CLAUDE_CONFIG_DIR is set")
	}
}

func TestClaude_Bindings_CustomConfigDir_KeepsOtherBindings(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/etc/claude-config")

	c := &Claude{}
	bindings := c.Bindings("/home/test", "/tmp/sandbox")

	foundOptClaude := false
	foundConfigClaude := false
	foundCache := false
	foundLocalShare := false
	for _, b := range bindings {
		switch b.Source {
		case "/opt/claude-code":
			foundOptClaude = true
		case filepath.Join("/home/test", ".config", "Claude"):
			foundConfigClaude = true
		case filepath.Join("/home/test", ".cache", "claude-cli-nodejs"):
			foundCache = true
		case filepath.Join("/home/test", ".local", "share", "claude"):
			foundLocalShare = true
		}
	}

	if !foundOptClaude {
		t.Error("expected /opt/claude-code binding to remain")
	}
	if !foundConfigClaude {
		t.Error("expected ~/.config/Claude binding to remain")
	}
	if !foundCache {
		t.Error("expected ~/.cache/claude-cli-nodejs binding to remain")
	}
	if !foundLocalShare {
		t.Error("expected ~/.local/share/claude binding to remain")
	}
}

func TestClaude_Environment_NoConfigDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	c := &Claude{}
	envVars := c.Environment("/home/test", "/tmp/sandbox")

	for _, env := range envVars {
		if env.Name == "CLAUDE_CONFIG_DIR" {
			t.Error("CLAUDE_CONFIG_DIR should not be set when env var is empty")
		}
	}
}

func TestClaude_Environment_WithConfigDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/etc/claude-config")

	c := &Claude{}
	envVars := c.Environment("/home/test", "/tmp/sandbox")

	found := false
	for _, env := range envVars {
		if env.Name == "CLAUDE_CONFIG_DIR" {
			found = true
			if !env.FromHost {
				t.Error("CLAUDE_CONFIG_DIR should use FromHost to pass through the host value")
			}
		}
	}

	if !found {
		t.Error("expected CLAUDE_CONFIG_DIR env var when env var is set")
	}
}

func TestClaude_Available_CustomConfigDir(t *testing.T) {
	tmpDir := t.TempDir()
	customDir := filepath.Join(tmpDir, "claude-config")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLAUDE_CONFIG_DIR", customDir)

	c := &Claude{}
	// Use a homeDir with no claude files to isolate the test
	emptyHome := filepath.Join(tmpDir, "empty-home")
	if err := os.MkdirAll(emptyHome, 0o755); err != nil {
		t.Fatal(err)
	}

	if !c.Available(emptyHome) {
		t.Error("Claude should be available when CLAUDE_CONFIG_DIR points to existing directory")
	}
}

func TestClaude_Check_CustomConfigDir(t *testing.T) {
	tmpDir := t.TempDir()
	customDir := filepath.Join(tmpDir, "claude-config")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLAUDE_CONFIG_DIR", customDir)

	c := &Claude{}
	emptyHome := filepath.Join(tmpDir, "empty-home")
	if err := os.MkdirAll(emptyHome, 0o755); err != nil {
		t.Fatal(err)
	}

	result := c.Check(emptyHome)

	foundCustom := false
	for _, p := range result.ConfigPaths {
		if p == customDir {
			foundCustom = true
		}
	}

	if !foundCustom {
		t.Errorf("Check() should include CLAUDE_CONFIG_DIR in config paths, got: %v", result.ConfigPaths)
	}
}
