package tools

import "testing"

func TestGo_CacheMounts(t *testing.T) {
	g := &Go{}

	mounts := g.CacheMounts()

	if len(mounts) != 2 {
		t.Fatalf("CacheMounts() returned %d mounts, want 2", len(mounts))
	}

	// Check go mod cache
	if mounts[0].Name != "go/mod" {
		t.Errorf("mounts[0].Name = %q, want %q", mounts[0].Name, "go/mod")
	}
	if mounts[0].EnvVar != "GOMODCACHE" {
		t.Errorf("mounts[0].EnvVar = %q, want %q", mounts[0].EnvVar, "GOMODCACHE")
	}

	// Check go build cache
	if mounts[1].Name != "go/build" {
		t.Errorf("mounts[1].Name = %q, want %q", mounts[1].Name, "go/build")
	}
	if mounts[1].EnvVar != "GOCACHE" {
		t.Errorf("mounts[1].EnvVar = %q, want %q", mounts[1].EnvVar, "GOCACHE")
	}
}

func TestGo_ImplementsToolWithCache(t *testing.T) {
	var _ ToolWithCache = (*Go)(nil)
}
