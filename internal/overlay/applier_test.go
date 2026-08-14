package overlay

import (
	"errors"
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

// TestApply_SymlinkDestinationNotFollowed covers a host destination that is a
// symlink pointing outside the migration root. Before the fix, copyFile opened
// it O_WRONLY|O_TRUNC and rewrote the link target — a file the preview never
// named and that lives outside the requested host path.
func TestApply_SymlinkDestinationNotFollowed(t *testing.T) {
	tmp := t.TempDir()
	outside := filepath.Join(tmp, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}

	host := filepath.Join(tmp, "host")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(host, "a.txt")
	if err := os.Symlink(secret, dest); err != nil {
		t.Fatal(err)
	}

	srcFile := filepath.Join(tmp, "src.txt")
	if err := os.WriteFile(srcFile, []byte("NEW"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan := Plan{
		HostPath: host,
		Operations: []Operation{{
			Kind: OpOverwrite, RelPath: "a.txt", HostPath: dest,
			Source: srcFile, Bytes: 3, Mode: 0o644, ModTime: time.Now(),
		}},
	}
	if err := Apply(plan); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if got, err := os.ReadFile(secret); err != nil || string(got) != "SECRET" {
		t.Errorf("link target modified: content=%q err=%v", string(got), err)
	}
	fi, err := os.Lstat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("destination is still a symlink, want a regular file")
	}
	if got, _ := os.ReadFile(dest); string(got) != "NEW" {
		t.Errorf("want NEW at destination, got %q", string(got))
	}
}

// TestApply_SymlinkedParentNotTraversed covers a symlinked parent component
// beneath the migration root. MkdirAll follows it silently, so before the fix
// the file landed in the linked-to directory outside the root.
func TestApply_SymlinkedParentNotTraversed(t *testing.T) {
	tmp := t.TempDir()
	outside := filepath.Join(tmp, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}

	host := filepath.Join(tmp, "host")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(host, "d")); err != nil {
		t.Fatal(err)
	}

	srcFile := filepath.Join(tmp, "src.txt")
	if err := os.WriteFile(srcFile, []byte("NEW"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan := Plan{
		HostPath: host,
		Operations: []Operation{{
			Kind: OpCreate, RelPath: filepath.Join("d", "a.txt"),
			HostPath: filepath.Join(host, "d", "a.txt"),
			Source:   srcFile, Bytes: 3, Mode: 0o644, ModTime: time.Now(),
		}},
	}
	err := Apply(plan)
	if !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("want ErrUnsafeDestination, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "a.txt")); !os.IsNotExist(statErr) {
		t.Errorf("wrote through the symlinked parent: err=%v", statErr)
	}
}

// TestApply_SymlinkedParentNotTraversedOnDelete covers the same traversal for
// OpDelete, which reaches the host path through os.RemoveAll.
func TestApply_SymlinkedParentNotTraversedOnDelete(t *testing.T) {
	tmp := t.TempDir()
	outside := filepath.Join(tmp, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(victim, []byte("KEEP"), 0o644); err != nil {
		t.Fatal(err)
	}

	host := filepath.Join(tmp, "host")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(host, "d")); err != nil {
		t.Fatal(err)
	}

	plan := Plan{
		HostPath: host,
		Operations: []Operation{{
			Kind: OpDelete, RelPath: filepath.Join("d", "keep.txt"),
			HostPath: filepath.Join(host, "d", "keep.txt"),
		}},
	}
	if err := Apply(plan); !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("want ErrUnsafeDestination, got %v", err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("deleted through the symlinked parent: %v", err)
	}
}

// TestApply_SourceSwappedToSymlinkRefused covers the upper changing between
// planning and application: the planner classified the entry as a regular file
// with Lstat, so a symlink at apply time must not be read through.
func TestApply_SourceSwappedToSymlinkRefused(t *testing.T) {
	tmp := t.TempDir()
	secret := filepath.Join(tmp, "secret.txt")
	if err := os.WriteFile(secret, []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(tmp, "src.txt")
	if err := os.Symlink(secret, srcFile); err != nil {
		t.Fatal(err)
	}

	host := filepath.Join(tmp, "host")
	plan := Plan{
		HostPath: host,
		Operations: []Operation{{
			Kind: OpCreate, RelPath: "a.txt", HostPath: filepath.Join(host, "a.txt"),
			Source: srcFile, Bytes: 6, Mode: 0o644, ModTime: time.Now(),
		}},
	}
	if err := Apply(plan); err == nil {
		t.Fatal("want an error for a symlinked source, got nil")
	}
	if _, err := os.Stat(filepath.Join(host, "a.txt")); !os.IsNotExist(err) {
		t.Errorf("destination should not exist, err=%v", err)
	}
}

// TestApply_OverwritePreservesModeAndMtime is the counterweight: the atomic
// replace must leave an ordinary overwrite indistinguishable from before.
func TestApply_OverwritePreservesModeAndMtime(t *testing.T) {
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
	if err := os.WriteFile(dest, []byte("OLD"), 0o600); err != nil {
		t.Fatal(err)
	}

	mtime := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	plan := Plan{
		HostPath: host,
		Operations: []Operation{{
			Kind: OpOverwrite, RelPath: "a.txt", HostPath: dest,
			Source: srcFile, Bytes: 3, Mode: 0o640, ModTime: mtime,
		}},
	}
	if err := Apply(plan); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "NEW" {
		t.Errorf("want NEW, got %q", string(got))
	}
	fi, err := os.Lstat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("want mode 0640, got %v", fi.Mode().Perm())
	}
	if !fi.ModTime().Equal(mtime) {
		t.Errorf("want mtime %v, got %v", mtime, fi.ModTime())
	}
	// No temp file left behind in the destination directory.
	entries, err := os.ReadDir(host)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("want 1 entry in host dir, got %d", len(entries))
	}
}

// TestApply_RejectsUnrootedOperation covers the fail-closed path: an operation
// whose HostPath does not end in its RelPath has no derivable migration root,
// so no confinement can be proven and it is refused rather than applied.
func TestApply_RejectsUnrootedOperation(t *testing.T) {
	tmp := t.TempDir()
	srcFile := filepath.Join(tmp, "src.txt")
	if err := os.WriteFile(srcFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(tmp, "host", "elsewhere.txt")
	plan := Plan{
		HostPath: filepath.Join(tmp, "host"),
		Operations: []Operation{{
			Kind: OpCreate, RelPath: "a.txt", HostPath: dest,
			Source: srcFile, Bytes: 1, Mode: 0o644, ModTime: time.Now(),
		}},
	}
	if err := Apply(plan); !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("want ErrUnsafeDestination, got %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("destination should not exist, err=%v", err)
	}
}

// TestApply_TypeChangeMatrix enumerates every pair in
// {regular, dir, symlink} × {regular, dir, symlink}. An overlay records a type
// change as an rm plus a recreate at the same name, and BuildPlan classifies
// any existing target as OpOverwrite without comparing types — so before the
// fix a directory over a file failed ENOTDIR, a file over a directory failed
// EISDIR, and a symlink over a non-empty directory failed EEXIST after the
// ignored os.Remove. Each aborted Apply with earlier operations committed.
//
// The destination symlink points at a directory in every case: that is what
// makes MkdirAll follow it silently instead of failing, so the entry the
// migration meant to create lands outside the host path it named.
func TestApply_TypeChangeMatrix(t *testing.T) {
	const (
		kindFile    = "file"
		kindDir     = "dir"
		kindSymlink = "symlink"
	)
	kinds := []string{kindFile, kindDir, kindSymlink}

	for _, srcKind := range kinds {
		for _, dstKind := range kinds {
			t.Run(srcKind+"_over_"+dstKind, func(t *testing.T) {
				tmp := t.TempDir()

				outside := filepath.Join(tmp, "outside")
				if err := os.MkdirAll(outside, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(outside, "kept.txt"), []byte("KEPT"), 0o644); err != nil {
					t.Fatal(err)
				}

				host := filepath.Join(tmp, "host")
				if err := os.MkdirAll(host, 0o755); err != nil {
					t.Fatal(err)
				}
				dest := filepath.Join(host, "entry")
				switch dstKind {
				case kindFile:
					if err := os.WriteFile(dest, []byte("OLD"), 0o644); err != nil {
						t.Fatal(err)
					}
				case kindDir:
					if err := os.MkdirAll(dest, 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dest, "child.txt"), []byte("CHILD"), 0o644); err != nil {
						t.Fatal(err)
					}
				case kindSymlink:
					if err := os.Symlink(outside, dest); err != nil {
						t.Fatal(err)
					}
				}

				mtime := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
				op := Operation{
					Kind: OpOverwrite, RelPath: "entry", HostPath: dest,
					ModTime: mtime,
				}
				switch srcKind {
				case kindFile:
					src := filepath.Join(tmp, "src.txt")
					if err := os.WriteFile(src, []byte("NEW"), 0o644); err != nil {
						t.Fatal(err)
					}
					op.Source = src
					op.Bytes = 3
					op.Mode = 0o640
				case kindDir:
					src := filepath.Join(tmp, "srcdir")
					if err := os.MkdirAll(src, 0o755); err != nil {
						t.Fatal(err)
					}
					op.Source = src
					op.IsDir = true
					op.Mode = os.ModeDir | 0o750
				case kindSymlink:
					op.IsSymlink = true
					op.LinkTarget = "new-target"
				}

				if err := Apply(Plan{HostPath: host, Operations: []Operation{op}}); err != nil {
					t.Fatalf("apply: %v", err)
				}

				fi, err := os.Lstat(dest)
				if err != nil {
					t.Fatalf("lstat destination: %v", err)
				}
				switch srcKind {
				case kindFile:
					if !fi.Mode().IsRegular() {
						t.Fatalf("want a regular file at the destination, got %v", fi.Mode())
					}
					if got, err := os.ReadFile(dest); err != nil || string(got) != "NEW" {
						t.Errorf("want NEW, got %q err=%v", string(got), err)
					}
					if fi.Mode().Perm() != 0o640 {
						t.Errorf("want mode 0640, got %v", fi.Mode().Perm())
					}
					if !fi.ModTime().Equal(mtime) {
						t.Errorf("want mtime %v, got %v", mtime, fi.ModTime())
					}
				case kindDir:
					if !fi.IsDir() {
						t.Fatalf("want a real directory at the destination, got %v", fi.Mode())
					}
					child := filepath.Join(dest, "child.txt")
					_, childErr := os.Stat(child)
					if dstKind == kindDir && childErr != nil {
						t.Errorf("existing directory contents were removed: %v", childErr)
					}
					if dstKind != kindDir && !os.IsNotExist(childErr) {
						t.Errorf("unexpected child entry, err=%v", childErr)
					}
				case kindSymlink:
					target, err := os.Readlink(dest)
					if err != nil {
						t.Fatalf("readlink: %v", err)
					}
					if target != "new-target" {
						t.Errorf("want new-target, got %q", target)
					}
				}

				// Nothing may reach the directory the destination symlink
				// pointed at: it lives outside the migration root.
				entries, err := os.ReadDir(outside)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 1 {
					t.Errorf("wrote through the destination symlink: %d entries outside the root", len(entries))
				}
				if got, err := os.ReadFile(filepath.Join(outside, "kept.txt")); err != nil || string(got) != "KEPT" {
					t.Errorf("file outside the root modified: content=%q err=%v", string(got), err)
				}
			})
		}
	}
}

// TestApply_ResumesAfterPartialFailure exercises the idempotence Apply's doc
// comment promises: a run that aborts partway leaves the operations it already
// applied in place, and a re-run after the cause is cleared completes the rest
// without tripping over them.
func TestApply_ResumesAfterPartialFailure(t *testing.T) {
	tmp := t.TempDir()
	host := filepath.Join(tmp, "host")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	// A host file where the upper holds a directory, so the resumed run has a
	// type change to reconcile as well as an already-applied operation to redo.
	if err := os.WriteFile(filepath.Join(host, "d"), []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}

	srcDir := filepath.Join(tmp, "upper", "d")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(tmp, "upper", "b.txt")

	plan := Plan{
		HostPath: host,
		Operations: []Operation{
			{
				Kind: OpOverwrite, RelPath: "d", HostPath: filepath.Join(host, "d"),
				Source: srcDir, IsDir: true, Mode: os.ModeDir | 0o755, ModTime: time.Now(),
			},
			{
				Kind: OpCreate, RelPath: "b.txt", HostPath: filepath.Join(host, "b.txt"),
				Source: missing, Bytes: 3, Mode: 0o644, ModTime: time.Now(),
			},
		},
	}

	if err := Apply(plan); err == nil {
		t.Fatal("want an error for the missing source, got nil")
	}
	if fi, err := os.Lstat(filepath.Join(host, "d")); err != nil || !fi.IsDir() {
		t.Fatalf("first operation should have been applied: err=%v", err)
	}

	if err := os.WriteFile(missing, []byte("NEW"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Apply(plan); err != nil {
		t.Fatalf("resumed apply: %v", err)
	}
	if fi, err := os.Lstat(filepath.Join(host, "d")); err != nil || !fi.IsDir() {
		t.Errorf("directory missing after resume: err=%v", err)
	}
	if got, err := os.ReadFile(filepath.Join(host, "b.txt")); err != nil || string(got) != "NEW" {
		t.Errorf("want NEW, got %q err=%v", string(got), err)
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

// TestApply_UnreadableSourceLeavesHostDirectoryIntact pins the ordering inside
// copyFile. reconcileDestination removes a host *directory* standing where a
// file has to go, and that removal is recursive and irreversible - so it must
// not run until the replacement exists. The upper is sandbox-writable and can
// change between planning and application, and running the removal first turned
// every such failure into a host directory deleted with nothing put back, on
// the first run and identically on every retry.
func TestApply_UnreadableSourceLeavesHostDirectoryIntact(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the mode bits this test relies on")
	}

	tmp := t.TempDir()
	upper := filepath.Join(tmp, "upper")
	host := filepath.Join(tmp, "host")

	// The upper holds a regular file the planner classified as migratable, but
	// which cannot be opened.
	writeFile(t, filepath.Join(upper, "d"), "replacement")
	if err := os.Chmod(filepath.Join(upper, "d"), 0o000); err != nil {
		t.Fatal(err)
	}
	// The host holds a directory with contents at the same name.
	writeFile(t, filepath.Join(host, "d", "precious.txt"), "host data")

	plan, err := BuildPlan([]UpperSource{{Kind: UpperPrimary, Path: upper, SandboxID: "s1"}}, host)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(plan); err == nil {
		t.Fatal("Apply succeeded on an unreadable source, want error")
	}

	if _, err := os.Stat(filepath.Join(host, "d", "precious.txt")); err != nil {
		t.Fatalf("host directory was destroyed before the source proved readable: %v", err)
	}
}

// TestApply_FileOverHostDirectoryStillReplaces is the same shape with a
// readable source, so the guard above cannot pass by disabling the replacement.
func TestApply_FileOverHostDirectoryStillReplaces(t *testing.T) {
	tmp := t.TempDir()
	upper := filepath.Join(tmp, "upper")
	host := filepath.Join(tmp, "host")

	writeFile(t, filepath.Join(upper, "d"), "replacement")
	writeFile(t, filepath.Join(host, "d", "old.txt"), "host data")

	plan, err := BuildPlan([]UpperSource{{Kind: UpperPrimary, Path: upper, SandboxID: "s1"}}, host)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(plan); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(host, "d"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "replacement" {
		t.Errorf("host file = %q, want %q", got, "replacement")
	}
}
