package fsutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadMiseInstallsShallow(t *testing.T) {
	root := t.TempDir()
	version := filepath.Join(root, "node", "22.0.0")
	if err := os.MkdirAll(version, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "node", ".mise.backend"), []byte("node"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, link := range []struct{ name, target string }{
		{"node/latest", "22.0.0"}, {"node/broken", "/missing"}, {"alias", "node"},
	} {
		if err := os.Symlink(link.target, filepath.Join(root, link.name)); err != nil {
			t.Fatal(err)
		}
	}
	// Enumeration needs only names and types, not access to any payload.
	if err := os.Chmod(version, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(version, 0o700); err != nil {
			t.Error(err)
		}
	})
	tools, err := ReadMiseInstalls(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "node" {
		t.Fatalf("tools = %#v", tools)
	}
	var names []string
	for _, entry := range tools[0].Entries {
		names = append(names, entry.Name())
	}
	if !reflect.DeepEqual(names, []string{".mise.backend", "22.0.0"}) {
		t.Fatalf("entries = %v", names)
	}
}

func TestReadMiseInstallsErrors(t *testing.T) {
	root := t.TempDir()
	if entries, err := ReadMiseInstalls(filepath.Join(root, "missing")); err != nil || len(entries) != 0 {
		t.Fatalf("missing installs: %v, %v", entries, err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMiseInstalls(file); err == nil {
		t.Fatal("accepted non-directory installs")
	}
}

func TestReadMiseInstallsPartialError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read permission-denied directories")
	}
	root := t.TempDir()
	for _, tool := range []string{"denied", "readable"} {
		if err := os.MkdirAll(filepath.Join(root, tool, "1.0"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	denied := filepath.Join(root, "denied")
	if err := os.Chmod(denied, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(denied, 0o700); err != nil {
			t.Error(err)
		}
	})
	tools, err := ReadMiseInstalls(root)
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("error = %v, want permission error", err)
	}
	if len(tools) != 1 || tools[0].Name != "readable" {
		t.Fatalf("partial results = %#v", tools)
	}
}
