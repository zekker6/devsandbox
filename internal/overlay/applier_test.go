package overlay

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestApply_CreatesFile(t *testing.T) {
	tmp := t.TempDir()
	srcFile := filepath.Join(tmp, "src.txt")
	if err := os.WriteFile(srcFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(tmp, "host")
	plan := Plan{
		HostPath: host,
		Operations: []Operation{{
			Kind: OpCreate, RelPath: "a.txt",
			HostPath: filepath.Join(host, "a.txt"),
			Source:   srcFile,
			Bytes:    5, Mode: 0o644, ModTime: time.Now(),
		}},
	}
	if err := Apply(plan); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(host, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("want %q, got %q", "hello", string(got))
	}
}

func TestApply_OverwritesFile(t *testing.T) {
	tmp := t.TempDir()
	srcFile := filepath.Join(tmp, "src.txt")
	if err := os.WriteFile(srcFile, []byte("NEW"), 0o644); err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(tmp, "host")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(host, "a.txt")
	if err := os.WriteFile(dest, []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := Plan{
		Operations: []Operation{{
			Kind: OpOverwrite, RelPath: "a.txt", HostPath: dest,
			Source: srcFile, Bytes: 3, Mode: 0o644, ModTime: time.Now(),
		}},
	}
	if err := Apply(plan); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "NEW" {
		t.Errorf("want NEW, got %q", string(got))
	}
}

func TestApply_DeletesFile(t *testing.T) {
	tmp := t.TempDir()
	host := filepath.Join(tmp, "host")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(host, "gone.txt")
	if err := os.WriteFile(victim, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan := Plan{
		Operations: []Operation{{Kind: OpDelete, RelPath: "gone.txt", HostPath: victim}},
	}
	if err := Apply(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(victim); !os.IsNotExist(err) {
		t.Errorf("file should be gone, err=%v", err)
	}
}

func TestApply_CreatesSymlink(t *testing.T) {
	tmp := t.TempDir()
	host := filepath.Join(tmp, "host")
	plan := Plan{
		Operations: []Operation{{
			Kind: OpCreate, RelPath: "link", HostPath: filepath.Join(host, "link"),
			IsSymlink: true, LinkTarget: "target-file",
		}},
	}
	if err := Apply(plan); err != nil {
		t.Fatal(err)
	}
	got, err := os.Readlink(filepath.Join(host, "link"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "target-file" {
		t.Errorf("want target-file, got %q", got)
	}
}

func TestApply_CreatesDirs(t *testing.T) {
	tmp := t.TempDir()
	srcFile := filepath.Join(tmp, "src.txt")
	if err := os.WriteFile(srcFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(tmp, "host")
	plan := Plan{
		Operations: []Operation{{
			Kind: OpCreate, RelPath: "deep/nested/a.txt",
			HostPath: filepath.Join(host, "deep", "nested", "a.txt"),
			Source:   srcFile, Bytes: 1, Mode: 0o644, ModTime: time.Now(),
		}},
	}
	if err := Apply(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(host, "deep", "nested", "a.txt")); err != nil {
		t.Fatal(err)
	}
}
