package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareOverlayDirs(t *testing.T) {
	base := t.TempDir()
	target := "/home/sandboxuser/.config/fish"

	upper, work, err := prepareOverlayDirs(base, target)
	if err != nil {
		t.Fatalf("prepareOverlayDirs: %v", err)
	}

	// Verify dirs exist
	if _, err := os.Stat(upper); err != nil {
		t.Errorf("upper dir %s does not exist: %v", upper, err)
	}
	if _, err := os.Stat(work); err != nil {
		t.Errorf("work dir %s does not exist: %v", work, err)
	}

	// Verify deterministic — same target produces same dirs
	upper2, work2, err := prepareOverlayDirs(base, target)
	if err != nil {
		t.Fatal(err)
	}
	if upper != upper2 || work != work2 {
		t.Error("prepareOverlayDirs should be deterministic for same target")
	}
}

func TestPrepareOverlayDirs_DifferentTargets(t *testing.T) {
	base := t.TempDir()

	upper1, _, _ := prepareOverlayDirs(base, "/home/sandboxuser/.config/fish")
	upper2, _, _ := prepareOverlayDirs(base, "/home/sandboxuser/.config/mise")

	if upper1 == upper2 {
		t.Error("different targets should produce different dirs")
	}
}

func TestBuildOverlayMountOptions(t *testing.T) {
	opts := buildOverlayMountOptions("/lower", "/upper", "/work")
	expected := "lowerdir=/lower,upperdir=/upper,workdir=/work"
	if opts != expected {
		t.Errorf("got %q, want %q", opts, expected)
	}
}

func TestCopyDir(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Create source structure: file, subdir/nested, symlink
	if err := os.MkdirAll(filepath.Join(src, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "subdir", "nested.txt"), []byte("world"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file.txt", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}

	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir: %v", err)
	}

	// Verify file content
	data, err := os.ReadFile(filepath.Join(dst, "file.txt"))
	if err != nil {
		t.Fatalf("read copied file: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("file content = %q, want %q", data, "hello")
	}

	// Verify nested file
	data, err = os.ReadFile(filepath.Join(dst, "subdir", "nested.txt"))
	if err != nil {
		t.Fatalf("read nested file: %v", err)
	}
	if string(data) != "world" {
		t.Errorf("nested content = %q, want %q", data, "world")
	}

	// Verify symlink
	link, err := os.Readlink(filepath.Join(dst, "link"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if link != "file.txt" {
		t.Errorf("symlink target = %q, want %q", link, "file.txt")
	}

	// Verify permissions preserved
	info, _ := os.Stat(filepath.Join(dst, "subdir", "nested.txt"))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestCopyDir_EmptySrc(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir empty: %v", err)
	}

	entries, _ := os.ReadDir(dst)
	if len(entries) != 0 {
		t.Errorf("expected empty dst, got %d entries", len(entries))
	}
}

func TestSplitOverlaySpec(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			"normal",
			"/home/sandboxuser/.config/fish:/tmp/.devsandbox-overlay-layers/abc/upper:/tmp/.devsandbox-overlay-layers/abc/work",
			[]string{
				"/home/sandboxuser/.config/fish",
				"/tmp/.devsandbox-overlay-layers/abc/upper",
				"/tmp/.devsandbox-overlay-layers/abc/work",
			},
		},
		{
			"empty",
			"",
			nil,
		},
		{
			"one colon",
			"a:b",
			nil,
		},
		{
			"exactly three parts",
			"a:b:c",
			[]string{"a", "b", "c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitOverlaySpec(tt.input)
			if tt.want == nil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
