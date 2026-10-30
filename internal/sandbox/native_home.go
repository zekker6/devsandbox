package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Native homes contain sandbox-written symlinks between launches. Initialize
// their directories through os.Root rather than following those links as host.
func (c *Config) ensureNativeHomeDirs(dirs []string) (retErr error) {
	if err := os.MkdirAll(c.SandboxRoot, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(c.SandboxRoot)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, root.Close()) }()
	if err := root.Mkdir("home", 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	info, err := root.Lstat("home")
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("seatbelt home must be a directory, not a symlink or file")
	}
	home, err := root.OpenRoot("home")
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, home.Close()) }()
	for _, dir := range dirs {
		rel, err := filepath.Rel(c.SandboxHome, dir)
		if err != nil || !filepath.IsLocal(rel) {
			return fmt.Errorf("seatbelt home path escapes its root: %s", dir)
		}
		if err := home.MkdirAll(rel, 0o700); err != nil {
			return fmt.Errorf("seatbelt initialize %s: %w", rel, err)
		}
	}
	return nil
}
