package main

import (
	"fmt"
	"os"
	"path/filepath"
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

func TestLoadOverlayManifest_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "overlays.json")
	data := `{"overlays":[{"path":"/home/sandboxuser/.config/fish","type":"tmpoverlay"}]}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create the target directory so it passes the isDir check
	if err := os.MkdirAll("/home/sandboxuser/.config/fish", 0o755); err != nil {
		t.Skipf("cannot create test directory: %v", err)
	}

	entries := loadOverlayManifest(path)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Path != "/home/sandboxuser/.config/fish" {
		t.Errorf("path = %q, want /home/sandboxuser/.config/fish", entries[0].Path)
	}
	if entries[0].Type != "tmpoverlay" {
		t.Errorf("type = %q, want tmpoverlay", entries[0].Type)
	}
}

func TestLoadOverlayManifest_NotExists(t *testing.T) {
	entries := loadOverlayManifest("/nonexistent/path.json")
	if len(entries) != 0 {
		t.Errorf("expected empty slice for missing file, got %d entries", len(entries))
	}
}

func TestLoadOverlayManifest_Malformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(path, []byte("{invalid"), 0o644)

	// Test the parsing separately since fatal calls os.Exit
	var m overlayManifest
	err := parseOverlayManifest([]byte("{invalid"), &m)
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestLoadOverlayManifest_CopyOverlay(t *testing.T) {
	dir := t.TempDir()

	// Create target and source directories
	targetDir := filepath.Join(dir, "target")
	sourceDir := filepath.Join(dir, "source")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "overlays.json")
	data := fmt.Sprintf(`{"overlays":[{"path":"%s","type":"copyoverlay","source":"%s"}]}`,
		targetDir, sourceDir)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	entries := loadOverlayManifest(path)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Type != "copyoverlay" {
		t.Errorf("type = %q, want copyoverlay", entries[0].Type)
	}
	if entries[0].Source != sourceDir {
		t.Errorf("source = %q, want %q", entries[0].Source, sourceDir)
	}
}

func TestLoadOverlayManifest_SkipsFiles(t *testing.T) {
	dir := t.TempDir()

	// Create a directory entry and a file entry
	dirPath := filepath.Join(dir, "fishdir")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(dir, "config.fish")
	if err := os.WriteFile(filePath, []byte("# fish"), 0o644); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "overlays.json")
	data := fmt.Sprintf(`{"overlays":[{"path":"%s","type":"tmpoverlay"},{"path":"%s","type":"tmpoverlay"}]}`,
		dirPath, filePath)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	entries := loadOverlayManifest(path)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (dir only), got %d", len(entries))
	}
	if entries[0].Path != dirPath {
		t.Errorf("expected dir path %s, got %s", dirPath, entries[0].Path)
	}
}
