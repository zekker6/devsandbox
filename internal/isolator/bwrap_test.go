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

func TestBwrapIsolator_IsolationType(t *testing.T) {
	iso := NewBwrapIsolator()
	if iso.IsolationType() != "bwrap" {
		t.Errorf("IsolationType() = %s, want bwrap", iso.IsolationType())
	}
}

func TestBwrapIsolator_PrepareNetwork(t *testing.T) {
	iso := NewBwrapIsolator()
	info, err := iso.PrepareNetwork(context.Background(), "/tmp/test")
	if err != nil {
		t.Fatalf("PrepareNetwork() error: %v", err)
	}
	if info != nil {
		t.Error("PrepareNetwork() should return nil for bwrap")
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
