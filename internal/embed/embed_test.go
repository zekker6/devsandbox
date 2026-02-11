package embed

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBwrapCacheDir(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	dir, err := BwrapCacheDir()
	if err != nil {
		t.Fatalf("BwrapCacheDir() error: %v", err)
	}

	expected := filepath.Join(tmpDir, "devsandbox", "bin", "bwrap-"+BwrapVersion)
	if dir != expected {
		t.Errorf("BwrapCacheDir() = %s, want %s", dir, expected)
	}
}

func TestPastaCacheDir(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	dir, err := PastaCacheDir()
	if err != nil {
		t.Fatalf("PastaCacheDir() error: %v", err)
	}

	expected := filepath.Join(tmpDir, "devsandbox", "bin", "pasta-"+PastaVersion)
	if dir != expected {
		t.Errorf("PastaCacheDir() = %s, want %s", dir, expected)
	}
}

func TestCacheDirDefaultsToHome(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")

	dir, err := BwrapCacheDir()
	if err != nil {
		t.Fatalf("BwrapCacheDir() error: %v", err)
	}

	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".cache", "devsandbox", "bin", "bwrap-"+BwrapVersion)
	if dir != expected {
		t.Errorf("BwrapCacheDir() = %s, want %s", dir, expected)
	}
}

func TestExtractBinary(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	dir, err := BwrapCacheDir()
	if err != nil {
		t.Fatalf("BwrapCacheDir() error: %v", err)
	}

	path, err := extractBinary("bwrap", dir)
	if err != nil {
		t.Fatalf("extractBinary(bwrap) error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("extracted binary not found at %s: %v", path, err)
	}

	if info.Mode()&0o111 == 0 {
		t.Errorf("extracted binary is not executable: mode %v", info.Mode())
	}

	if filepath.Dir(path) != dir {
		t.Errorf("extracted to %s, expected dir %s", path, dir)
	}
}

func TestExtractBinaryCaches(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	dir, err := PastaCacheDir()
	if err != nil {
		t.Fatalf("PastaCacheDir() error: %v", err)
	}

	path1, err := extractBinary("pasta", dir)
	if err != nil {
		t.Fatalf("first extraction error: %v", err)
	}

	path2, err := extractBinary("pasta", dir)
	if err != nil {
		t.Fatalf("second extraction error: %v", err)
	}

	if path1 != path2 {
		t.Errorf("paths differ: %s vs %s", path1, path2)
	}
}

func TestExtractBinaryInvalidName(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	dir, err := BwrapCacheDir()
	if err != nil {
		t.Fatalf("BwrapCacheDir() error: %v", err)
	}

	_, err = extractBinary("nonexistent", dir)
	if err == nil {
		t.Error("expected error for nonexistent binary")
	}
}

func TestExtractBinaryCorrectArch(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	dir, err := BwrapCacheDir()
	if err != nil {
		t.Fatalf("BwrapCacheDir() error: %v", err)
	}

	path, err := extractBinary("bwrap", dir)
	if err != nil {
		t.Fatalf("extractBinary error: %v", err)
	}

	extracted, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read extracted binary: %v", err)
	}

	embedded, err := binFS.ReadFile("bin/" + runtime.GOARCH + "/bwrap")
	if err != nil {
		t.Fatalf("cannot read embedded binary: %v", err)
	}

	if string(extracted) != string(embedded) {
		t.Error("extracted content does not match embedded content")
	}
}

func TestIsEmbedded(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	dir, err := BwrapCacheDir()
	if err != nil {
		t.Fatalf("BwrapCacheDir() error: %v", err)
	}

	path, err := extractBinary("bwrap", dir)
	if err != nil {
		t.Fatalf("extractBinary error: %v", err)
	}

	if !IsEmbedded(path) {
		t.Errorf("IsEmbedded(%s) = false, want true", path)
	}

	if IsEmbedded("/usr/bin/bwrap") {
		t.Error("IsEmbedded(/usr/bin/bwrap) = true, want false")
	}
}
