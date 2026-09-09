package isolator

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"devsandbox/internal/fsutil"
)

// OverlayManifestPath is the container-side path where the manifest is mounted.
const OverlayManifestPath = "/tmp/.devsandbox-overlays.json"

// OverlayManifest carries mount and private-file setup instructions for the shim.
type OverlayManifest struct {
	Overlays []OverlayEntry    `json:"overlays"`
	Seeds    []fsutil.FileSeed `json:"seeds,omitempty"`
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
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	// Kept containers pin this inode. It must have been private from creation;
	// tightening a public inode cannot revoke readers that already opened it.
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		_ = f.Close()
		return fmt.Errorf("refusing to write private setup data to %s: remove the non-private manifest and recreate the container", path)
	}
	if err := f.Truncate(0); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
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
