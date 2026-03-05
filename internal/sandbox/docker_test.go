package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestDockerContainerName(t *testing.T) {
	name := DockerContainerName("/home/user/projects/myapp")
	if name == "" {
		t.Fatal("DockerContainerName() returned empty string")
	}
	// Must start with devsandbox- prefix
	if name[:len("devsandbox-")] != "devsandbox-" {
		t.Errorf("expected devsandbox- prefix, got %q", name)
	}
	// Must contain the project basename
	if !strings.Contains(name, "myapp") {
		t.Errorf("expected container name to contain 'myapp', got %q", name)
	}
	// Same input must produce same output
	name2 := DockerContainerName("/home/user/projects/myapp")
	if name != name2 {
		t.Errorf("expected deterministic output, got %q and %q", name, name2)
	}
	// Different input must produce different output
	name3 := DockerContainerName("/home/user/projects/other")
	if name == name3 {
		t.Errorf("expected different names for different projects, both got %q", name)
	}
}

func TestRemoveSandboxByType_DockerWithFilesystemPath(t *testing.T) {
	// Skip if docker is not available
	_, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Docker not installed")
	}

	// Create a temp directory to simulate a disk-listed Docker sandbox
	tmpDir := t.TempDir()
	sandboxDir := filepath.Join(tmpDir, "devsandbox-test123")
	if err := os.MkdirAll(sandboxDir, 0o755); err != nil {
		t.Fatal(err)
	}

	m := &Metadata{
		Isolation:   IsolationDocker,
		SandboxRoot: sandboxDir, // Filesystem path, not container name
		ProjectDir:  "/nonexistent/project",
	}

	// Should NOT fail — the Docker container doesn't exist but the disk
	// directory should still be cleaned up.
	err = RemoveSandboxByType(m, false)
	if err != nil {
		t.Errorf("RemoveSandboxByType() should succeed cleaning up disk dir even when container is gone: %v", err)
	}

	// Verify the disk directory was removed
	if _, err := os.Stat(sandboxDir); !os.IsNotExist(err) {
		t.Error("expected disk directory to be removed")
	}
}

func TestRemoveSandboxByType_DockerWithUnknownProjectDir(t *testing.T) {
	// Create a temp directory to simulate a disk-listed Docker sandbox
	// with unknown project dir
	tmpDir := t.TempDir()
	sandboxDir := filepath.Join(tmpDir, "devsandbox-unknown")
	if err := os.MkdirAll(sandboxDir, 0o755); err != nil {
		t.Fatal(err)
	}

	m := &Metadata{
		Isolation:   IsolationDocker,
		SandboxRoot: sandboxDir,
		ProjectDir:  "(unknown)",
	}

	// Should clean up disk dir without trying Docker container removal
	err := RemoveSandboxByType(m, false)
	if err != nil {
		t.Errorf("RemoveSandboxByType() should clean up disk dir for unknown project: %v", err)
	}

	if _, err := os.Stat(sandboxDir); !os.IsNotExist(err) {
		t.Error("expected disk directory to be removed")
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
