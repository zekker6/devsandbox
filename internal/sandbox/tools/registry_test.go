package tools

import "testing"

func TestCollectCacheMounts(t *testing.T) {
	// CollectCacheMounts depends on host tool availability (exec.LookPath),
	// so we only assert structural invariants rather than specific counts.
	mounts := CollectCacheMounts()

	for _, m := range mounts {
		if m.Name == "" {
			t.Error("CacheMount has empty Name")
		}
		if m.EnvVar == "" {
			t.Error("CacheMount has empty EnvVar")
		}
		if m.FullPath() == "" {
			t.Error("CacheMount has empty FullPath()")
		}
	}
}

func TestAllReturnsRegisteredTools(t *testing.T) {
	tools := All()
	if len(tools) == 0 {
		t.Fatal("All() returned no tools, expected at least one registered tool")
	}

	// Verify sorted order
	for i := 1; i < len(tools); i++ {
		if tools[i].Name() < tools[i-1].Name() {
			t.Errorf("All() not sorted: %s comes after %s", tools[i].Name(), tools[i-1].Name())
		}
	}
}
