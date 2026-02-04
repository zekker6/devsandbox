package isolator

import (
	"runtime"
	"testing"
)

func TestDetect_Auto_Linux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only test")
	}
	backend, err := Detect(BackendAuto)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	if backend != BackendBwrap {
		t.Errorf("Expected bwrap on Linux, got %s", backend)
	}
}

func TestDetect_Explicit(t *testing.T) {
	tests := []struct {
		requested Backend
		expected  Backend
	}{
		{BackendBwrap, BackendBwrap},
		{BackendDocker, BackendDocker},
	}
	for _, tt := range tests {
		backend, err := Detect(tt.requested)
		if err != nil {
			t.Fatalf("Detect(%s) failed: %v", tt.requested, err)
		}
		if backend != tt.expected {
			t.Errorf("Detect(%s) = %s, want %s", tt.requested, backend, tt.expected)
		}
	}
}

func TestDetect_UnknownBackend(t *testing.T) {
	_, err := Detect(Backend("unknown"))
	if err == nil {
		t.Error("Detect with unknown backend should return error")
	}
}

func TestNew_UnknownBackend(t *testing.T) {
	_, err := New(Backend("unknown"), DockerConfig{})
	if err == nil {
		t.Error("New with unknown backend should return error")
	}
}
