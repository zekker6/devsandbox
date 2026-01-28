package sandbox

import "testing"

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
