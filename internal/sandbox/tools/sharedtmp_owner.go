package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"devsandbox/internal/fsutil"
)

// SharedTmpOwnerRoot holds host-owned records mapping temporary directory hashes
// back to sandbox homes, including homes under previously configured bases.
func SharedTmpOwnerRoot(homeDir string) string {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(homeDir, ".local", "state")
	}
	return filepath.Join(state, "devsandbox", "shared-tmp-owners")
}

func recordSharedTmpOwner(homeDir, sandboxHome string) error {
	if !filepath.IsAbs(sandboxHome) {
		return fmt.Errorf("shared tmp: owner home must be absolute: %q", sandboxHome)
	}
	root := SharedTmpOwnerRoot(homeDir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("shared tmp: create owner directory: %w", err)
	}
	if err := fsutil.WriteFileAtomic(filepath.Join(root, sharedTmpSessionID(sandboxHome)), []byte(sandboxHome), 0o600); err != nil {
		return fmt.Errorf("shared tmp: record owner: %w", err)
	}
	return nil
}

// A staged directory has already been detached under the sandbox's exclusive
// lock. Its name cannot be a sandbox-home hash, so age alone can reclaim it.
var stagedSharedTmpName = regexp.MustCompile(`^[a-f0-9]{12}\.removing-[0-9]+-[0-9]+$`)

func sharedTmpOwnerGone(homeDir, id string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(SharedTmpOwnerRoot(homeDir), id))
	if errors.Is(err, fs.ErrNotExist) {
		// Pre-tracking directories cannot be attributed by hash. Absence from
		// the current base does not prove absence from a previous base.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("shared tmp: read owner %s: %w", id, err)
	}
	home := string(data)
	if !filepath.IsAbs(home) || sharedTmpSessionID(home) != id {
		return false, fmt.Errorf("shared tmp: invalid owner record %s", id)
	}
	// Check the sandbox root, not home: a partial sandbox still owns its tmp.
	root := filepath.Dir(home)
	if _, err := os.Stat(root); !errors.Is(err, fs.ErrNotExist) {
		if err != nil {
			return false, fmt.Errorf("shared tmp: stat owner root: %w", err)
		}
		return false, nil
	}
	// An absent base may be an unmounted volume, not a removed sandbox.
	if _, err := os.Stat(filepath.Dir(root)); err != nil {
		return false, fmt.Errorf("shared tmp: stat owner base: %w", err)
	}
	return true, nil
}

// SweepSharedTmpOwners removes records only after their sandbox and both temp
// directories are gone. Keeping records while the sandbox exists also protects
// a launch between recording its owner and creating the temporary directory.
func SweepSharedTmpOwners(homeDir string) (int, error) {
	if homeDir == "" {
		return 0, errors.New("shared tmp: owner sweep needs the home directory")
	}
	root := SharedTmpOwnerRoot(homeDir)
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("shared tmp: list owners: %w", err)
	}
	removed := 0
	var errs []error
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue // WriteFileAtomic's in-progress temporary file.
		}
		gone, err := sharedTmpOwnerGone(homeDir, entry.Name())
		if err != nil || !gone {
			errs = append(errs, err)
			continue
		}
		absent, err := sharedTmpDirsAbsent(homeDir, entry.Name())
		if err != nil || !absent {
			errs = append(errs, err)
			continue
		}
		if err := os.Remove(filepath.Join(root, entry.Name())); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("shared tmp: remove owner: %w", err))
			}
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

func sharedTmpDirsAbsent(homeDir, id string) (bool, error) {
	for _, root := range []string{SharedTmpRoot(homeDir), filepath.Join(homeDir, legacySharedTmpRelPath)} {
		if _, err := os.Lstat(filepath.Join(root, id)); !errors.Is(err, fs.ErrNotExist) {
			if err != nil {
				return false, fmt.Errorf("shared tmp: stat directory: %w", err)
			}
			return false, nil
		}
	}
	return true, nil
}
