package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateOverlayDirs(t *testing.T) {
	tmpDir := t.TempDir()
	sandboxHome := filepath.Join(tmpDir, "sandbox")

	upper, work, err := createOverlayDirs(sandboxHome, "/home/user/.local/share/mise", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check directories were created
	if _, err := os.Stat(upper); err != nil {
		t.Errorf("upper dir not created: %v", err)
	}
	if _, err := os.Stat(work); err != nil {
		t.Errorf("work dir not created: %v", err)
	}

	// Check path structure
	expectedBase := filepath.Join(sandboxHome, "overlay", "home_user_.local_share_mise")
	if upper != filepath.Join(expectedBase, "upper") {
		t.Errorf("unexpected upper path: %s", upper)
	}
	if work != filepath.Join(expectedBase, "work") {
		t.Errorf("unexpected work path: %s", work)
	}
}

func TestCreateOverlayDirs_WithSubdir(t *testing.T) {
	tmpDir := t.TempDir()
	sandboxHome := filepath.Join(tmpDir, "sandbox")

	upper, work, err := createOverlayDirs(sandboxHome, "/opt/tools", "custom")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedBase := filepath.Join(sandboxHome, "overlay", "custom", "opt_tools")
	if upper != filepath.Join(expectedBase, "upper") {
		t.Errorf("unexpected upper path: %s", upper)
	}
	if work != filepath.Join(expectedBase, "work") {
		t.Errorf("unexpected work path: %s", work)
	}
}

func TestCreateOverlayDirs_InvalidPath(t *testing.T) {
	tmpDir := t.TempDir()
	sandboxHome := filepath.Join(tmpDir, "sandbox")

	// Relative paths should be rejected
	_, _, err := createOverlayDirs(sandboxHome, "relative/path", "")
	if err == nil {
		t.Error("expected error for relative path")
	}

	// Relative path with traversal should be rejected (not absolute)
	_, _, err = createOverlayDirs(sandboxHome, "../../../etc/passwd", "")
	if err == nil {
		t.Error("expected error for relative path with traversal")
	}
}

func TestCreateOverlayDirs_PathTraversalResolved(t *testing.T) {
	// Absolute paths with .. are resolved by filepath.Clean to valid paths
	// This is expected behavior - /path/with/../traversal becomes /path/traversal
	tmpDir := t.TempDir()
	sandboxHome := filepath.Join(tmpDir, "sandbox")

	upper, work, err := createOverlayDirs(sandboxHome, "/path/with/../traversal", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Clean resolves /path/with/../traversal to /path/traversal
	expectedBase := filepath.Join(sandboxHome, "overlay", "path_traversal")
	if upper != filepath.Join(expectedBase, "upper") {
		t.Errorf("unexpected upper path: %s, expected: %s", upper, filepath.Join(expectedBase, "upper"))
	}
	if work != filepath.Join(expectedBase, "work") {
		t.Errorf("unexpected work path: %s", work)
	}
}

func TestCreateOverlayDirs_DoubleDotInFilename(t *testing.T) {
	// Filenames containing ".." as a substring (e.g., "..cache") are valid
	// and should not be rejected by path traversal checks.
	tmpDir := t.TempDir()
	sandboxHome := filepath.Join(tmpDir, "sandbox")

	_, _, err := createOverlayDirs(sandboxHome, "/var/log/..cache", "")
	if err != nil {
		t.Errorf("path with '..' in filename should be allowed, got: %v", err)
	}

	_, _, err = createOverlayDirs(sandboxHome, "/opt/..hidden-dir/data", "")
	if err != nil {
		t.Errorf("path with '..' prefix in dirname should be allowed, got: %v", err)
	}
}
