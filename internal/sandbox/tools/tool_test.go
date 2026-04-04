package tools

import "testing"

func TestBindingCategory_Values(t *testing.T) {
	// Verify all category constants are distinct and non-empty
	categories := []BindingCategory{
		CategoryConfig, CategoryCache, CategoryData, CategoryState, CategoryRuntime,
	}
	seen := make(map[BindingCategory]bool)
	for _, c := range categories {
		if c == "" {
			t.Errorf("category should not be empty")
		}
		if seen[c] {
			t.Errorf("duplicate category: %s", c)
		}
		seen[c] = true
	}
}

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
