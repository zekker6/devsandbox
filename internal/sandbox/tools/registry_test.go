package tools

import "testing"

func TestCollectCacheMounts(t *testing.T) {
	// This tests that CollectCacheMounts returns mounts from tools
	// that implement ToolWithCache
	mounts := CollectCacheMounts("/home/testuser")

	// Should have at least mise and go mounts (4 total)
	if len(mounts) < 4 {
		t.Errorf("CollectCacheMounts() returned %d mounts, want at least 4", len(mounts))
	}

	// Check that mise mounts are present
	foundMise := false
	foundGoMod := false
	for _, m := range mounts {
		if m.EnvVar == "MISE_DATA_DIR" {
			foundMise = true
		}
		if m.EnvVar == "GOMODCACHE" {
			foundGoMod = true
		}
	}

	if !foundMise {
		t.Error("CollectCacheMounts() missing MISE_DATA_DIR")
	}
	if !foundGoMod {
		t.Error("CollectCacheMounts() missing GOMODCACHE")
	}
}
