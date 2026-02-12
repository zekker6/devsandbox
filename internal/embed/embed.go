package embed

import (
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

//go:embed bin/amd64/* bin/arm64/*
var binFS embed.FS

// Disabled skips embedded binary extraction when true.
// When set, BwrapPath() and PastaPath() fall back directly to system binaries.
// Set this before the first call to BwrapPath() or PastaPath().
var Disabled bool

var (
	bwrapOnce sync.Once
	bwrapPath string
	bwrapErr  error

	pastaOnce sync.Once
	pastaPath string
	pastaErr  error
)

// cacheBase returns the XDG-compliant cache base directory.
// Respects XDG_CACHE_HOME, defaults to ~/.cache.
func cacheBase() (string, error) {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine home directory: %w", err)
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "devsandbox", "bin"), nil
}

// BwrapCacheDir returns the versioned cache directory for bwrap.
func BwrapCacheDir() (string, error) {
	base, err := cacheBase()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "bwrap-"+BwrapVersion), nil
}

// PastaCacheDir returns the versioned cache directory for pasta.
func PastaCacheDir() (string, error) {
	base, err := cacheBase()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "pasta-"+PastaVersion), nil
}

// extractBinary extracts a named binary from the embedded FS to the given cache directory.
// Returns the path to the extracted (or already cached) binary.
//
// This function is not safe for concurrent calls with the same binary name.
// Use BwrapPath() or PastaPath() for concurrent-safe access.
func extractBinary(name string, dir string) (string, error) {
	targetPath := filepath.Join(dir, name)

	// Return cached binary if it exists
	if info, err := os.Stat(targetPath); err == nil && !info.IsDir() {
		return targetPath, nil
	}

	// Read from embedded FS for current architecture
	arch := runtime.GOARCH
	data, err := binFS.ReadFile(fmt.Sprintf("bin/%s/%s", arch, name))
	if err != nil {
		return "", fmt.Errorf("embedded binary not found for %s/%s: %w", arch, name, err)
	}

	// Create cache directory
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("cannot create cache directory %s: %w", dir, err)
	}

	// Write to temp file then rename atomically to avoid races
	tmpFile, err := os.CreateTemp(dir, name+".tmp.*")
	if err != nil {
		return "", fmt.Errorf("cannot create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("cannot write binary: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("cannot close temp file: %w", err)
	}

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("cannot set executable permission: %w", err)
	}

	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("cannot move binary to cache: %w", err)
	}

	return targetPath, nil
}

// BwrapPath returns the path to the bwrap binary.
// Extracts the embedded binary on first call. Falls back to system binary if extraction fails.
// If Disabled is true, skips extraction and uses system binary only.
func BwrapPath() (string, error) {
	bwrapOnce.Do(func() {
		if !Disabled {
			dir, err := BwrapCacheDir()
			if err == nil {
				bwrapPath, bwrapErr = extractBinary("bwrap", dir)
			}
		}
		if bwrapErr != nil || Disabled {
			bwrapPath, bwrapErr = exec.LookPath("bwrap")
		}
	})
	return bwrapPath, bwrapErr
}

// PastaPath returns the path to the pasta binary.
// Extracts the embedded binary on first call. Falls back to system binary if extraction fails.
// If Disabled is true, skips extraction and uses system binary only.
// On amd64, also extracts pasta.avx2 alongside pasta (pasta exec's it at startup for better performance).
func PastaPath() (string, error) {
	pastaOnce.Do(func() {
		if !Disabled {
			dir, err := PastaCacheDir()
			if err == nil {
				pastaPath, pastaErr = extractBinary("pasta", dir)
				if pastaErr == nil && runtime.GOARCH == "amd64" {
					// Best-effort: pasta tries to exec pasta.avx2 from same dir at startup.
					// Ignore errors — pasta works fine without it.
					_, _ = extractBinary("pasta.avx2", dir)
				}
			}
		}
		if pastaErr != nil || Disabled {
			pastaPath, pastaErr = exec.LookPath("pasta")
		}
	})
	return pastaPath, pastaErr
}

// IsEmbedded returns true if the given path points to an extracted embedded binary.
func IsEmbedded(binPath string) bool {
	base, err := cacheBase()
	if err != nil {
		return false
	}
	dir := filepath.Dir(binPath)
	return filepath.Dir(dir) == base
}
