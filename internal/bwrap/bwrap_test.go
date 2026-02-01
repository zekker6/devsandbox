package bwrap

import (
	"os/exec"
	"testing"
)

func TestCheckInstalled(t *testing.T) {
	err := CheckInstalled()

	// Check if bwrap is actually installed
	_, lookupErr := exec.LookPath("bwrap")

	if lookupErr != nil {
		// bwrap not installed, should return error
		if err == nil {
			t.Error("expected error when bwrap is not installed")
		}
	} else {
		// bwrap installed, should not return error
		if err != nil {
			t.Errorf("unexpected error when bwrap is installed: %v", err)
		}
	}
}

func TestPastaSupportsMapHostLoopback(t *testing.T) {
	// This is a functional test that depends on pasta being installed
	_, err := exec.LookPath("pasta")
	if err != nil {
		t.Skip("pasta not installed, skipping test")
	}

	// Just verify it doesn't panic and returns a boolean
	result := pastaSupportsMapHostLoopback()
	t.Logf("pastaSupportsMapHostLoopback() = %v", result)
}
