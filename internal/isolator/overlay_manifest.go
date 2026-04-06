package isolator

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// OverlayManifestPath is the container-side path where the manifest is mounted.
const OverlayManifestPath = "/tmp/.devsandbox-overlays.json"

// OverlayManifest lists paths that the shim should mount as overlayfs.
type OverlayManifest struct {
	Overlays []OverlayEntry `json:"overlays"`
}

// OverlayEntry describes a single overlay mount.
type OverlayEntry struct {
	// Path is the container-side mount point (must be a directory).
	Path string `json:"path"`
	// Type is the overlay type: "tmpoverlay" (overlayfs) or "copyoverlay" (copy-based fallback).
	Type string `json:"type"`
	// Source is the container-side shadow path for copyoverlay entries.
	Source string `json:"source,omitempty"`
}

// Write serializes the manifest to a file.
func (m *OverlayManifest) Write(path string) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal overlay manifest: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// ReadOverlayManifest reads and parses a manifest file.
// Returns (nil, nil) if the file does not exist (backwards compatibility).
// Returns an error if the file exists but is malformed.
func ReadOverlayManifest(path string) (*OverlayManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read overlay manifest: %w", err)
	}
	var m OverlayManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse overlay manifest %s: %w", path, err)
	}
	return &m, nil
}
