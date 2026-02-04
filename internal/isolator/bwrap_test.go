package isolator

import (
	"context"
	"os/exec"
	"runtime"
	"testing"
)

func TestBwrapIsolator_Name(t *testing.T) {
	iso := NewBwrapIsolator()
	if iso.Name() != BackendBwrap {
		t.Errorf("Name() = %s, want %s", iso.Name(), BackendBwrap)
	}
}

func TestBwrapIsolator_Available(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only test")
	}
	iso := NewBwrapIsolator()
	err := iso.Available()

	// Check if bwrap is actually installed
	_, lookErr := exec.LookPath("bwrap")
	if lookErr != nil {
		if err == nil {
			t.Error("Available() should return error when bwrap not installed")
		}
	} else {
		if err != nil {
			t.Errorf("Available() returned error but bwrap is installed: %v", err)
		}
	}
}

func TestBwrapIsolator_Build(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only test")
	}

	// Check if bwrap is installed
	_, lookErr := exec.LookPath("bwrap")
	if lookErr != nil {
		t.Skip("bwrap not installed, skipping Build test")
	}

	iso := NewBwrapIsolator()
	cfg := &Config{
		ProjectDir:  "/tmp/test",
		SandboxHome: "/tmp/sandbox-home",
		HomeDir:     "/home/test",
		Shell:       "bash",
		ShellPath:   "/bin/bash",
	}

	path, args, err := iso.Build(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	if path == "" {
		t.Error("Build() returned empty path")
	}

	// Args should be nil for now (thin wrapper)
	if args != nil {
		t.Errorf("Build() args = %v, want nil", args)
	}
}

func TestBwrapIsolator_Cleanup(t *testing.T) {
	iso := NewBwrapIsolator()
	err := iso.Cleanup()
	if err != nil {
		t.Errorf("Cleanup() error: %v", err)
	}
}

func TestBwrapIsolator_ImplementsInterface(t *testing.T) {
	var _ Isolator = (*BwrapIsolator)(nil)
}
