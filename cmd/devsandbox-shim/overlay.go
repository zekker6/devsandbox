package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const overlayLayersBase = "/tmp/.devsandbox-overlay-layers"

// prepareOverlayDirs creates upper and work directories for an overlay mount.
// Uses a hash of the target path to produce deterministic, unique dir names.
func prepareOverlayDirs(base, target string) (upper, work string, err error) {
	h := sha256.Sum256([]byte(target))
	name := hex.EncodeToString(h[:])[:16]

	upper = filepath.Join(base, name, "upper")
	work = filepath.Join(base, name, "work")

	if err := os.MkdirAll(upper, 0o755); err != nil {
		return "", "", fmt.Errorf("create overlay upper dir: %w", err)
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", "", fmt.Errorf("create overlay work dir: %w", err)
	}
	return upper, work, nil
}

// buildOverlayMountOptions builds the data string for mount(2) with overlay type.
func buildOverlayMountOptions(lowerdir, upperdir, workdir string) string {
	return "lowerdir=" + lowerdir + ",upperdir=" + upperdir + ",workdir=" + workdir
}

// splitOverlaySpec splits a "target:upper:work" spec. Uses last two colons
// to handle paths that may contain colons (unlikely but defensive).
func splitOverlaySpec(spec string) []string {
	last := len(spec) - 1
	secondColon := -1
	firstColon := -1
	for i := last; i >= 0; i-- {
		if spec[i] == ':' {
			if secondColon == -1 {
				secondColon = i
			} else {
				firstColon = i
				break
			}
		}
	}
	if firstColon == -1 || secondColon == -1 {
		return nil
	}
	return []string{spec[:firstColon], spec[firstColon+1 : secondColon], spec[secondColon+1:]}
}

func writeFile(path, content string) {
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		fatal("write %s: %v", path, err)
	}
}

// copyDir recursively copies src directory contents into dst.
// Preserves file permissions and symlinks. dst must already exist.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(dst, relPath)

		// Handle symlinks before IsDir check (symlinks to dirs would match)
		if d.Type()&fs.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("readlink %s: %w", path, err)
			}
			return os.Symlink(link, targetPath)
		}

		if d.IsDir() {
			return os.MkdirAll(targetPath, 0o755)
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		return os.WriteFile(targetPath, data, info.Mode().Perm())
	})
}
