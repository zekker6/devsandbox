package main

import (
	"net"
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

	if err := copyDir(filepath.Dir(dst), src, dst); err != nil {
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

// TestCopyDir_SkipsSocket reproduces the krun copy-on-start failure where a
// unix socket in the source tree (e.g. ~/.claude/channels/matrix/mux.sock)
// aborted the whole launch: os.ReadFile on a socket returns ENXIO. The copy
// must skip non-regular files and still copy regular files around them.
func TestCopyDir_SkipsSocket(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	if err := os.WriteFile(filepath.Join(src, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A live unix socket alongside a regular file.
	sockPath := filepath.Join(src, "mux.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("create unix socket: %v", err)
	}
	defer func() { _ = ln.Close() }()

	if err := copyDir(filepath.Dir(dst), src, dst); err != nil {
		t.Fatalf("copyDir with socket present: %v", err)
	}

	// Regular file copied.
	data, err := os.ReadFile(filepath.Join(dst, "config.json"))
	if err != nil || string(data) != "{}" {
		t.Fatalf("regular file not copied: data=%q err=%v", data, err)
	}
	// Socket skipped, not recreated.
	if _, err := os.Lstat(filepath.Join(dst, "mux.sock")); !os.IsNotExist(err) {
		t.Errorf("socket should be skipped, got Lstat err=%v", err)
	}
}

// TestCopyDirExcluding_SkipsMount reproduces the krun copy-on-start collision
// where a nested binding (~/.claude/projects) is mounted read-only beneath the
// copyoverlay target (~/.claude). The copy must prune that subtree instead of
// writing through it (which would hit EROFS and abort the launch).
func TestCopyDirExcluding_SkipsMount(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	if err := os.MkdirAll(filepath.Join(src, "projects", "p1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "projects", "p1", "session.jsonl"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Mark dst/projects as an excluded mount point.
	skip := map[string]bool{filepath.Join(dst, "projects"): true}
	if err := copyDirExcluding(src, dst, skip); err != nil {
		t.Fatalf("copyDirExcluding: %v", err)
	}

	// Top-level file copied.
	if _, err := os.Stat(filepath.Join(dst, "settings.json")); err != nil {
		t.Errorf("settings.json not copied: %v", err)
	}
	// Excluded subtree NOT descended into - nothing written under it.
	if _, err := os.Stat(filepath.Join(dst, "projects", "p1", "session.jsonl")); !os.IsNotExist(err) {
		t.Errorf("excluded mount subtree should be skipped, got Stat err=%v", err)
	}
}

// TestCleanCopyOverlayTargetExcluding is the Task 2 regression: under krun a
// tmpoverlay dir degrades to a copy into the persistent sandbox home, so files a
// previous (potentially untrusted) run wrote there - e.g. a malicious hook
// planted in ~/.claude - must not survive into the next session. The clean-slate
// step must remove that stale content while leaving nested read-only mount
// points (and every ancestor directory on the way to them) untouched, then the
// fresh copy repopulates only what the host source contains.
func TestCleanCopyOverlayTargetExcluding(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Host source: a legitimate settings file the fresh copy should restore.
	if err := os.WriteFile(filepath.Join(src, "settings.json"), []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Stale target content left by a previous run:
	//   - a top-level malicious hook
	//   - a nested mount point (dst/projects) with content that must be preserved
	//   - a deep mount point (dst/a/b/mnt) with an extraneous file beside it at
	//     each ancestor level, to prove ancestors survive but their junk does not
	if err := os.WriteFile(filepath.Join(dst, "evil-hook.sh"), []byte("rm -rf /"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dst, "projects", "p1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "projects", "p1", "session.jsonl"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dst, "a", "b", "mnt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "a", "b", "mnt", "data"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "a", "stale-a.txt"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "a", "b", "stale-b.txt"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A symlink planted at the top level, pointing OUTSIDE the target at a
	// host-sensitive dir, and named to resemble a mount ancestor (a skip path is
	// registered "beneath" it below). The clean must remove the link itself and
	// never follow it: e.IsDir() is false for a symlink, so it must not enter the
	// recurse-into-ancestor branch and must fall to os.RemoveAll(child).
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "sensitive"), []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(dst, "linkdir")); err != nil {
		t.Fatal(err)
	}

	// Mark the nested mount points as excluded (simulating /proc/self/mountinfo).
	// "linkdir/mnt" resembles a mount nested under the symlink, to prove a symlink
	// named like a mount ancestor is still removed (not traversed into).
	skip := map[string]bool{
		filepath.Join(dst, "projects"):       true,
		filepath.Join(dst, "a", "b", "mnt"):  true,
		filepath.Join(dst, "linkdir", "mnt"): true,
	}

	if err := cleanCopyOverlayTargetExcluding(filepath.Dir(dst), dst, skip); err != nil {
		t.Fatalf("cleanCopyOverlayTargetExcluding: %v", err)
	}

	// Stale, non-mount content is gone.
	if _, err := os.Lstat(filepath.Join(dst, "evil-hook.sh")); !os.IsNotExist(err) {
		t.Errorf("stale hook should be removed, got Lstat err=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "a", "stale-a.txt")); !os.IsNotExist(err) {
		t.Errorf("stale ancestor file should be removed, got Lstat err=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "a", "b", "stale-b.txt")); !os.IsNotExist(err) {
		t.Errorf("stale ancestor file should be removed, got Lstat err=%v", err)
	}

	// The planted symlink is removed (the link, not its target) and was not
	// followed: the host-sensitive file it pointed at must still exist.
	if _, err := os.Lstat(filepath.Join(dst, "linkdir")); !os.IsNotExist(err) {
		t.Errorf("planted symlink should be removed, got Lstat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(external, "sensitive")); err != nil {
		t.Errorf("symlink target must not be followed/removed: %v", err)
	}

	// Mount points and their content survive, together with their ancestors.
	if _, err := os.Stat(filepath.Join(dst, "projects", "p1", "session.jsonl")); err != nil {
		t.Errorf("mount-point content should be preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "a", "b", "mnt", "data")); err != nil {
		t.Errorf("deep mount-point content should be preserved: %v", err)
	}

	// Fresh copy repopulates the host source; the mount subtree is untouched.
	if err := copyDirExcluding(src, dst, skip); err != nil {
		t.Fatalf("copyDirExcluding: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dst, "settings.json")); err != nil || string(data) != `{"ok":true}` {
		t.Fatalf("source file not restored: data=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "projects", "p1", "session.jsonl")); err != nil {
		t.Errorf("mount-point content should still be present after copy: %v", err)
	}
}

// TestCleanCopyOverlayTargetExcluding_RootSymlink is the regression for the
// root-symlink vector (distinct from the child-symlink case in the test above).
// The copyoverlay target ROOT lives in the persistent, guest-writable sandbox
// home, so a prior untrusted run can delete the real dir and replace it with a
// symlink pointing at the project directory or another read-write mount. The shim
// runs the clean+copy as ROOT before dropping privileges, so following that
// symlink would os.RemoveAll files OUTSIDE the target and then copy source files
// INTO the symlink target. The root guard must remove the link itself (never its
// target), recreate dst as a real directory, and copy the source there.
func TestCleanCopyOverlayTargetExcluding_RootSymlink(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "settings.json"), []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// A separate directory the symlink points at, standing in for the project dir
	// or another read-write mount, seeded with a file that must survive.
	victim := t.TempDir()
	if err := os.WriteFile(filepath.Join(victim, "important.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	// dst ROOT is a symlink to victim: a previous run deleted the real dir and
	// planted the link in its place.
	parent := t.TempDir()
	dst := filepath.Join(parent, ".claude")
	if err := os.Symlink(victim, dst); err != nil {
		t.Fatal(err)
	}

	if err := cleanCopyOverlayTargetExcluding(parent, dst, map[string]bool{}); err != nil {
		t.Fatalf("cleanCopyOverlayTargetExcluding: %v", err)
	}
	if err := copyDirExcluding(src, dst, map[string]bool{}); err != nil {
		t.Fatalf("copyDirExcluding: %v", err)
	}

	// (a) The symlink-target directory's original files are NOT deleted through the
	// root symlink.
	if _, err := os.Stat(filepath.Join(victim, "important.txt")); err != nil {
		t.Errorf("symlink target file must not be deleted through the root symlink: %v", err)
	}
	// (b) dst is now a real directory, not a symlink.
	fi, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("lstat dst: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("dst is still a symlink; the root guard must replace it with a real directory")
	}
	if !fi.IsDir() {
		t.Errorf("dst must be a real directory after the root guard, got mode %v", fi.Mode())
	}
	// (c) Source content was copied into the real dst, NOT into the symlink target.
	if data, err := os.ReadFile(filepath.Join(dst, "settings.json")); err != nil || string(data) != `{"ok":true}` {
		t.Fatalf("source not copied into real dst: data=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(victim, "settings.json")); !os.IsNotExist(err) {
		t.Errorf("source must not be copied into the symlink target: Stat err=%v", err)
	}
}

// TestCleanCopyOverlayTargetExcluding_IntermediateSymlink is the regression for
// the intermediate-component vector (distinct from the leaf-root-symlink and
// child-symlink cases above). Copyoverlay targets are NESTED - e.g.
// ~/.config/Claude, with nothing mounted at ~/.config itself - and a leaf-only
// os.Lstat FOLLOWS the intermediate component. A prior untrusted run can replace
// ~/.config wholesale with a symlink to the project dir (or another read-write
// mount); the leaf then resolves through the link to a real directory, so a
// leaf-only guard wrongly passes and the ROOT-run clean+copy operate through the
// link. The full-path guard must remove the intermediate link (never its target),
// recreate it as a real directory, and confine clean+copy to the real target.
func TestCleanCopyOverlayTargetExcluding_IntermediateSymlink(t *testing.T) {
	base := t.TempDir()

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "settings.json"), []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// The victim dir the intermediate symlink points at, standing in for the
	// project dir or another read-write mount. It already contains a real dir at
	// the LEAF name with a file: this is what defeats a leaf-only guard - Lstat of
	// dst resolves .config through the link and sees a real victim/Claude dir.
	victim := t.TempDir()
	if err := os.WriteFile(filepath.Join(victim, "important.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(victim, "Claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "Claude", "victim-file"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Plant the INTERMEDIATE component as a symlink; the leaf target is nested one
	// level below it.
	if err := os.Symlink(victim, filepath.Join(base, ".config")); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(base, ".config", "Claude")

	if err := cleanCopyOverlayTargetExcluding(base, dst, map[string]bool{}); err != nil {
		t.Fatalf("cleanCopyOverlayTargetExcluding: %v", err)
	}
	if err := copyDir(base, src, dst); err != nil {
		t.Fatalf("copyDir: %v", err)
	}

	// (a) The victim dir's original files are NOT deleted through the intermediate
	// symlink (neither the file beside the leaf nor the one inside victim/Claude).
	if _, err := os.Stat(filepath.Join(victim, "important.txt")); err != nil {
		t.Errorf("victim file must not be deleted through the intermediate symlink: %v", err)
	}
	if _, err := os.Stat(filepath.Join(victim, "Claude", "victim-file")); err != nil {
		t.Errorf("victim nested file must not be deleted through the intermediate symlink: %v", err)
	}
	// (b) The intermediate component is now a real directory, not a symlink.
	fi, err := os.Lstat(filepath.Join(base, ".config"))
	if err != nil {
		t.Fatalf("lstat intermediate: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("intermediate .config is still a symlink; the guard must replace it with a real directory")
	}
	if !fi.IsDir() {
		t.Errorf("intermediate .config must be a real directory, got mode %v", fi.Mode())
	}
	// (c) Source content landed in the real nested target, NOT in the victim.
	if data, err := os.ReadFile(filepath.Join(base, ".config", "Claude", "settings.json")); err != nil || string(data) != `{"ok":true}` {
		t.Fatalf("source not copied into the real nested target: data=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(victim, "Claude", "settings.json")); !os.IsNotExist(err) {
		t.Errorf("source must not be copied into the victim through the symlink: Stat err=%v", err)
	}
}

// TestCopyDir_IntermediateSymlink proves the copy half's guard repairs an
// intermediate symlink on its own (no preceding clean), so both entry points are
// independently protected.
func TestCopyDir_IntermediateSymlink(t *testing.T) {
	base := t.TempDir()

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "settings.json"), []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	victim := t.TempDir()
	if err := os.MkdirAll(filepath.Join(victim, "Claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "Claude", "victim-file"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(base, ".config")); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(base, ".config", "Claude")

	if err := copyDir(base, src, dst); err != nil {
		t.Fatalf("copyDir: %v", err)
	}

	fi, err := os.Lstat(filepath.Join(base, ".config"))
	if err != nil {
		t.Fatalf("lstat intermediate: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		t.Errorf("intermediate .config must be a real directory after copyDir, got mode %v", fi.Mode())
	}
	if _, err := os.Stat(filepath.Join(victim, "Claude", "victim-file")); err != nil {
		t.Errorf("victim file must survive copyDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(victim, "Claude", "settings.json")); !os.IsNotExist(err) {
		t.Errorf("source must not be copied into the victim through the symlink: Stat err=%v", err)
	}
	if data, err := os.ReadFile(filepath.Join(base, ".config", "Claude", "settings.json")); err != nil || string(data) != `{"ok":true}` {
		t.Fatalf("source not copied into the real nested target: data=%q err=%v", data, err)
	}
}

// TestEnsureRealDirPath_FailsClosed asserts the guard rejects a dst that is not
// strictly under base rather than silently operating outside the sandbox home.
func TestEnsureRealDirPath_FailsClosed(t *testing.T) {
	base := t.TempDir()
	for _, dst := range []string{
		base,                             // equal to base
		filepath.Dir(base),               // above base
		filepath.Join(base, "..", "sib"), // escapes base via ..
	} {
		if err := ensureRealDirPath(base, dst); err == nil {
			t.Errorf("ensureRealDirPath(%q, %q) = nil, want fail-closed error", base, dst)
		}
	}
}

// TestCleanCopyOverlayTargetExcluding_MissingTarget: a target that does not yet
// exist (first run) is a no-op, not an error - copyDir creates it afterwards.
func TestCleanCopyOverlayTargetExcluding_MissingTarget(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "does-not-exist")
	if err := cleanCopyOverlayTargetExcluding(filepath.Dir(dst), dst, map[string]bool{}); err != nil {
		t.Fatalf("expected nil for missing target, got %v", err)
	}
}

func TestCopyDir_EmptySrc(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	if err := copyDir(filepath.Dir(dst), src, dst); err != nil {
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
