package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"devsandbox/internal/fsutil"
	"devsandbox/internal/isolator"
)

func TestHomeSeeds_NamedVolumeLifecycle(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "setup.json")
	producer := &isolator.OverlayManifest{Seeds: []fsutil.FileSeed{{Name: ".claude.json", Data: []byte(`{"host":true}`)}}}
	if err := producer.Write(manifestPath); err != nil {
		t.Fatal(err)
	}
	guest := t.TempDir()
	manifest := loadOverlayManifest(manifestPath)
	if len(manifest.Seeds) != 1 {
		t.Fatalf("shim lost the seed in the host manifest: %+v", manifest)
	}
	apply := func() {
		t.Helper()
		if warnings, err := seedHomeFiles(guest, manifest.Seeds, os.Getuid(), os.Getgid()); err != nil {
			t.Fatal(err)
		} else if len(warnings) != 0 {
			t.Fatalf("unexpected seed warnings: %v", warnings)
		}
	}
	apply()
	dst := filepath.Join(guest, ".claude.json")
	if got, err := os.ReadFile(dst); err != nil || string(got) != `{"host":true}` {
		t.Fatalf("unmounted named-volume home was not seeded: %q, %v", got, err)
	}
	if info, err := os.Stat(dst); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private seed permissions: %v, %v", info, err)
	}
	const private = `{"sandbox":true}`
	if err := os.WriteFile(dst, []byte(private), 0o600); err != nil {
		t.Fatal(err)
	}
	apply()
	if got, err := os.ReadFile(dst); err != nil || string(got) != private {
		t.Fatalf("restart overwrote guest configuration: %q, %v", got, err)
	}
}

func TestHomeSeeds_ReplacesEmptyFileAndSymlink(t *testing.T) {
	for _, mode := range []string{"empty", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			dst := filepath.Join(home, ".claude.json")
			victim := filepath.Join(t.TempDir(), "untouched")
			if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			if mode == "empty" {
				if err := os.WriteFile(dst, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(victim, dst); err != nil {
				t.Fatal(err)
			}
			if _, err := seedHomeFiles(home, []fsutil.FileSeed{{Name: ".claude.json", Data: []byte("seed")}}, os.Getuid(), os.Getgid()); err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(dst); err != nil || string(got) != "seed" {
				t.Fatalf("seed was not copied: %q, %v", got, err)
			}
			if got, err := os.ReadFile(victim); err != nil || string(got) != "original" {
				t.Fatalf("followed a symlink: %q, %v", got, err)
			}
		})
	}
}

func TestHomeSeeds_ReadinessWarnings(t *testing.T) {
	for _, kind := range []string{"directory", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			dst := filepath.Join(home, ".claude.json")
			if kind == "directory" {
				if err := os.Mkdir(dst, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := syscall.Mkfifo(dst, 0o600); err != nil {
				t.Fatal(err)
			}
			warnings, err := seedHomeFiles(home, []fsutil.FileSeed{{Name: ".claude.json", Data: []byte("seed")}}, os.Getuid(), os.Getgid())
			if err != nil {
				t.Fatal(err)
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0], dst) || !strings.Contains(warnings[0], "not a file") {
				t.Fatalf("missing non-regular seed warning: %v", warnings)
			}
			ready := filepath.Join(home, "ready")
			if err := writeReadySentinel(ready, warnings); err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(ready); err != nil || string(got) != warnings[0] {
				t.Fatalf("readiness warning = %q, %v; want %q", got, err, warnings[0])
			}
			info, err := os.Lstat(dst)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().IsRegular() {
				t.Fatal("non-regular seed destination was overwritten")
			}
			warnings, err = seedHomeFiles(home, nil, os.Getuid(), os.Getgid())
			if err != nil || len(warnings) != 0 {
				t.Fatalf("warnings leaked between calls: %v, %v", warnings, err)
			}
		})
	}
}

func TestWriteReadySentinel(t *testing.T) {
	for _, warnings := range [][]string{nil, {"first warning", "second warning"}} {
		path := filepath.Join(t.TempDir(), "ready")
		if err := writeReadySentinel(path, warnings); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != strings.Join(warnings, "\n") {
			t.Fatalf("sentinel = %q, %v; want warnings %v", got, err, warnings)
		}
	}
}

func TestWriteReadySentinel_ReportsWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "ready")
	if err := writeReadySentinel(path, []string{"seed warning"}); err == nil {
		t.Fatal("missing parent directory did not fail readiness write")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("failed write published a ready sentinel: %v", err)
	}
}

func TestHomeSeeds_RejectsNonBasenames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../victim", "/tmp/victim", "dir/file"} {
		if _, err := seedHomeFiles(t.TempDir(), []fsutil.FileSeed{{Name: name}}, os.Getuid(), os.Getgid()); err == nil {
			t.Errorf("accepted invalid seed name %q", name)
		}
	}
}
