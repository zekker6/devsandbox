package tools

import "testing"

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
