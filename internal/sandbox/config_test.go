package sandbox

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestNewConfig_XDGRuntime(t *testing.T) {
	// When XDG_RUNTIME_DIR is unset, it should use /run/user/<uid> with numeric UID
	t.Setenv("XDG_RUNTIME_DIR", "")
	cfg, err := NewConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	uid := os.Getuid()
	expected := filepath.Join("/run/user", strconv.Itoa(uid))
	if cfg.XDGRuntime != expected {
		t.Errorf("XDGRuntime = %q, want %q", cfg.XDGRuntime, expected)
	}
}

func TestSanitizeProjectName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple name",
			input:    "myproject",
			expected: "myproject",
		},
		{
			name:     "with spaces",
			input:    "my project",
			expected: "my_project",
		},
		{
			name:     "with special chars",
			input:    "my@project#123",
			expected: "my_project_123",
		},
		{
			name:     "preserves dots",
			input:    "my.project",
			expected: "my.project",
		},
		{
			name:     "preserves hyphens",
			input:    "my-project",
			expected: "my-project",
		},
		{
			name:     "preserves underscores",
			input:    "my_project",
			expected: "my_project",
		},
		{
			name:     "mixed special chars",
			input:    "my project@v1.0-beta_test",
			expected: "my_project_v1.0-beta_test",
		},
		{
			name:     "unicode chars",
			input:    "проект",
			expected: "______",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeProjectName(tt.input)
			if result != tt.expected {
				t.Errorf("SanitizeProjectName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestGenerateSandboxName(t *testing.T) {
	// Same directory name in different paths should produce different sandbox names
	name1 := GenerateSandboxName("/home/user/work/myproject")
	name2 := GenerateSandboxName("/home/user/personal/myproject")

	if name1 == name2 {
		t.Errorf("Different paths with same basename should have different names: %s == %s", name1, name2)
	}

	// Same path should always produce the same name
	name3 := GenerateSandboxName("/home/user/work/myproject")
	if name1 != name3 {
		t.Errorf("Same path should produce same name: %s != %s", name1, name3)
	}

	// Name should start with sanitized basename
	name := GenerateSandboxName("/home/user/my project")
	if name[:10] != "my_project" {
		t.Errorf("Name should start with sanitized basename: %s", name)
	}

	// Name should contain hash suffix
	if len(name) < 12 { // basename + "-" + 8 char hash
		t.Errorf("Name should include hash suffix: %s", name)
	}
}
