package mounts

import (
	"os"
	"path/filepath"
	"testing"

	"devsandbox/internal/config"
)

func TestNewEngine(t *testing.T) {
	homeDir := "/home/testuser"

	t.Run("empty config has no rules", func(t *testing.T) {
		cfg := config.MountsConfig{}
		engine := NewEngine(cfg, homeDir)

		rules := engine.Rules()
		if len(rules) != 0 {
			t.Errorf("expected 0 rules for empty config, got %d", len(rules))
		}
	})

	t.Run("custom rules are loaded", func(t *testing.T) {
		cfg := config.MountsConfig{
			Rules: []config.MountRule{
				{Pattern: "~/.config/myapp", Mode: "readonly"},
				{Pattern: "**/secrets/**", Mode: "hidden"},
				{Pattern: "~/.cache/app", Mode: "overlay"},
			},
		}
		engine := NewEngine(cfg, homeDir)

		rules := engine.Rules()
		if len(rules) != 3 {
			t.Errorf("expected 3 rules, got %d", len(rules))
		}
	})

	t.Run("modes are parsed correctly", func(t *testing.T) {
		cfg := config.MountsConfig{
			Rules: []config.MountRule{
				{Pattern: "/a", Mode: "hidden"},
				{Pattern: "/b", Mode: "readonly"},
				{Pattern: "/c", Mode: "readwrite"},
				{Pattern: "/d", Mode: "overlay"},
				{Pattern: "/e", Mode: "tmpoverlay"},
				{Pattern: "/f", Mode: ""}, // default
			},
		}
		engine := NewEngine(cfg, homeDir)

		expected := []Mode{ModeHidden, ModeReadOnly, ModeReadWrite, ModeOverlay, ModeTmpOverlay, ModeReadOnly}
		for i, rule := range engine.Rules() {
			if rule.Mode != expected[i] {
				t.Errorf("rule %d: expected mode %s, got %s", i, expected[i], rule.Mode)
			}
		}
	})
}

func TestExpandHome(t *testing.T) {
	homeDir := "/home/testuser"

	tests := []struct {
		input    string
		expected string
	}{
		{"~/.config", "/home/testuser/.config"},
		{"~/", "/home/testuser"},
		{"~", "/home/testuser"},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
		{"", ""},
		{"~other", "~other"}, // Not expanded (user ~other)
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := expandHome(tt.input, homeDir)
			if result != tt.expected {
				t.Errorf("expandHome(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestExpandedPaths(t *testing.T) {
	// Create temp directory structure
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	secretsDir := filepath.Join(projectDir, "secrets")
	configDir := filepath.Join(projectDir, "config")

	_ = os.MkdirAll(secretsDir, 0o755)
	_ = os.MkdirAll(configDir, 0o755)
	_ = os.WriteFile(filepath.Join(secretsDir, "api.key"), []byte("key"), 0o600)
	_ = os.WriteFile(filepath.Join(configDir, "app.json"), []byte("{}"), 0o644)

	cfg := config.MountsConfig{
		Rules: []config.MountRule{
			{Pattern: projectDir + "/secrets/**", Mode: "hidden"},
			{Pattern: projectDir + "/config/**", Mode: "readonly"},
		},
	}
	engine := NewEngine(cfg, tmpDir)

	paths := engine.ExpandedPaths()

	// Should have 2 directories (secrets/** and config/**)
	if len(paths) != 2 {
		t.Errorf("expected 2 paths, got %d: %v", len(paths), paths)
	}

	if rule, ok := paths[secretsDir]; !ok {
		t.Error("expected secrets dir to be in paths")
	} else if rule.Mode != ModeHidden {
		t.Errorf("expected secrets mode hidden, got %s", rule.Mode)
	}

	if rule, ok := paths[configDir]; !ok {
		t.Error("expected config dir to be in paths")
	} else if rule.Mode != ModeReadOnly {
		t.Errorf("expected config mode readonly, got %s", rule.Mode)
	}
}

func TestExpandedPaths_SingleFile(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.json")
	_ = os.WriteFile(configFile, []byte("{}"), 0o644)

	cfg := config.MountsConfig{
		Rules: []config.MountRule{
			{Pattern: configFile, Mode: "readonly"},
		},
	}
	engine := NewEngine(cfg, tmpDir)

	paths := engine.ExpandedPaths()

	if len(paths) != 1 {
		t.Errorf("expected 1 path, got %d: %v", len(paths), paths)
	}

	if rule, ok := paths[configFile]; !ok {
		t.Error("expected config file to be in paths")
	} else if rule.Mode != ModeReadOnly {
		t.Errorf("expected mode readonly, got %s", rule.Mode)
	}
}

func TestExpandedPaths_GlobMatchingFiles(t *testing.T) {
	tmpDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmpDir, "app.conf"), []byte(""), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, "db.conf"), []byte(""), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, "other.txt"), []byte(""), 0o644)

	cfg := config.MountsConfig{
		Rules: []config.MountRule{
			{Pattern: tmpDir + "/*.conf", Mode: "hidden"},
		},
	}
	engine := NewEngine(cfg, tmpDir)

	paths := engine.ExpandedPaths()

	if len(paths) != 2 {
		t.Errorf("expected 2 paths (*.conf files), got %d: %v", len(paths), paths)
	}

	for path, rule := range paths {
		if filepath.Ext(path) != ".conf" {
			t.Errorf("unexpected path %s matched", path)
		}
		if rule.Mode != ModeHidden {
			t.Errorf("expected mode hidden for %s, got %s", path, rule.Mode)
		}
	}
}

func TestExpandedPaths_NoMatches(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config.MountsConfig{
		Rules: []config.MountRule{
			{Pattern: tmpDir + "/nonexistent/**", Mode: "hidden"},
			{Pattern: tmpDir + "/*.xyz", Mode: "readonly"},
		},
	}
	engine := NewEngine(cfg, tmpDir)

	paths := engine.ExpandedPaths()

	if len(paths) != 0 {
		t.Errorf("expected 0 paths for non-matching patterns, got %d: %v", len(paths), paths)
	}
}

func TestExpandedPaths_FirstRuleWins(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.json")
	_ = os.WriteFile(configFile, []byte("{}"), 0o644)

	cfg := config.MountsConfig{
		Rules: []config.MountRule{
			{Pattern: configFile, Mode: "hidden"},
			{Pattern: configFile, Mode: "readonly"}, // Should be ignored
		},
	}
	engine := NewEngine(cfg, tmpDir)

	paths := engine.ExpandedPaths()

	if len(paths) != 1 {
		t.Errorf("expected 1 path, got %d", len(paths))
	}

	if rule := paths[configFile]; rule.Mode != ModeHidden {
		t.Errorf("expected first rule (hidden) to win, got %s", rule.Mode)
	}
}
