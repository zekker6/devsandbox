package tools

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestClaude_AgentSessionDirMatchesProjectsBinding pins the invariant the herdr
// filter's path confinement rests on: the bound it is given and the directory
// Claude's sessions are actually persisted in are the same path. If these ever
// disagree, every real session report is denied.
func TestClaude_AgentSessionDirMatchesProjectsBinding(t *testing.T) {
	const homeDir = "/home/test"

	tests := []struct {
		name      string
		configDir string
		want      string
	}{
		{name: "default", configDir: "", want: filepath.Join(homeDir, ".claude", "projects")},
		{name: "CLAUDE_CONFIG_DIR", configDir: "/etc/claude-config", want: "/etc/claude-config/projects"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", tt.configDir)

			c := &Claude{}
			got := c.AgentSessionDir(homeDir)
			if got != tt.want {
				t.Errorf("AgentSessionDir = %q, want %q", got, tt.want)
			}

			found := false
			for _, b := range c.Bindings(homeDir, "/tmp/sandbox") {
				if b.Source == got {
					found = true
					if b.Category != CategoryData {
						t.Errorf("binding for %q has category %q, want %q", got, b.Category, CategoryData)
					}
				}
			}
			if !found {
				t.Errorf("AgentSessionDir %q is not among Claude's bindings", got)
			}
		})
	}
}

func TestClaude_Bindings_DefaultPaths(t *testing.T) {
	// When CLAUDE_CONFIG_DIR is not set, default bindings should include ~/.claude
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	c := &Claude{}
	bindings := c.Bindings("/home/test", "/tmp/sandbox")

	foundDotClaude := false
	foundProjects := false
	foundClaudeJSON := false
	for _, b := range bindings {
		if b.Source == "/home/test/.claude" {
			foundDotClaude = true
			if b.Category != CategoryConfig {
				t.Errorf("~/.claude binding: expected category %q, got %q", CategoryConfig, b.Category)
			}
		}
		if b.Source == "/home/test/.claude/projects" {
			foundProjects = true
			if b.Category != CategoryData {
				t.Errorf("~/.claude/projects binding: expected category %q, got %q", CategoryData, b.Category)
			}
		}
		if b.Source == "/home/test/.claude.json" {
			foundClaudeJSON = true
		}
	}

	if !foundDotClaude {
		t.Error("expected ~/.claude binding when CLAUDE_CONFIG_DIR is not set")
	}
	if !foundProjects {
		t.Error("expected ~/.claude/projects binding for persistent session history")
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
	foundProjects := false
	foundDotClaude := false
	foundClaudeJSON := false
	foundClaudeJSONBackup := false
	for _, b := range bindings {
		if b.Source == customDir {
			foundCustom = true
			if b.Category != CategoryConfig {
				t.Errorf("custom config dir binding: expected category %q, got %q", CategoryConfig, b.Category)
			}
			if !b.Optional {
				t.Error("custom config dir binding should be optional")
			}
		}
		if b.Source == filepath.Join(customDir, "projects") {
			foundProjects = true
			if b.Category != CategoryData {
				t.Errorf("custom config dir projects binding: expected category %q, got %q", CategoryData, b.Category)
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
	if !foundProjects {
		t.Error("expected projects subdirectory binding for persistent session history")
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

// TestClaude_Bindings_LocalShareClaudeReadOnly verifies ~/.local/share/claude
// is mounted as a read-only bind, not a writable persistent overlay. Claude
// Code's in-sandbox auto-updater writes binaries to versions/<X> under this
// path; when those writes go to a persistent overlay upper-dir, partial or
// failed updates leave 0-byte shadow files that break execution across
// sessions (the real host binary is masked by the stub in the upper layer).
// Read-only keeps the host's installation as the single source of truth.
func TestClaude_Bindings_LocalShareClaudeReadOnly(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	c := &Claude{}
	bindings := c.Bindings("/home/test", "/tmp/sandbox")

	var localShareClaude *Binding
	for i := range bindings {
		if bindings[i].Source == "/home/test/.local/share/claude" {
			localShareClaude = &bindings[i]
			break
		}
	}

	if localShareClaude == nil {
		t.Fatal("Bindings() missing ~/.local/share/claude")
	}
	if localShareClaude.Type != MountBind {
		t.Errorf("~/.local/share/claude Type = %q, want %q", localShareClaude.Type, MountBind)
	}
	if !localShareClaude.ReadOnly {
		t.Error("~/.local/share/claude must be ReadOnly to prevent overlay shadowing from failed in-sandbox updates")
	}
	if localShareClaude.Category != "" {
		t.Errorf("~/.local/share/claude Category should be empty (explicit Type takes precedence), got %q", localShareClaude.Category)
	}
	if !localShareClaude.Optional {
		t.Error("~/.local/share/claude should be Optional (user may not have claude installed)")
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

// The projects binding is Optional, and the builder skips an Optional overlay
// whose host source is missing while ~/.claude itself stays a tmpoverlay - so
// without this the transcripts of a host-authenticated, sandbox-only user are
// discarded at exit and a captured herdr session resolves to nothing.
func TestClaude_SetupCreatesProjectsDirUnderAnExistingClaudeHome(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	homeDir := t.TempDir()
	claudeHome := filepath.Join(homeDir, ".claude")
	if err := os.MkdirAll(claudeHome, 0o700); err != nil {
		t.Fatalf("create claude home: %v", err)
	}

	c := &Claude{}
	projects := c.AgentSessionDir(homeDir)
	if _, err := os.Stat(projects); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("precondition: projects dir must not exist yet, stat err = %v", err)
	}

	if err := c.Setup(homeDir, "/tmp/sandbox"); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	info, err := os.Stat(projects)
	if err != nil {
		t.Fatalf("Setup did not create %s: %v", projects, err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", projects)
	}

	found := false
	for _, b := range c.Bindings(homeDir, "/tmp/sandbox") {
		if b.Source == projects {
			found = true
			if b.Category != CategoryData {
				t.Errorf("projects binding category = %q, want %q", b.Category, CategoryData)
			}
		}
	}
	if !found {
		t.Errorf("no binding for %q", projects)
	}
}

// Nothing is created when the Claude home is absent: no tmpoverlay is mounted
// there in that case, so in-sandbox writes already persist and creating host
// state for a tool the user never configured would be a side effect.
func TestClaude_SetupCreatesNothingWithoutAClaudeHome(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	homeDir := t.TempDir()
	c := &Claude{}
	if err := c.Setup(homeDir, "/tmp/sandbox"); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(homeDir, ".claude")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Setup created ~/.claude, stat err = %v", err)
	}
}

// CLAUDE_CONFIG_DIR must move Setup with the bindings: creating projects/ under
// ~/.claude while the overlay is declared under the custom dir would leave the
// real source missing and the overlay skipped.
func TestClaude_SetupHonorsClaudeConfigDir(t *testing.T) {
	homeDir := t.TempDir()
	custom := filepath.Join(t.TempDir(), "claude-config")
	if err := os.MkdirAll(custom, 0o700); err != nil {
		t.Fatalf("create custom config dir: %v", err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", custom)

	c := &Claude{}
	if err := c.Setup(homeDir, "/tmp/sandbox"); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(custom, "projects")); err != nil {
		t.Fatalf("Setup did not create projects under CLAUDE_CONFIG_DIR: %v", err)
	}
	if _, err := os.Stat(filepath.Join(homeDir, ".claude")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Setup touched ~/.claude despite CLAUDE_CONFIG_DIR, stat err = %v", err)
	}
}
