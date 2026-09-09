package isolator

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"devsandbox/internal/fsutil"
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

// Seeds may contain account configuration. Container-root reads the private
// manifest using the DAC_OVERRIDE capability granted for shim setup.
func TestWriteOverlayManifest_PrivateEvenAfterUpgrade(t *testing.T) {
	cfg := &Config{SandboxRoot: t.TempDir()}
	stale := filepath.Join(cfg.SandboxRoot, "overlays.json")
	if err := os.WriteFile(stale, []byte("{}"), 0o644); err != nil {
		t.Fatalf("seed stale manifest: %v", err)
	}
	reader, err := os.Open(stale)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	manifest := testOverlayManifest()
	manifest.Seeds = []fsutil.FileSeed{{Name: ".claude.json", Data: []byte("private account state")}}
	path, err := (&DockerIsolator{}).writeOverlayManifest(cfg, manifest)
	if err != nil {
		t.Fatalf("writeOverlayManifest: %v", err)
	}
	if path == stale {
		t.Error("private manifest reused the formerly public path")
	}
	if got, err := io.ReadAll(reader); err != nil || string(got) != "{}" {
		t.Fatalf("legacy reader can observe new manifest content: %q, %v", got, err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat manifest: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("manifest mode = %04o, want 0600", mode)
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
