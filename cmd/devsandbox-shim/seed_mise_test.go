package main

import (
	"os"
	"path/filepath"
	"testing"
)

// buildFakeBakedInstalls creates a minimal replica of the image's
// /opt/mise/installs layout: a real version directory, a version alias symlink,
// and a tool-level backend metadata file.
func buildFakeBakedInstalls(t *testing.T) string {
	t.Helper()
	baked := filepath.Join(t.TempDir(), "opt-mise-installs")
	nodeVer := filepath.Join(baked, "node", "22.13.0", "bin")
	if err := os.MkdirAll(nodeVer, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nodeVer, "node"), []byte("#!/bin/true\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("./22.13.0", filepath.Join(baked, "node", "22")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baked, "node", ".mise.backend"), []byte("core"), 0o644); err != nil {
		t.Fatal(err)
	}
	return baked
}

func TestSeedMiseInstalls_RealVersionDirWithSymlinkedChildren(t *testing.T) {
	baked := buildFakeBakedInstalls(t)
	dataDir := t.TempDir()

	created := seedMiseInstalls(baked, dataDir, os.Getuid(), os.Getgid())
	if created == 0 {
		t.Fatal("seedMiseInstalls reported 0 created links for a fresh seed")
	}

	nodeToolDir := filepath.Join(dataDir, "installs", "node")

	// The tool directory must be a REAL directory, not a symlink, so a version
	// the project installs later persists in the sandbox home instead of being
	// redirected back into the ephemeral image path.
	if fi, err := os.Lstat(nodeToolDir); err != nil {
		t.Fatalf("installs/node not created: %v", err)
	} else if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("installs/node must be a real directory, not a symlink")
	}

	// The version directory must ALSO be a real directory - a symlinked version
	// dir breaks mise's aqua/ubi bin-path discovery - with its children
	// symlinked into the source.
	verDir := filepath.Join(nodeToolDir, "22.13.0")
	if fi, err := os.Lstat(verDir); err != nil {
		t.Fatalf("version dir not created: %v", err)
	} else if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("installs/node/22.13.0 must be a real directory, not a symlink")
	}
	binLink := filepath.Join(verDir, "bin")
	if fi, err := os.Lstat(binLink); err != nil {
		t.Fatalf("version child not created: %v", err)
	} else if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("installs/node/22.13.0/bin must be a symlink")
	}
	if got, _ := os.Readlink(binLink); got != filepath.Join(baked, "node", "22.13.0", "bin") {
		t.Errorf("child symlink target = %q, want baked path", got)
	}

	// Resolving through the seeded shape must reach the real binary.
	if _, err := os.Stat(filepath.Join(verDir, "bin", "node")); err != nil {
		t.Errorf("node binary not reachable through seeded dir: %v", err)
	}

	// Tool-level metadata files are still symlinked.
	if fi, err := os.Lstat(filepath.Join(nodeToolDir, ".mise.backend")); err != nil {
		t.Errorf("backend metadata not mirrored: %v", err)
	} else if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("mirrored .mise.backend should be a symlink")
	}

	// Version alias symlinks are dropped: mise prefix-matches real version dir
	// names, and alias targets are often host-absolute (dangling in the guest).
	if _, err := os.Lstat(filepath.Join(nodeToolDir, "22")); !os.IsNotExist(err) {
		t.Errorf("alias symlink should not be seeded (err=%v)", err)
	}
}

func TestSeedMiseInstalls_NeverClobbersExisting(t *testing.T) {
	baked := buildFakeBakedInstalls(t)
	dataDir := t.TempDir()

	// Simulate a real, project-installed node@22.13.0 already present from a prior
	// run: a real directory with a marker file that must survive the seed, with no
	// seed children merged into it.
	realVer := filepath.Join(dataDir, "installs", "node", "22.13.0")
	if err := os.MkdirAll(realVer, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(realVer, "REAL_INSTALL")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	seedMiseInstalls(baked, dataDir, os.Getuid(), os.Getgid())

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("marker file lost, seed overwrote a real install: %v", err)
	}
	// The real install must not receive seeded children.
	if _, err := os.Lstat(filepath.Join(realVer, "bin")); !os.IsNotExist(err) {
		t.Errorf("seed merged children into a real install (err=%v)", err)
	}
}

func TestSeedMiseInstalls_Idempotent(t *testing.T) {
	baked := buildFakeBakedInstalls(t)
	dataDir := t.TempDir()

	seedMiseInstalls(baked, dataDir, os.Getuid(), os.Getgid())
	// A second run must not error, change the seeded layout, or report new links
	// (the caller uses the count to decide whether to reshim).
	if created := seedMiseInstalls(baked, dataDir, os.Getuid(), os.Getgid()); created != 0 {
		t.Errorf("re-seed reported %d created links, want 0", created)
	}

	binLink := filepath.Join(dataDir, "installs", "node", "22.13.0", "bin")
	if got, err := os.Readlink(binLink); err != nil {
		t.Fatalf("child symlink missing after second seed: %v", err)
	} else if got != filepath.Join(baked, "node", "22.13.0", "bin") {
		t.Errorf("child symlink target changed on re-seed: %q", got)
	}
}

func TestSeedMiseInstalls_MigratesVersionLevelSymlink(t *testing.T) {
	// Earlier seed shape: installs/<tool>/<version> was itself a symlink into the
	// source. That shape breaks aqua/ubi bin-path discovery, so the seed must
	// replace its OWN old symlink with the child-level shape - while a symlink
	// pointing anywhere else (e.g. the other seed source) is left alone.
	baked := buildFakeBakedInstalls(t)
	dataDir := t.TempDir()

	nodeToolDir := filepath.Join(dataDir, "installs", "node")
	if err := os.MkdirAll(nodeToolDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldShape := filepath.Join(nodeToolDir, "22.13.0")
	if err := os.Symlink(filepath.Join(baked, "node", "22.13.0"), oldShape); err != nil {
		t.Fatal(err)
	}

	created := seedMiseInstalls(baked, dataDir, os.Getuid(), os.Getgid())
	if created == 0 {
		t.Fatal("migration seed reported 0 created links")
	}
	if fi, err := os.Lstat(oldShape); err != nil {
		t.Fatalf("version dir missing after migration: %v", err)
	} else if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("old version-level symlink was not migrated to a real directory")
	}
	if _, err := os.Stat(filepath.Join(oldShape, "bin", "node")); err != nil {
		t.Errorf("binary not reachable after migration: %v", err)
	}
}

func TestSeedMiseInstalls_MissingSourceDir(t *testing.T) {
	dataDir := t.TempDir()

	// A missing source installs dir (image without /opt/mise, or no host installs
	// mounted) must be a quiet no-op: no panic, and nothing created under the
	// data dir.
	if created := seedMiseInstalls(filepath.Join(dataDir, "does-not-exist"), dataDir, os.Getuid(), os.Getgid()); created != 0 {
		t.Errorf("missing source reported %d created links, want 0", created)
	}

	if _, err := os.Stat(filepath.Join(dataDir, "installs")); !os.IsNotExist(err) {
		t.Errorf("installs dir should not be created when there is nothing to seed (err=%v)", err)
	}
}

func TestSeedMiseInstalls_SkipsAliasAndDanglingEntries(t *testing.T) {
	// Version alias symlinks (often host-absolute, dangling in the guest) are
	// dropped; real version dirs seed normally, and a child that does not
	// resolve is skipped without failing the rest.
	hostInstalls := filepath.Join(t.TempDir(), "host-installs")
	goVer := filepath.Join(hostInstalls, "go", "1.26.0")
	if err := os.MkdirAll(goVer, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goVer, "go"), []byte("#!/bin/true\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(goVer, "missing"), filepath.Join(goVer, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nonexistent/host/path/go/1.26.0", filepath.Join(hostInstalls, "go", "latest")); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	created := seedMiseInstalls(hostInstalls, dataDir, os.Getuid(), os.Getgid())
	if created != 1 {
		t.Errorf("created = %d, want 1 (real child seeded; alias and dangling child skipped)", created)
	}
	goToolDir := filepath.Join(dataDir, "installs", "go")
	if _, err := os.Lstat(filepath.Join(goToolDir, "latest")); !os.IsNotExist(err) {
		t.Errorf("alias entry should not be seeded (err=%v)", err)
	}
	if _, err := os.Lstat(filepath.Join(goToolDir, "1.26.0", "dangling")); !os.IsNotExist(err) {
		t.Errorf("dangling child should not be seeded (err=%v)", err)
	}
}

func TestSeedMiseInstalls_TwoSourcesFirstWins(t *testing.T) {
	// main() seeds the baked installs first, then the host installs. A version
	// present in both must keep the baked children (image-local I/O beats
	// virtiofs); versions only the host has must still be added.
	baked := buildFakeBakedInstalls(t)

	hostInstalls := filepath.Join(t.TempDir(), "host-installs")
	for _, ver := range []string{"22.13.0", "24.1.0"} {
		d := filepath.Join(hostInstalls, "node", ver, "bin")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "node"), []byte("#!/bin/true\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	dataDir := t.TempDir()
	seedMiseInstalls(baked, dataDir, os.Getuid(), os.Getgid())
	if created := seedMiseInstalls(hostInstalls, dataDir, os.Getuid(), os.Getgid()); created != 1 {
		t.Errorf("host seed created = %d, want 1 (only the host-exclusive version's child)", created)
	}

	sharedChild := filepath.Join(dataDir, "installs", "node", "22.13.0", "bin")
	if got, err := os.Readlink(sharedChild); err != nil {
		t.Fatalf("shared version child missing: %v", err)
	} else if got != filepath.Join(baked, "node", "22.13.0", "bin") {
		t.Errorf("shared version child points at %q, want the baked target", got)
	}

	hostOnlyChild := filepath.Join(dataDir, "installs", "node", "24.1.0", "bin")
	if got, err := os.Readlink(hostOnlyChild); err != nil {
		t.Fatalf("host-exclusive version child missing: %v", err)
	} else if got != filepath.Join(hostInstalls, "node", "24.1.0", "bin") {
		t.Errorf("host-exclusive version child points at %q, want the host target", got)
	}
}
