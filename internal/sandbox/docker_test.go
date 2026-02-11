package sandbox

import (
	"os/exec"
	"testing"
)

func TestListDockerSandboxes_NoDocker(t *testing.T) {
	// Save original PATH and restore after test
	// This test verifies behavior when docker is not available

	// If docker is not installed, should return empty, not error
	_, err := exec.LookPath("docker")
	if err != nil {
		sandboxes, err := ListDockerSandboxes()
		if err != nil {
			t.Errorf("ListDockerSandboxes() should not error when docker not installed: %v", err)
		}
		if len(sandboxes) > 0 {
			t.Error("ListDockerSandboxes() should return empty when docker not installed")
		}
	}
}

func TestParseLabels_Empty(t *testing.T) {
	labels := parseLabels("")
	if len(labels) != 0 {
		t.Errorf("empty input should produce empty map, got %v", labels)
	}
}

func TestParseLabels_Basic(t *testing.T) {
	labels := parseLabels("devsandbox=true,devsandbox.project_name=myproject")
	if labels["devsandbox"] != "true" {
		t.Error("expected devsandbox=true")
	}
	if labels["devsandbox.project_name"] != "myproject" {
		t.Error("expected devsandbox.project_name=myproject")
	}
}

func TestParseLabels_TrimSpaces(t *testing.T) {
	labels := parseLabels("key1 = val1 , key2 = val2")
	if labels["key1"] != "val1" {
		t.Errorf("expected trimmed key1=val1, got %q", labels["key1"])
	}
	if labels["key2"] != "val2" {
		t.Errorf("expected trimmed key2=val2, got %q", labels["key2"])
	}
}

func TestRemoveSandboxByType_Bwrap(t *testing.T) {
	// Test that bwrap sandboxes use RemoveSandbox
	m := &Metadata{
		Isolation:   IsolationBwrap,
		SandboxRoot: "/nonexistent/path",
	}

	// Should not panic, just fail gracefully
	err := RemoveSandboxByType(m, false)
	// Error is expected since path doesn't exist, but should not panic
	_ = err
}

func TestRemoveSandboxByType_Docker(t *testing.T) {
	// Test that docker sandboxes use RemoveDockerContainer
	m := &Metadata{
		Isolation:   IsolationDocker,
		SandboxRoot: "nonexistent-container",
	}

	// Skip if docker is not available
	_, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Docker not installed")
	}

	// Should not panic, just fail (container doesn't exist)
	err = RemoveSandboxByType(m, false)
	// Error is expected since container doesn't exist
	if err == nil {
		t.Error("RemoveSandboxByType() should error for non-existent docker container")
	}
}

func TestRemoveContainerVolumes_NonExistent(t *testing.T) {
	// Skip if docker is not available
	_, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Docker not installed")
	}

	// Should handle non-existent container gracefully (no volumes to remove)
	err = removeContainerVolumes("nonexistent-container-xyz")
	if err != nil {
		t.Errorf("removeContainerVolumes() should handle non-existent container gracefully: %v", err)
	}
}

func TestGetDockerVolumeSizes_NoDocker(t *testing.T) {
	_, err := exec.LookPath("docker")
	if err != nil {
		sizes := GetDockerVolumeSizes()
		if len(sizes) != 0 {
			t.Errorf("GetDockerVolumeSizes() should return empty map when docker not installed, got %v", sizes)
		}
	}
}

func TestParseDockerSize(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
	}{
		{"1.5GB", 1500000000},
		{"250MB", 250000000},
		{"0B", 0},
		{"45.2kB", 45200},
		{"1TB", 1000000000000},
		{"100B", 100},
		{"", 0},
		{"invalid", 0},
		{"GB", 0},
		{"1.5", 0},
		{"abc123", 0},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := parseDockerSize(tt.input)
			if result != tt.expected {
				t.Errorf("parseDockerSize(%q) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

func TestGetContainerVolumes_NonExistent(t *testing.T) {
	// Skip if docker is not available
	_, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Docker not installed")
	}

	// Non-existent container should return nil
	volumes := GetContainerVolumes("nonexistent-container-xyz")
	if len(volumes) != 0 {
		t.Errorf("GetContainerVolumes() should return empty for non-existent container, got %v", volumes)
	}
}
