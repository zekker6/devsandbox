// internal/config/trust_test.go
package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTrustStore_LoadEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "trusted-configs.toml")

	store, err := LoadTrustStore(storePath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.Trusted) != 0 {
		t.Error("expected empty trust store")
	}
}

func TestTrustStore_SaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "trusted-configs.toml")

	store, err := LoadTrustStore(storePath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	store.Trusted = []TrustedConfig{
		{
			Path:  "/home/user/project",
			Hash:  "abc123",
			Added: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		},
	}

	if err := store.Save(); err != nil {
		t.Fatalf("failed to save: %v", err)
	}

	loaded, err := LoadTrustStore(storePath)
	if err != nil {
		t.Fatalf("failed to load: %v", err)
	}

	if len(loaded.Trusted) != 1 {
		t.Fatalf("expected 1 trusted config, got %d", len(loaded.Trusted))
	}
	if loaded.Trusted[0].Path != "/home/user/project" {
		t.Error("path mismatch")
	}
	if loaded.Trusted[0].Hash != "abc123" {
		t.Error("hash mismatch")
	}
}

func TestTrustStore_IsTrusted(t *testing.T) {
	store := &TrustStore{
		Trusted: []TrustedConfig{
			{Path: "/home/user/project", Hash: "abc123"},
		},
	}

	// Exact match
	if !store.IsTrusted("/home/user/project", "abc123") {
		t.Error("expected trusted for matching path and hash")
	}

	// Wrong hash
	if store.IsTrusted("/home/user/project", "wronghash") {
		t.Error("expected not trusted for wrong hash")
	}

	// Wrong path
	if store.IsTrusted("/home/user/other", "abc123") {
		t.Error("expected not trusted for wrong path")
	}
}

func TestTrustStore_AddTrust(t *testing.T) {
	store := &TrustStore{}

	store.AddTrust("/home/user/project", "abc123")

	if len(store.Trusted) != 1 {
		t.Fatal("expected 1 trusted config")
	}
	if store.Trusted[0].Path != "/home/user/project" {
		t.Error("path mismatch")
	}
	if store.Trusted[0].Added.IsZero() {
		t.Error("expected Added to be set")
	}
}

func TestTrustStore_AddTrust_Update(t *testing.T) {
	store := &TrustStore{
		Trusted: []TrustedConfig{
			{Path: "/home/user/project", Hash: "oldhash"},
		},
	}

	store.AddTrust("/home/user/project", "newhash")

	if len(store.Trusted) != 1 {
		t.Fatal("expected 1 trusted config (update, not add)")
	}
	if store.Trusted[0].Hash != "newhash" {
		t.Error("expected hash to be updated")
	}
}

func TestTrustStore_RemoveTrust(t *testing.T) {
	store := &TrustStore{
		Trusted: []TrustedConfig{
			{Path: "/home/user/project1", Hash: "abc"},
			{Path: "/home/user/project2", Hash: "def"},
		},
	}

	removed := store.RemoveTrust("/home/user/project1")
	if !removed {
		t.Error("expected RemoveTrust to return true")
	}
	if len(store.Trusted) != 1 {
		t.Fatal("expected 1 trusted config after removal")
	}
	if store.Trusted[0].Path != "/home/user/project2" {
		t.Error("removed wrong config")
	}

	// Remove non-existent
	removed = store.RemoveTrust("/nonexistent")
	if removed {
		t.Error("expected false for non-existent path")
	}
}

func TestHashFile(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.toml")

	content := "[proxy]\nenabled = true\n"
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	hash, err := HashFile(testFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// SHA256 hash should be 64 hex characters
	if len(hash) != 64 {
		t.Errorf("expected 64 char hash, got %d", len(hash))
	}

	// Same content should produce same hash
	hash2, _ := HashFile(testFile)
	if hash != hash2 {
		t.Error("same file should produce same hash")
	}
}

func TestHashFile_NotFound(t *testing.T) {
	_, err := HashFile("/nonexistent/file.toml")
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}
