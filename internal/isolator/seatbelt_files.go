package isolator

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"devsandbox/internal/cmdpattern"
	"devsandbox/internal/config"
	"devsandbox/internal/sandbox"
	"devsandbox/internal/sandbox/tools"
)

type nativeBinding struct {
	source     string
	rel        string
	persistent bool
	readonly   bool
	shared     bool
	install    bool
}

func (p *nativePlan) applyBindings(home *os.Root, cfg *sandbox.Config, bindings []tools.Binding) error {
	var planned []nativeBinding
	for _, b := range bindings {
		info, err := os.Stat(b.Source)
		if b.Optional && errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("seatbelt binding %s: %w", b.Source, err)
		}
		if len(b.OverlaySources) != 0 {
			return fmt.Errorf("seatbelt cannot merge overlay layers for %s", b.Source)
		}
		source, err := filepath.EvalSymlinks(b.Source)
		if err != nil {
			return err
		}
		dest := b.Dest
		if dest == "" {
			dest = b.Source
		}
		inHome := nativeWithin(cfg.HomeDir, dest) && (b.Dest == "" || b.HomeRelativeDest)
		if !inHome {
			if b.Source != dest || b.Type != tools.MountBind || !b.ReadOnly {
				return fmt.Errorf("seatbelt cannot remap %s to %s outside the private home", b.Source, dest)
			}
			p.policy.ReadOnly = append(p.policy.ReadOnly, source)
			p.policy.DenyWrite = append(p.policy.DenyWrite, source)
			continue
		}
		rel, err := filepath.Rel(cfg.HomeDir, dest)
		if err != nil || !filepath.IsLocal(rel) || rel == "." {
			return fmt.Errorf("seatbelt invalid home destination %s", dest)
		}
		if b.Type == tools.MountBind && !b.ReadOnly {
			return fmt.Errorf("seatbelt refuses a writable host binding for %s; use an overlay copy", b.Source)
		}
		readonly := b.ReadOnly || b.Type == tools.MountBind || !info.IsDir()
		// Home seed files are private state, unlike overlay-mounted config files.
		seed := p.seeds[b.Source]
		planned = append(planned, nativeBinding{
			source: source, rel: rel, persistent: b.Type == tools.MountOverlay,
			readonly: readonly && !seed,
			shared:   readonly && !seed && b.Category == tools.CategoryData && !nativeWithin(p.setup, source),
			install:  p.installSeeds[b.Source],
		})
		if !nativeWithin(p.setup, source) {
			// Installed executables and their shebangs can name their original
			// location. Selected tool sources remain readable, never writable.
			p.policy.ReadOnly = append(p.policy.ReadOnly, source)
			p.policy.DenyWrite = append(p.policy.DenyWrite, source)
		}
	}
	slices.SortFunc(planned, func(a, b nativeBinding) int {
		if len(a.rel) != len(b.rel) {
			return len(a.rel) - len(b.rel)
		}
		return strings.Compare(a.rel, b.rel)
	})
	// A shared parent cannot contain private child bindings. Keep a copy in
	// that case; linking it would send child setup outside os.Root.
	parents := make(map[string]bool)
	for _, b := range planned {
		for parent := filepath.Dir(b.rel); parent != "."; parent = filepath.Dir(parent) {
			parents[parent] = true
		}
	}
	var preserve []string
	for i := range planned {
		b := &planned[i]
		b.shared = b.shared && !parents[b.rel]
		if b.persistent && !b.readonly || b.shared {
			preserve = append(preserve, b.rel)
		}
	}
	for _, b := range planned {
		info, err := home.Lstat(b.rel)
		if b.persistent && !b.readonly && err == nil {
			// Preserve private state, but turn our previous read-only host link
			// into a copy when the user switches this binding to a writable mode.
			if info.Mode()&os.ModeSymlink == 0 {
				continue
			}
			target, linkErr := home.Readlink(b.rel)
			if linkErr != nil {
				return linkErr
			}
			if target != b.source {
				continue
			}
			if err := home.Remove(b.rel); err != nil {
				return err
			}
			err = fs.ErrNotExist
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("seatbelt inspect %s: %w", b.rel, err)
		}
		if b.install && err == nil && info.IsDir() {
			// Existing private versions may contain user changes. Only missing
			// versions or previous host-backed links are seeded/refreshed.
			continue
		}
		if b.shared {
			if b.install && err == nil && info.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("seatbelt installed version %s is not a directory or link", b.rel)
			}
			if err := nativeLinkBinding(home, b.rel, b.source); err != nil {
				return err
			}
			p.policy.DenyWrite = append(p.policy.DenyWrite, cmdpattern.ResolvedSpellings(filepath.Join(cfg.SandboxHome, b.rel))...)
			continue
		}
		var keep []string
		for _, path := range preserve {
			if path != b.rel && nativeWithin(b.rel, path) {
				keep = append(keep, path)
			}
		}
		if err := nativeReset(home, b.rel, keep); err != nil {
			return fmt.Errorf("seatbelt refresh %s: %w", b.rel, err)
		}
		if err := nativeCopy(home, b.source, b.rel, keep); err != nil {
			return fmt.Errorf("seatbelt copy %s: %w", b.source, err)
		}
		if b.readonly {
			p.policy.DenyWrite = append(p.policy.DenyWrite, cmdpattern.ResolvedSpellings(filepath.Join(cfg.SandboxHome, b.rel))...)
		}
	}
	return nil
}

// nativeReset refreshes a disposable parent without moving or deleting nested
// persistent data. Every destination operation goes through os.Root: an old
// sandbox's symlink must never turn setup into a host write.
func nativeReset(root *os.Root, name string, keep []string) error {
	for _, path := range keep {
		if nativeWithin(path, name) {
			return nil
		}
	}
	ancestor := slices.ContainsFunc(keep, func(path string) bool { return nativeWithin(name, path) })
	if !ancestor {
		return root.RemoveAll(name)
	}
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("persistent data ancestor %s is not a directory", name)
	}
	dir, err := root.Open(name)
	if err != nil {
		return err
	}
	entries, readErr := dir.ReadDir(-1)
	if err := errors.Join(readErr, dir.Close()); err != nil {
		return err
	}
	for _, entry := range entries {
		if err := nativeReset(root, filepath.Join(name, entry.Name()), keep); err != nil {
			return err
		}
	}
	return nil
}

// nativeCopy copies a tree without following internal symlinks or opening
// special files. Links are retained as links; the runtime policy decides which
// targets are readable. It never widens an allowlist by following such a link.
func nativeCopy(dst *os.Root, source, name string, keep []string) (retErr error) {
	parent, err := os.OpenRoot(filepath.Dir(source))
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, parent.Close()) }()
	return nativeCopyEntry(dst, parent, filepath.Base(source), name, keep)
}

func nativeCopyEntry(dst, src *os.Root, source, name string, keep []string) error {
	for _, path := range keep {
		if nativeWithin(path, name) {
			return nil
		}
	}
	info, err := src.Lstat(source)
	if err != nil {
		return err
	}
	if err := dst.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	switch {
	case info.IsDir():
		if err := dst.MkdirAll(name, 0o700); err != nil {
			return err
		}
		f, err := src.Open(source)
		if err != nil {
			return err
		}
		entries, readErr := f.ReadDir(-1)
		if err := errors.Join(readErr, f.Close()); err != nil {
			return err
		}
		for _, entry := range entries {
			if err := nativeCopyEntry(dst, src, filepath.Join(source, entry.Name()), filepath.Join(name, entry.Name()), keep); err != nil {
				return err
			}
		}
		return nil
	case info.Mode()&os.ModeSymlink != 0:
		target, err := src.Readlink(source)
		if err != nil {
			return err
		}
		return dst.Symlink(target, name)
	case info.Mode().IsRegular():
		return nativeCopyFile(dst, src, source, name, info.Mode())
	default:
		return fmt.Errorf("refusing special file %s", source)
	}
}

func nativeCopyFile(dst, src *os.Root, source, name string, mode fs.FileMode) (retErr error) {
	in, err := src.OpenFile(source, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, in.Close()) }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source %s changed to a special file", source)
	}
	out, err := dst.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600|mode.Perm()&0o111)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, out.Close()) }()
	_, err = io.Copy(out, in)
	return err
}

func nativeGitMode(cfg *sandbox.Config) string {
	if config.ToolMountMode(cfg.ToolsConfig, "git") == "disabled" {
		return "disabled"
	}
	mode, _ := config.ToolSection(cfg.ToolsConfig, "git")["mode"].(string)
	if mode == "" {
		return "readonly"
	}
	return strings.ToLower(mode)
}

// Seatbelt checks resolved paths. Filename patterns alone do not protect a
// configuration symlink's target, or its differently named aliases.
func (p *nativePlan) protectProjectFiles(project string) error {
	if !p.policy.HideEnvFiles && !p.policy.ProtectConfig && !p.policy.ConfigHidden {
		return nil
	}
	return filepath.WalkDir(project, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("seatbelt inspect protected project paths: %w", walkErr)
		}
		if entry.IsDir() && slices.Contains(p.policy.DenyRead, name) {
			return fs.SkipDir
		}
		base := strings.ToLower(entry.Name())
		env := p.policy.HideEnvFiles && (base == ".env" || strings.HasPrefix(base, ".env."))
		config := (p.policy.ProtectConfig || p.policy.ConfigHidden) && base == ".devsandbox.toml"
		if !env && !config {
			return nil
		}
		resolved, err := filepath.EvalSymlinks(name)
		if err != nil {
			return fmt.Errorf("seatbelt resolve protected project path: %w", err)
		}
		paths := []string{name}
		if resolved != name {
			paths = append(paths, resolved)
		}
		if env || p.policy.ConfigHidden {
			p.policy.DenyRead = append(p.policy.DenyRead, paths...)
		} else {
			p.policy.DenyWrite = append(p.policy.DenyWrite, paths...)
		}
		if entry.IsDir() {
			return fs.SkipDir
		}
		return nil
	})
}
