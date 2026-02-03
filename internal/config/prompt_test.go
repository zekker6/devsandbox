// Package config provides configuration file support for devsandbox.
package config

import (
	"bytes"
	"strings"
	"testing"
)

func Test_isInteractive(t *testing.T) {
	// Note: Actual TTY detection requires a real terminal.
	// We test the function exists and handles non-TTY gracefully.
	// In tests, stdin is typically not a TTY.
	result := isInteractive()
	// We just verify it doesn't panic and returns a bool
	_ = result
}

func Test_promptTrust_Yes(t *testing.T) {
	input := strings.NewReader("y\n")
	output := &bytes.Buffer{}

	result, err := promptTrust(input, output, "/path/to/project", "[proxy]\nenabled = true\n", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result {
		t.Error("expected true for 'y' input")
	}
	if !strings.Contains(output.String(), "Local config found") {
		t.Error("expected prompt output")
	}
}

func Test_promptTrust_No(t *testing.T) {
	input := strings.NewReader("n\n")
	output := &bytes.Buffer{}

	result, err := promptTrust(input, output, "/path/to/project", "[proxy]\n", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result {
		t.Error("expected false for 'n' input")
	}
}

func Test_promptTrust_Default(t *testing.T) {
	input := strings.NewReader("\n")
	output := &bytes.Buffer{}

	result, err := promptTrust(input, output, "/path/to/project", "[proxy]\n", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result {
		t.Error("expected false for empty input (default N)")
	}
}

func Test_promptTrust_Changed(t *testing.T) {
	input := strings.NewReader("y\n")
	output := &bytes.Buffer{}

	_, err := promptTrust(input, output, "/path/to/project", "[proxy]\n", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output.String(), "Local config changed") {
		t.Error("expected 'changed' message for updated config")
	}
}

func Test_promptTrust_CaseInsensitive(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"YES\n", true},
		{"n\n", false},
		{"N\n", false},
		{"no\n", false},
		{"anything\n", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			input := strings.NewReader(tt.input)
			output := &bytes.Buffer{}

			result, _ := promptTrust(input, output, "/path", "[proxy]\n", false)
			if result != tt.want {
				t.Errorf("promptTrust with %q = %v, want %v", tt.input, result, tt.want)
			}
		})
	}
}
