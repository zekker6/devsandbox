package sandbox

import (
	"os"
	"path/filepath"
	"strings"
)

// FindEnvFiles walks dir up to maxDepth and returns paths to .env files.
// Skips common large directories that won't contain env files.
func FindEnvFiles(dir string, maxDepth int) []string {
	var files []string
	skipDirs := map[string]bool{
		"node_modules": true,
		".git":         true,
		"vendor":       true,
		".venv":        true,
	}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			depth := strings.Count(strings.TrimPrefix(path, dir), string(os.PathSeparator))
			if depth > maxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if name == ".env" || strings.HasPrefix(name, ".env.") {
			files = append(files, path)
		}
		return nil
	})
	return files
}
