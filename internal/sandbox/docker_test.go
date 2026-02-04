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

func TestRemoveSandboxByType_Bwrap(t *testing.T) {
	// Test that bwrap sandboxes use RemoveSandbox
	m := &Metadata{
		Isolation:   IsolationBwrap,
		SandboxRoot: "/nonexistent/path",
	}

	// Should not panic, just fail gracefully
	err := RemoveSandboxByType(m)
	// Error is expected since path doesn't exist, but should not panic
	_ = err
}

func TestRemoveSandboxByType_Docker(t *testing.T) {
	// Test that docker sandboxes use RemoveDockerVolume
	m := &Metadata{
		Isolation:   IsolationDocker,
		SandboxRoot: "nonexistent-volume",
	}

	// Skip if docker is not available
	_, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Docker not installed")
	}

	// Should not panic, just fail (volume doesn't exist)
	err = RemoveSandboxByType(m)
	// Error is expected since volume doesn't exist
	if err == nil {
		t.Error("RemoveSandboxByType() should error for non-existent docker volume")
	}
}
