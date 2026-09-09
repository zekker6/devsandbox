package isolator

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"devsandbox/internal/fsutil"
)

const maxHomeSeedBytes = 8 << 20

func collectHomeSeeds(home string, names []string) ([]fsutil.FileSeed, error) {
	if len(names) == 0 {
		return nil, nil
	}
	root, err := os.OpenRoot(home)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	var seeds []fsutil.FileSeed
	for _, name := range names {
		if name == "." || name == ".." || filepath.Base(name) != name {
			return nil, fmt.Errorf("invalid home seed filename %q", name)
		}
		data, err := readHomeSeed(root, name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read home seed %s: %w", name, err)
		}
		seeds = append(seeds, fsutil.FileSeed{Name: name, Data: data})
	}
	return seeds, nil
}

func readHomeSeed(root *os.Root, name string) ([]byte, error) {
	// A bound home is sandbox-writable. Refuse links and non-regular files,
	// without blocking on a FIFO, and bound reads of attacker-controlled data.
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxHomeSeedBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxHomeSeedBytes {
		return nil, fmt.Errorf("exceeds %d-byte home seed limit", maxHomeSeedBytes)
	}
	return data, nil
}
