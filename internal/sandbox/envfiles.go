package sandbox

import (
	"os"
	"path/filepath"
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
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return filepath.SkipDir
			}
			if rel != "." {
				// Count path components: "a/b/c" has depth 3
				depth := 1
				for i := range len(rel) {
					if rel[i] == filepath.Separator {
						depth++
					}
				}
				if depth > maxDepth {
					return filepath.SkipDir
				}
			}
			return nil
		}
		name := d.Name()
		if name == ".env" || len(name) > 5 && name[:5] == ".env." {
			files = append(files, path)
		}
		return nil
	})
	return files
}
