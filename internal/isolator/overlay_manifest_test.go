package isolator

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOverlayManifest_WriteAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "overlays.json")

	manifest := &OverlayManifest{
		Overlays: []OverlayEntry{
			{Path: "/home/sandboxuser/.config/fish", Type: "tmpoverlay"},
			{Path: "/home/sandboxuser/.config/mise", Type: "tmpoverlay"},
		},
	}

	if err := manifest.Write(path); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := ReadOverlayManifest(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(got.Overlays) != 2 {
		t.Fatalf("expected 2 overlays, got %d", len(got.Overlays))
	}
	if got.Overlays[0].Path != "/home/sandboxuser/.config/fish" {
		t.Errorf("overlay[0].Path = %q, want /home/sandboxuser/.config/fish", got.Overlays[0].Path)
	}
	if got.Overlays[1].Type != "tmpoverlay" {
		t.Errorf("overlay[1].Type = %q, want tmpoverlay", got.Overlays[1].Type)
	}
}

func TestReadOverlayManifest_NotExists(t *testing.T) {
	got, err := ReadOverlayManifest("/nonexistent/path.json")
	if err != nil {
		t.Fatalf("unexpected error for missing file: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil manifest for missing file, got %+v", got)
	}
}

func TestReadOverlayManifest_Malformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(path, []byte("{invalid"), 0o644)

	_, err := ReadOverlayManifest(path)
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestOverlayManifest_Empty(t *testing.T) {
	manifest := &OverlayManifest{}
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.json")

	if err := manifest.Write(path); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := ReadOverlayManifest(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got.Overlays) != 0 {
		t.Errorf("expected 0 overlays, got %d", len(got.Overlays))
	}
}
