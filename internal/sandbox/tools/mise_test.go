package tools

import (
	"strings"
	"testing"
)

func TestMise_CacheMounts(t *testing.T) {
	m := &Mise{}

	mounts := m.CacheMounts()

	if len(mounts) != 2 {
		t.Fatalf("CacheMounts() returned %d mounts, want 2", len(mounts))
	}

	// Check mise data dir
	if mounts[0].Name != "mise" {
		t.Errorf("mounts[0].Name = %q, want %q", mounts[0].Name, "mise")
	}
	if mounts[0].EnvVar != "MISE_DATA_DIR" {
		t.Errorf("mounts[0].EnvVar = %q, want %q", mounts[0].EnvVar, "MISE_DATA_DIR")
	}

	// Check mise cache dir
	if mounts[1].Name != "mise/cache" {
		t.Errorf("mounts[1].Name = %q, want %q", mounts[1].Name, "mise/cache")
	}
	if mounts[1].EnvVar != "MISE_CACHE_DIR" {
		t.Errorf("mounts[1].EnvVar = %q, want %q", mounts[1].EnvVar, "MISE_CACHE_DIR")
	}
}

func TestMise_ImplementsToolWithCache(t *testing.T) {
	var _ ToolWithCache = (*Mise)(nil)
}

func TestMise_DockerBindings_NoCacheDirs(t *testing.T) {
	m := &Mise{}

	mounts := m.DockerBindings("/home/testuser", "/tmp/sandbox")

	// Should only have config and bin mounts, NOT data/cache/state
	for _, mount := range mounts {
		if strings.Contains(mount.Dest, "share/mise") {
			t.Errorf("DockerBindings() should not mount share/mise (use CacheMounts): %s", mount.Dest)
		}
		if strings.Contains(mount.Dest, "cache/mise") {
			t.Errorf("DockerBindings() should not mount cache/mise (use CacheMounts): %s", mount.Dest)
		}
	}

	// Should have config mount
	foundConfig := false
	foundBin := false
	for _, mount := range mounts {
		if strings.Contains(mount.Dest, ".config/mise") {
			foundConfig = true
		}
		if strings.Contains(mount.Dest, ".local/bin") {
			foundBin = true
		}
	}

	if !foundConfig {
		t.Error("DockerBindings() missing .config/mise mount")
	}
	if !foundBin {
		t.Error("DockerBindings() missing .local/bin mount")
	}
}
