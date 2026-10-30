package fsutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// MiseToolInstalls contains a tool's immediate install entries, without version
// alias symlinks. Entries include version directories and tool metadata files.
type MiseToolInstalls struct {
	Name    string
	Entries []fs.DirEntry
}

// ReadMiseInstalls reads installs/<tool>/<version> without traversing version
// payloads or following tool and version symlinks. A missing installs root is
// empty. Unreadable tools are omitted and reported alongside successful results,
// allowing best-effort callers to retain the readable tools.
func ReadMiseInstalls(installs string) ([]MiseToolInstalls, error) {
	tools, err := os.ReadDir(installs)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read mise installs %s: %w", installs, err)
	}
	result := make([]MiseToolInstalls, 0, len(tools))
	var readErrors error
	for _, tool := range tools {
		if !tool.IsDir() {
			continue
		}
		path := filepath.Join(installs, tool.Name())
		entries, err := os.ReadDir(path)
		if err != nil {
			readErrors = errors.Join(readErrors, fmt.Errorf("read mise tool %s: %w", path, err))
			continue
		}
		kept := entries[:0]
		for _, entry := range entries {
			if entry.Type()&fs.ModeSymlink == 0 {
				kept = append(kept, entry)
			}
		}
		result = append(result, MiseToolInstalls{Name: tool.Name(), Entries: kept})
	}
	return result, readErrors
}
