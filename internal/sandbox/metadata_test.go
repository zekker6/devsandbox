package sandbox

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveAndLoadMetadata(t *testing.T) {
	tmpDir := t.TempDir()

	original := &Metadata{
		Name:       "test-project",
		ProjectDir: "/home/user/projects/test",
		CreatedAt:  time.Now().Truncate(time.Second),
		LastUsed:   time.Now().Truncate(time.Second),
		Shell:      ShellFish,
	}

	if err := SaveMetadata(original, tmpDir); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	loaded, err := LoadMetadata(tmpDir)
	if err != nil {
		t.Fatalf("LoadMetadata failed: %v", err)
	}

	if loaded.Name != original.Name {
		t.Errorf("Name mismatch: got %s, want %s", loaded.Name, original.Name)
	}
	if loaded.ProjectDir != original.ProjectDir {
		t.Errorf("ProjectDir mismatch: got %s, want %s", loaded.ProjectDir, original.ProjectDir)
	}
	if !loaded.CreatedAt.Equal(original.CreatedAt) {
		t.Errorf("CreatedAt mismatch: got %v, want %v", loaded.CreatedAt, original.CreatedAt)
	}
	if loaded.Shell != original.Shell {
		t.Errorf("Shell mismatch: got %s, want %s", loaded.Shell, original.Shell)
	}
	if loaded.SandboxRoot != tmpDir {
		t.Errorf("SandboxRoot not set: got %s, want %s", loaded.SandboxRoot, tmpDir)
	}
}

func TestLoadMetadata_OrphanedProject(t *testing.T) {
	tmpDir := t.TempDir()

	m := &Metadata{
		Name:       "orphaned",
		ProjectDir: "/nonexistent/path/that/does/not/exist",
		CreatedAt:  time.Now(),
		LastUsed:   time.Now(),
		Shell:      ShellBash,
	}

	if err := SaveMetadata(m, tmpDir); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	loaded, err := LoadMetadata(tmpDir)
	if err != nil {
		t.Fatalf("LoadMetadata failed: %v", err)
	}

	if !loaded.Orphaned {
		t.Error("Expected Orphaned to be true for nonexistent project dir")
	}
}

func TestListSandboxes(t *testing.T) {
	tmpDir := t.TempDir()

	// Create some sandbox directories with metadata
	for _, name := range []string{"project-a", "project-b", "project-c"} {
		sandboxRoot := filepath.Join(tmpDir, name)
		if err := os.MkdirAll(sandboxRoot, 0o755); err != nil {
			t.Fatal(err)
		}

		m := &Metadata{
			Name:       name,
			ProjectDir: "/tmp/" + name,
			CreatedAt:  time.Now(),
			LastUsed:   time.Now(),
			Shell:      ShellZsh,
		}
		if err := SaveMetadata(m, sandboxRoot); err != nil {
			t.Fatal(err)
		}
	}

	// Create one without metadata
	noMetaDir := filepath.Join(tmpDir, "no-metadata")
	if err := os.MkdirAll(noMetaDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sandboxes, err := ListSandboxes(tmpDir)
	if err != nil {
		t.Fatalf("ListSandboxes failed: %v", err)
	}

	if len(sandboxes) != 4 {
		t.Errorf("Expected 4 sandboxes, got %d", len(sandboxes))
	}

	// Check that the one without metadata was handled
	var foundNoMeta bool
	for _, s := range sandboxes {
		if s.Name == "no-metadata" {
			foundNoMeta = true
			if s.ProjectDir != "(unknown)" {
				t.Errorf("Expected (unknown) project dir for sandbox without metadata")
			}
			if !s.Orphaned {
				t.Error("Expected sandbox without metadata to be marked orphaned")
			}
		}
	}
	if !foundNoMeta {
		t.Error("Sandbox without metadata not found in list")
	}
}

func TestListSandboxes_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()

	sandboxes, err := ListSandboxes(tmpDir)
	if err != nil {
		t.Fatalf("ListSandboxes failed: %v", err)
	}

	if len(sandboxes) != 0 {
		t.Errorf("Expected 0 sandboxes, got %d", len(sandboxes))
	}
}

func TestListSandboxes_NonexistentDir(t *testing.T) {
	sandboxes, err := ListSandboxes("/nonexistent/path")
	if err != nil {
		t.Fatalf("ListSandboxes should not fail for nonexistent dir: %v", err)
	}

	if sandboxes != nil {
		t.Error("Expected nil for nonexistent directory")
	}
}

func TestGetSandboxSize(t *testing.T) {
	tmpDir := t.TempDir()

	// Create some files
	if err := os.WriteFile(filepath.Join(tmpDir, "file1.txt"), make([]byte, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "file2.txt"), make([]byte, 500), 0o644); err != nil {
		t.Fatal(err)
	}

	size, err := GetSandboxSize(tmpDir)
	if err != nil {
		t.Fatalf("GetSandboxSize failed: %v", err)
	}

	if size != 1500 {
		t.Errorf("Expected size 1500, got %d", size)
	}
}

func TestSortSandboxes(t *testing.T) {
	now := time.Now()
	sandboxes := []*Metadata{
		{Name: "charlie", CreatedAt: now.Add(-1 * time.Hour), LastUsed: now.Add(-2 * time.Hour), SizeBytes: 300},
		{Name: "alpha", CreatedAt: now.Add(-3 * time.Hour), LastUsed: now.Add(-1 * time.Hour), SizeBytes: 100},
		{Name: "bravo", CreatedAt: now.Add(-2 * time.Hour), LastUsed: now.Add(-3 * time.Hour), SizeBytes: 200},
	}

	// Sort by name
	SortSandboxes(sandboxes, SortByName)
	if sandboxes[0].Name != "alpha" || sandboxes[1].Name != "bravo" || sandboxes[2].Name != "charlie" {
		t.Error("SortByName failed")
	}

	// Sort by created
	SortSandboxes(sandboxes, SortByCreated)
	if sandboxes[0].Name != "alpha" { // oldest created first
		t.Error("SortByCreated failed")
	}

	// Sort by used
	SortSandboxes(sandboxes, SortByUsed)
	if sandboxes[0].Name != "bravo" { // oldest used first
		t.Error("SortByUsed failed")
	}

	// Sort by size
	SortSandboxes(sandboxes, SortBySize)
	if sandboxes[0].Name != "alpha" { // smallest first
		t.Error("SortBySize failed")
	}
}

func TestSelectForPruning_All(t *testing.T) {
	sandboxes := []*Metadata{
		{Name: "a"},
		{Name: "b"},
		{Name: "c"},
	}

	toPrune := SelectForPruning(sandboxes, PruneOptions{All: true})
	if len(toPrune) != 3 {
		t.Errorf("Expected 3 to prune with --all, got %d", len(toPrune))
	}
}

func TestSelectForPruning_KeepN(t *testing.T) {
	now := time.Now()
	sandboxes := []*Metadata{
		{Name: "oldest", LastUsed: now.Add(-3 * time.Hour)},
		{Name: "middle", LastUsed: now.Add(-2 * time.Hour)},
		{Name: "newest", LastUsed: now.Add(-1 * time.Hour)},
	}

	toPrune := SelectForPruning(sandboxes, PruneOptions{Keep: 2})
	if len(toPrune) != 1 {
		t.Errorf("Expected 1 to prune with --keep 2, got %d", len(toPrune))
	}
	if toPrune[0].Name != "oldest" {
		t.Errorf("Expected oldest to be pruned, got %s", toPrune[0].Name)
	}
}

func TestSelectForPruning_OlderThan(t *testing.T) {
	now := time.Now()
	sandboxes := []*Metadata{
		{Name: "old", LastUsed: now.Add(-48 * time.Hour)},
		{Name: "recent", LastUsed: now.Add(-1 * time.Hour)},
	}

	toPrune := SelectForPruning(sandboxes, PruneOptions{OlderThan: 24 * time.Hour})
	if len(toPrune) != 1 {
		t.Errorf("Expected 1 to prune with --older-than 24h, got %d", len(toPrune))
	}
	if toPrune[0].Name != "old" {
		t.Errorf("Expected 'old' to be pruned, got %s", toPrune[0].Name)
	}
}

func TestSelectForPruning_OrphanedOnly(t *testing.T) {
	sandboxes := []*Metadata{
		{Name: "active", Orphaned: false},
		{Name: "orphaned", Orphaned: true},
	}

	// No --all, --keep, or --older-than means only orphaned
	toPrune := SelectForPruning(sandboxes, PruneOptions{})
	if len(toPrune) != 1 {
		t.Errorf("Expected 1 orphaned to prune, got %d", len(toPrune))
	}
	if toPrune[0].Name != "orphaned" {
		t.Errorf("Expected orphaned to be pruned, got %s", toPrune[0].Name)
	}
}

func TestFormatSize(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{500, "500 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1572864, "1.5 MB"},
		{1073741824, "1.0 GB"},
	}

	for _, tt := range tests {
		result := FormatSize(tt.bytes)
		if result != tt.expected {
			t.Errorf("FormatSize(%d) = %s, want %s", tt.bytes, result, tt.expected)
		}
	}
}
