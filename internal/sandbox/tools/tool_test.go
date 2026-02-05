package tools

import "testing"

func TestCacheMount_FullPath(t *testing.T) {
	cm := CacheMount{
		Name:   "mise",
		EnvVar: "MISE_DATA_DIR",
	}

	path := cm.FullPath()
	if path != "/cache/mise" {
		t.Errorf("FullPath() = %q, want %q", path, "/cache/mise")
	}
}

func TestCacheMount_FullPath_Nested(t *testing.T) {
	cm := CacheMount{
		Name:   "go/mod",
		EnvVar: "GOMODCACHE",
	}

	path := cm.FullPath()
	if path != "/cache/go/mod" {
		t.Errorf("FullPath() = %q, want %q", path, "/cache/go/mod")
	}
}
