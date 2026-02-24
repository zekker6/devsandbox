package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestFindEnvFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create directory structure:
	// tmpDir/
	//   .env
	//   .env.local
	//   sub/
	//     .env
	//     deep/
	//       .env
	//   node_modules/
	//     .env  (should be skipped)
	//   other.txt

	_ = os.WriteFile(filepath.Join(tmpDir, ".env"), []byte(""), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, ".env.local"), []byte(""), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, "other.txt"), []byte(""), 0o644)

	_ = os.MkdirAll(filepath.Join(tmpDir, "sub", "deep"), 0o755)
	_ = os.WriteFile(filepath.Join(tmpDir, "sub", ".env"), []byte(""), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, "sub", "deep", ".env"), []byte(""), 0o644)

	_ = os.MkdirAll(filepath.Join(tmpDir, "node_modules"), 0o755)
	_ = os.WriteFile(filepath.Join(tmpDir, "node_modules", ".env"), []byte(""), 0o644)

	t.Run("depth 0 finds only root env files", func(t *testing.T) {
		files := FindEnvFiles(tmpDir, 0)
		// depth 0 = only files in the root dir itself
		// sub/ is depth 1, so it should be skipped
		for _, f := range files {
			rel, _ := filepath.Rel(tmpDir, f)
			if rel != ".env" && rel != ".env.local" {
				t.Errorf("unexpected file at depth 0: %s", rel)
			}
		}
		if len(files) != 2 {
			t.Errorf("expected 2 files at depth 0, got %d: %v", len(files), files)
		}
	})

	t.Run("depth 1 includes sub dir", func(t *testing.T) {
		files := FindEnvFiles(tmpDir, 1)
		expected := []string{
			filepath.Join(tmpDir, ".env"),
			filepath.Join(tmpDir, ".env.local"),
			filepath.Join(tmpDir, "sub", ".env"),
		}
		if len(files) != len(expected) {
			t.Errorf("expected %d files at depth 1, got %d: %v", len(expected), len(files), files)
		}
		for _, e := range expected {
			if !slices.Contains(files, e) {
				t.Errorf("expected %s in results", e)
			}
		}
	})

	t.Run("depth 2 includes deep dir", func(t *testing.T) {
		files := FindEnvFiles(tmpDir, 2)
		if len(files) != 4 {
			t.Errorf("expected 4 files at depth 2, got %d: %v", len(files), files)
		}
	})

	t.Run("skips node_modules", func(t *testing.T) {
		files := FindEnvFiles(tmpDir, 10)
		for _, f := range files {
			if filepath.Base(filepath.Dir(f)) == "node_modules" {
				t.Errorf("should not include files from node_modules: %s", f)
			}
		}
	})

	t.Run("trailing slash on dir does not affect depth", func(t *testing.T) {
		files := FindEnvFiles(tmpDir+"/", 1)
		expected := []string{
			filepath.Join(tmpDir, ".env"),
			filepath.Join(tmpDir, ".env.local"),
			filepath.Join(tmpDir, "sub", ".env"),
		}
		if len(files) != len(expected) {
			t.Errorf("expected %d files with trailing slash dir at depth 1, got %d: %v",
				len(expected), len(files), files)
		}
	})
}
