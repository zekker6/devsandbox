package fsutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// FileSeed carries a private file into a container home that is a named volume.
// Name is a basename inside that home, never a host-selected absolute path.
type FileSeed struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

// SeedFile initializes an absent or empty private copy. Existing non-empty
// files belong to the sandbox and win. A symlink is replaced, never followed;
// other non-regular entries are retained with a warning to avoid deleting work.
// read is deferred until a copy is needed, and a missing source is a no-op.
func SeedFile(dst string, read func() ([]byte, error), warn func(string, ...any)) (bool, error) {
	info, err := os.Lstat(dst)
	switch {
	case err == nil && info.Mode().IsRegular() && info.Size() > 0:
		return false, nil
	case err == nil && info.Mode()&os.ModeSymlink == 0 && !info.Mode().IsRegular():
		kind := info.Mode().String()
		if info.IsDir() {
			kind = "directory"
		}
		warn("%s is a %s, not a file; leaving it alone, so the sandbox will not find its state file", dst, kind)
		return false, nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return false, fmt.Errorf("stat %s: %w", dst, err)
	}

	data, err := read()
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := WriteFileAtomic(dst, data, 0o600); err != nil {
		return false, fmt.Errorf("seed %s: %w", dst, err)
	}
	if info != nil && info.Mode()&os.ModeSymlink != 0 {
		warn("replaced a symlink at %s with a private configuration copy", dst)
	}
	return true, nil
}
