package main

import (
	"testing"
)

func TestGetHostIDs_Default(t *testing.T) {
	t.Setenv("HOST_UID", "")
	t.Setenv("HOST_GID", "")

	uid, gid := getHostIDs()
	if uid != 1000 {
		t.Errorf("expected default UID 1000, got %d", uid)
	}
	if gid != 1000 {
		t.Errorf("expected default GID 1000, got %d", gid)
	}
}

func TestGetHostIDs_Custom(t *testing.T) {
	t.Setenv("HOST_UID", "501")
	t.Setenv("HOST_GID", "20")

	uid, gid := getHostIDs()
	if uid != 501 {
		t.Errorf("expected UID 501, got %d", uid)
	}
	if gid != 20 {
		t.Errorf("expected GID 20, got %d", gid)
	}
}

func TestGetHostIDs_Invalid(t *testing.T) {
	t.Setenv("HOST_UID", "notanumber")
	t.Setenv("HOST_GID", "")

	uid, gid := getHostIDs()
	if uid != 1000 {
		t.Errorf("expected fallback UID 1000, got %d", uid)
	}
	if gid != 1000 {
		t.Errorf("expected fallback GID 1000, got %d", gid)
	}
}

func TestGetHostIDs_RejectsZeroUID(t *testing.T) {
	t.Setenv("HOST_UID", "0")
	t.Setenv("HOST_GID", "1000")
	uid, _ := getHostIDs()
	if uid == 0 {
		t.Error("UID 0 should be rejected (root)")
	}
}

func TestGetHostIDs_RejectsNegativeUID(t *testing.T) {
	t.Setenv("HOST_UID", "-1")
	t.Setenv("HOST_GID", "1000")
	uid, _ := getHostIDs()
	if uid < 1 {
		t.Error("negative UID should fall back to default")
	}
}

func TestGetHostIDs_RejectsZeroGID(t *testing.T) {
	t.Setenv("HOST_UID", "1000")
	t.Setenv("HOST_GID", "0")
	_, gid := getHostIDs()
	if gid == 0 {
		t.Error("GID 0 should be rejected (root)")
	}
}

func TestEnvInt(t *testing.T) {
	tests := []struct {
		name     string
		envVal   string
		fallback int
		expected int
	}{
		{"empty", "", 42, 42},
		{"valid", "100", 42, 100},
		{"invalid", "abc", 42, 42},
		{"zero", "0", 42, 0},
		{"negative", "-1", 42, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "TEST_ENV_INT_" + tt.name
			if tt.envVal != "" {
				t.Setenv(key, tt.envVal)
			}
			got := envInt(key, tt.fallback)
			if got != tt.expected {
				t.Errorf("envInt(%q, %d) = %d, want %d", tt.envVal, tt.fallback, got, tt.expected)
			}
		})
	}
}

// NOTE: Tests for isEnvFile and findEnvFiles were removed because .env hiding
// is now handled at container creation time via Docker volume mounts.
// See internal/isolator/docker_test.go for the equivalent coverage.

func TestEnvOr(t *testing.T) {
	t.Setenv("TEST_ENVOR_SET", "value")

	if got := envOr("TEST_ENVOR_SET", "default"); got != "value" {
		t.Errorf("expected 'value', got %q", got)
	}
	if got := envOr("TEST_ENVOR_UNSET", "default"); got != "default" {
		t.Errorf("expected 'default', got %q", got)
	}
}
