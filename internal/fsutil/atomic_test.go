package fsutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	if err := WriteFileAtomic(path, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic() error = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != `{"a":1}` {
		t.Errorf("content = %q, want the written bytes", got)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	// The temporary file becomes the real one, so the caller's mode has to survive
	// the rename rather than CreateTemp's 0600 being left in place.
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestWriteFileAtomicReplacesExistingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	if err := os.WriteFile(path, []byte("the old and longer content"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := WriteFileAtomic(path, []byte("new"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic() error = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	// A rename replaces rather than overwrites in place, so no trailing bytes of
	// the previous content can survive.
	if string(got) != "new" {
		t.Errorf("content = %q, want it fully replaced", got)
	}
}

// A failed write must not leave a temporary file behind: the callers write into
// directories that are listed (sandbox roots, the session store), where a stray
// file is either enumerated as real state or trips the enumeration up.
func TestWriteFileAtomicLeavesNoResidue(t *testing.T) {
	dir := t.TempDir()

	if err := WriteFileAtomic(filepath.Join(dir, "state.json"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic() error = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("directory holds %v, want only state.json", names)
	}
}

func TestWriteFileAtomicReportsAMissingDirectory(t *testing.T) {
	err := WriteFileAtomic(filepath.Join(t.TempDir(), "nope", "state.json"), []byte("x"), 0o600)
	if err == nil {
		t.Fatal("WriteFileAtomic() = nil error, want a failure for a missing directory")
	}
	if !strings.Contains(err.Error(), "state.json") {
		t.Errorf("error = %q, want it to name the target file", err)
	}
}

// A bare filename has no directory component, and resolving it to "" would make
// CreateTemp fall back to $TMPDIR - a different filesystem, where the rename is no
// longer atomic and can fail outright.
func TestWriteFileAtomicHandlesABareFilename(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := WriteFileAtomic("state.json", []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json")); err != nil {
		t.Errorf("file was not written to the working directory: %v", err)
	}
}
