package isolator

import (
	"fmt"
	"os"
	"path/filepath"

	"devsandbox/internal/fsutil"
	"devsandbox/internal/sandbox/tools"
)

// nativeLinkBinding replaces a readonly copy or link with a host-backed link.
// The caller must deny writes to canonicalSource in the Seatbelt policy. All
// destination operations stay confined to home, even through hostile ancestors.
func nativeLinkBinding(home *os.Root, rel, canonicalSource string) error {
	if !filepath.IsLocal(rel) || filepath.Clean(rel) == "." {
		return fmt.Errorf("seatbelt invalid link destination %q", rel)
	}
	if !filepath.IsAbs(canonicalSource) {
		return fmt.Errorf("seatbelt link source must be absolute: %q", canonicalSource)
	}
	if err := home.MkdirAll(filepath.Dir(rel), 0o700); err != nil {
		return fmt.Errorf("seatbelt create link parent %s: %w", rel, err)
	}
	if err := home.RemoveAll(rel); err != nil {
		return fmt.Errorf("seatbelt replace readonly entry %s: %w", rel, err)
	}
	if err := home.Symlink(canonicalSource, rel); err != nil {
		return fmt.Errorf("seatbelt link %s: %w", rel, err)
	}
	return nil
}

// nativeMiseInstallBindings enumerates host version directories for split mode.
// It does not inspect or alter the private home: the caller must retain existing
// private version directories and preserve these descendants during parent copies.
// Alias symlinks and metadata are left to the private data-root seed.
func nativeMiseInstallBindings(homeDir string) ([]tools.Binding, error) {
	installs := filepath.Join(homeDir, ".local", "share", "mise", "installs")
	installed, err := fsutil.ReadMiseInstalls(installs)
	if err != nil {
		return nil, err
	}
	var bindings []tools.Binding
	for _, tool := range installed {
		for _, entry := range tool.Entries {
			if !entry.IsDir() {
				continue
			}
			source := filepath.Join(installs, tool.Name, entry.Name())
			bindings = append(bindings, tools.Binding{
				Source: source, Dest: source, HomeRelativeDest: true,
				Category: tools.CategoryData, Type: tools.MountBind, ReadOnly: true,
			})
		}
	}
	return bindings, nil
}
