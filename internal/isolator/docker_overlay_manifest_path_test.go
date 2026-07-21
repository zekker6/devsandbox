package isolator

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func testOverlayManifest() *OverlayManifest {
	return &OverlayManifest{Overlays: []OverlayEntry{{Path: "/home/sandboxuser/.config/fish", Type: "tmpoverlay"}}}
}

// TestWriteOverlayManifest_StablePathAcrossRuns verifies successive launches
// reuse one host path. A kept container binds this path permanently, so a
// per-run name would leave every later `docker start` mounting a path that no
// longer exists, failing the restart and forcing a full recreate.
func TestWriteOverlayManifest_StablePathAcrossRuns(t *testing.T) {
	cfg := &Config{SandboxRoot: t.TempDir()}
	d := &DockerIsolator{}

	first, err := d.writeOverlayManifest(cfg, testOverlayManifest())
	if err != nil {
		t.Fatalf("writeOverlayManifest (first run): %v", err)
	}
	second, err := d.writeOverlayManifest(cfg, testOverlayManifest())
	if err != nil {
		t.Fatalf("writeOverlayManifest (second run): %v", err)
	}

	if first != second {
		t.Errorf("manifest path changed between runs: %q then %q", first, second)
	}
	if want := filepath.Join(cfg.SandboxRoot, overlayManifestFileName); first != want {
		t.Errorf("manifest path = %q, want %q", first, want)
	}
}

// TestWriteOverlayManifest_RewritesInPlace verifies the manifest keeps its inode
// across rewrites. A kept container's bind resolves to the inode mounted at
// start, so replacing the file (rather than truncating it) would leave a running
// container reading stale overlay entries.
func TestWriteOverlayManifest_RewritesInPlace(t *testing.T) {
	cfg := &Config{SandboxRoot: t.TempDir()}
	d := &DockerIsolator{}

	path, err := d.writeOverlayManifest(cfg, testOverlayManifest())
	if err != nil {
		t.Fatalf("writeOverlayManifest: %v", err)
	}
	before := inodeOf(t, path)

	updated := &OverlayManifest{Overlays: []OverlayEntry{{Path: "/home/sandboxuser/.config/nvim", Type: "tmpoverlay"}}}
	if _, err := d.writeOverlayManifest(cfg, updated); err != nil {
		t.Fatalf("writeOverlayManifest (rewrite): %v", err)
	}

	if after := inodeOf(t, path); after != before {
		t.Errorf("manifest inode changed on rewrite: %d then %d", before, after)
	}

	got, err := ReadOverlayManifest(path)
	if err != nil {
		t.Fatalf("ReadOverlayManifest: %v", err)
	}
	if len(got.Overlays) != 1 || got.Overlays[0].Path != "/home/sandboxuser/.config/nvim" {
		t.Errorf("rewritten manifest = %+v, want the updated entry", got.Overlays)
	}
}

// TestWriteOverlayManifest_ReadableByContainerRoot verifies the manifest ends up
// world-readable even when an older devsandbox left it at 0600. Container-root
// reads it during shim setup, and cannot bypass the DAC bits when DAC_OVERRIDE
// is unavailable.
func TestWriteOverlayManifest_ReadableByContainerRoot(t *testing.T) {
	cfg := &Config{SandboxRoot: t.TempDir()}
	stale := filepath.Join(cfg.SandboxRoot, overlayManifestFileName)
	if err := os.WriteFile(stale, []byte("{}"), 0o600); err != nil {
		t.Fatalf("seed stale manifest: %v", err)
	}

	path, err := (&DockerIsolator{}).writeOverlayManifest(cfg, testOverlayManifest())
	if err != nil {
		t.Fatalf("writeOverlayManifest: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat manifest: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o644 {
		t.Errorf("manifest mode = %04o, want 0644", mode)
	}
}

// TestWriteOverlayManifest_RequiresSandboxRoot verifies a missing sandbox root
// fails loudly instead of writing the manifest to a relative path.
func TestWriteOverlayManifest_RequiresSandboxRoot(t *testing.T) {
	if _, err := (&DockerIsolator{}).writeOverlayManifest(&Config{}, testOverlayManifest()); err == nil {
		t.Error("writeOverlayManifest without a sandbox root = nil error, want a failure")
	}
}

// TestWriteOverlayManifest_OutsideSandboxHome verifies the manifest is not
// written under SandboxHome. SandboxHome is mounted into the container, so a
// manifest there would be rewritable by the sandbox before the shim reads it.
func TestWriteOverlayManifest_OutsideSandboxHome(t *testing.T) {
	root := t.TempDir()
	cfg := &Config{SandboxRoot: root, SandboxHome: filepath.Join(root, "home")}

	path, err := (&DockerIsolator{}).writeOverlayManifest(cfg, testOverlayManifest())
	if err != nil {
		t.Fatalf("writeOverlayManifest: %v", err)
	}

	if strings.HasPrefix(path, cfg.SandboxHome+string(filepath.Separator)) {
		t.Errorf("manifest %q is inside the container-visible sandbox home %q", path, cfg.SandboxHome)
	}
}

func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("inode not available on this platform")
	}
	return uint64(st.Ino)
}
