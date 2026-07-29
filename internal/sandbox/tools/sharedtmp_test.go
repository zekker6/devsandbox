package tools

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// registerSibling fakes a second devsandbox process holding this sandbox home,
// using the PID of a process that is certainly alive: this test binary.
func registerSibling(t *testing.T, sandboxHome string, pid int) {
	t.Helper()
	dir := filepath.Join(sandboxHome, runDirName, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeFileAt(t *testing.T, path, content string, modTime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if !modTime.IsZero() {
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}
}

// ageTree back-dates every entry under root so a prune sees it as stale.
func ageTree(t *testing.T, root string, when time.Time) {
	t.Helper()
	if err := filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, when, when)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSharedTmpSessionID_DeterministicAndDistinct(t *testing.T) {
	a := sharedTmpSessionID("/some/host/path/session-a")
	if a == "" {
		t.Fatal("sessionID returned empty string")
	}
	if again := sharedTmpSessionID("/some/host/path/session-a"); again != a {
		t.Errorf("sessionID not deterministic: %q vs %q", a, again)
	}
	if b := sharedTmpSessionID("/some/host/path/session-b"); a == b {
		t.Errorf("sessionID collision for distinct inputs: %q", a)
	}
}

func TestSharedTmpPath_UnderHomeAndRoot(t *testing.T) {
	got := SharedTmpPath("/home/alice", "/host/sessions/abc")
	root := SharedTmpRoot("/home/alice")
	if filepath.Dir(got) != root {
		t.Errorf("SharedTmpPath = %q, want a child of root %q", got, root)
	}
	if want := "/home/alice/.cache/devsandbox/tmp"; root != want {
		t.Errorf("SharedTmpRoot = %q, want %q", root, want)
	}
}

func TestSharedTmpBinding_WriteThroughAtIdenticalPath(t *testing.T) {
	bs := SharedTmpBinding("/home/alice", "/host/sessions/abc")
	if len(bs) != 1 {
		t.Fatalf("want 1 binding, got %d", len(bs))
	}
	b := bs[0]
	want := SharedTmpPath("/home/alice", "/host/sessions/abc")
	if b.Source != want {
		t.Errorf("Source = %q, want %q", b.Source, want)
	}
	if b.Dest != want {
		t.Errorf("Dest = %q, want %q (must equal Source — host-side helpers receive the path as literal data)", b.Dest, want)
	}
	if b.Type != MountBind {
		t.Errorf("Type = %q, want MountBind (%q) — an overlay would keep the sandbox's writes from the host", b.Type, MountBind)
	}
	if b.ReadOnly {
		t.Error("binding must stay writable: the sandbox creates the files the host reads")
	}
	if b.Category != CategoryRuntime {
		t.Errorf("Category = %q, want CategoryRuntime (%q)", b.Category, CategoryRuntime)
	}
}

func TestSharedTmpBindingAndEnv_EmptyArgs_ReturnNil(t *testing.T) {
	if got := SharedTmpBinding("", "/host"); got != nil {
		t.Errorf("SharedTmpBinding(\"\", _) = %v, want nil", got)
	}
	if got := SharedTmpBinding("/home/alice", ""); got != nil {
		t.Errorf("SharedTmpBinding(_, \"\") = %v, want nil", got)
	}
	if got := SharedTmpEnv("", "/host"); got != nil {
		t.Errorf("SharedTmpEnv(\"\", _) = %v, want nil", got)
	}
	if got := SharedTmpEnv("/home/alice", ""); got != nil {
		t.Errorf("SharedTmpEnv(_, \"\") = %v, want nil", got)
	}
}

func TestSharedTmpEnv_ExportsTmpdir(t *testing.T) {
	envs := SharedTmpEnv("/home/alice", "/host/sessions/abc")
	if len(envs) != 1 {
		t.Fatalf("want 1 env var, got %d: %v", len(envs), envs)
	}
	e := envs[0]
	if e.Name != "TMPDIR" {
		t.Errorf("Name = %q, want TMPDIR", e.Name)
	}
	if want := SharedTmpPath("/home/alice", "/host/sessions/abc"); e.Value != want {
		t.Errorf("Value = %q, want %q", e.Value, want)
	}
	if e.FromHost {
		t.Error("FromHost should be false for a static value")
	}
}

func TestPrepareSharedTmp_CreatesDirWithCorrectMode(t *testing.T) {
	homeDir := t.TempDir()
	sandboxHome := t.TempDir()

	if err := prepareSharedTmp(homeDir, sandboxHome, nil); err != nil {
		t.Fatalf("prepareSharedTmp: %v", err)
	}

	dir := SharedTmpPath(homeDir, sandboxHome)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a dir", dir)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("mode = %o, want 0700", got)
	}
}

// TestPrepareSharedTmp_TightensLooseModeOnExistingDir covers the case MkdirAll
// silently skips: the directory already exists with a permissive mode.
func TestPrepareSharedTmp_TightensLooseModeOnExistingDir(t *testing.T) {
	homeDir := t.TempDir()
	sandboxHome := t.TempDir()
	dir := SharedTmpPath(homeDir, sandboxHome)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}

	if err := prepareSharedTmp(homeDir, sandboxHome, nil); err != nil {
		t.Fatalf("prepareSharedTmp: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("mode = %o, want 0700", got)
	}
}

// TestPrepareSharedTmp_ColdStartWipesContents is the reason this change exists:
// $TMPDIR points here, so without a wipe every temporary file the sandbox ever
// wrote survives on the host forever.
func TestPrepareSharedTmp_ColdStartWipesContents(t *testing.T) {
	homeDir := t.TempDir()
	sandboxHome := t.TempDir()
	dir := SharedTmpPath(homeDir, sandboxHome)
	writeFileAt(t, filepath.Join(dir, "go-build123", "obj", "a.o"), "junk", time.Time{})
	writeFileAt(t, filepath.Join(dir, "claude-1000", "proj", "uuid", "scratch"), "junk", time.Time{})

	if err := prepareSharedTmp(homeDir, sandboxHome, nil); err != nil {
		t.Fatalf("prepareSharedTmp: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("cold start left %d entries, want 0: %v", len(entries), entries)
	}
}

// TestPrepareSharedTmp_ColdStartWipesReadOnlyTree guards the failure mode a
// plain os.RemoveAll has: unlinking a child needs write permission on its
// parent, and build and test temporaries do lay down 0555 directories.
func TestPrepareSharedTmp_ColdStartWipesReadOnlyTree(t *testing.T) {
	homeDir := t.TempDir()
	sandboxHome := t.TempDir()
	dir := SharedTmpPath(homeDir, sandboxHome)
	locked := filepath.Join(dir, "modcache", "pkg")
	writeFileAt(t, filepath.Join(locked, "file"), "junk", time.Time{})
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	if err := prepareSharedTmp(homeDir, sandboxHome, nil); err != nil {
		t.Fatalf("prepareSharedTmp: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "modcache")); !os.IsNotExist(err) {
		t.Errorf("read-only tree survived the wipe: err=%v", err)
	}
}

// TestPrepareSharedTmp_LiveSiblingKeepsRecentContent locks in the invariant an
// earlier version broke by wiping unconditionally: a concurrent session for the
// same project is still using these files, and a non-recursive mkdir in a
// running tenant fails with ENOENT once they vanish.
func TestPrepareSharedTmp_LiveSiblingKeepsRecentContent(t *testing.T) {
	homeDir := t.TempDir()
	sandboxHome := t.TempDir()
	// The parent PID is alive and never equal to our own.
	registerSibling(t, sandboxHome, os.Getppid())

	dir := SharedTmpPath(homeDir, sandboxHome)
	marker := filepath.Join(dir, "claude-1000", "proj", "uuid", "scratch")
	writeFileAt(t, marker, "important", time.Time{})

	if err := prepareSharedTmp(homeDir, sandboxHome, nil); err != nil {
		t.Fatalf("prepareSharedTmp: %v", err)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("live sibling's file removed: %v", err)
	}
	if string(data) != "important" {
		t.Errorf("content changed: %q", data)
	}
}

// TestPrepareSharedTmp_LiveSiblingPrunesStaleContent is the backstop for the
// case above: a sibling blocks the wipe, so age is the only signal separating
// its working files from residue of runs already gone.
func TestPrepareSharedTmp_LiveSiblingPrunesStaleContent(t *testing.T) {
	homeDir := t.TempDir()
	sandboxHome := t.TempDir()
	registerSibling(t, sandboxHome, os.Getppid())

	dir := SharedTmpPath(homeDir, sandboxHome)
	stale := filepath.Join(dir, "go-build-old")
	fresh := filepath.Join(dir, "go-build-new")
	writeFileAt(t, filepath.Join(stale, "a.o"), "junk", time.Time{})
	writeFileAt(t, filepath.Join(fresh, "a.o"), "junk", time.Time{})
	ageTree(t, stale, time.Now().Add(-sharedTmpStaleAge-time.Hour))

	if err := prepareSharedTmp(homeDir, sandboxHome, nil); err != nil {
		t.Fatalf("prepareSharedTmp: %v", err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale entry survived: err=%v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh entry removed: %v", err)
	}
}

// TestPrepareSharedTmp_PruneChecksWholeSubtree covers the trap in using a
// directory's own mtime: it moves only when a direct child changes, so a tenant
// writing deep inside a tree it created days ago looks stale to a shallow
// check — and that tenant belongs to the sibling this prune protects.
func TestPrepareSharedTmp_PruneChecksWholeSubtree(t *testing.T) {
	homeDir := t.TempDir()
	sandboxHome := t.TempDir()
	registerSibling(t, sandboxHome, os.Getppid())

	dir := SharedTmpPath(homeDir, sandboxHome)
	tree := filepath.Join(dir, "claude-1000")
	deep := filepath.Join(tree, "proj", "uuid", "scratch")
	writeFileAt(t, deep, "in use", time.Time{})
	ageTree(t, tree, time.Now().Add(-sharedTmpStaleAge-time.Hour))
	// Only the deepest file is recent; every directory above it looks stale.
	now := time.Now()
	if err := os.Chtimes(deep, now, now); err != nil {
		t.Fatal(err)
	}

	if err := prepareSharedTmp(homeDir, sandboxHome, nil); err != nil {
		t.Fatalf("prepareSharedTmp: %v", err)
	}

	if _, err := os.Stat(deep); err != nil {
		t.Errorf("tree with recent activity deep inside was pruned: %v", err)
	}
}

// TestPrepareSharedTmp_LeavesSiblingProjects checks the wipe is scoped to this
// sandbox home. Other projects' directories share the root.
func TestPrepareSharedTmp_LeavesSiblingProjects(t *testing.T) {
	homeDir := t.TempDir()
	sandboxHome := t.TempDir()
	other := filepath.Join(SharedTmpRoot(homeDir), "deadbeefcafe")
	writeFileAt(t, filepath.Join(other, "keep"), "x", time.Time{})

	if err := prepareSharedTmp(homeDir, sandboxHome, nil); err != nil {
		t.Fatalf("prepareSharedTmp: %v", err)
	}

	if _, err := os.Stat(filepath.Join(other, "keep")); err != nil {
		t.Errorf("another project's content was removed: %v", err)
	}
}

// TestPrepareSharedTmp_ReclaimsLegacyDir covers the rename: the pre-rename
// directory for this same sandbox home is dead once no current devsandbox binds
// it, and it is where the reported disk usage accumulated.
func TestPrepareSharedTmp_ReclaimsLegacyDir(t *testing.T) {
	homeDir := t.TempDir()
	sandboxHome := t.TempDir()
	legacy := legacySharedTmpPath(homeDir, sandboxHome)
	writeFileAt(t, filepath.Join(legacy, "go-build999", "a.o"), "junk", time.Time{})

	if err := prepareSharedTmp(homeDir, sandboxHome, nil); err != nil {
		t.Fatalf("prepareSharedTmp: %v", err)
	}

	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy dir survived: err=%v", err)
	}
	// Its root goes too, once it holds nothing else.
	if _, err := os.Stat(filepath.Join(homeDir, legacySharedTmpRelPath)); !os.IsNotExist(err) {
		t.Errorf("emptied legacy root survived: err=%v", err)
	}
}

// TestPrepareSharedTmp_KeepsLegacyRootWithOtherProjects makes sure reclaiming
// the legacy layout never reaches past this sandbox home.
func TestPrepareSharedTmp_KeepsLegacyRootWithOtherProjects(t *testing.T) {
	homeDir := t.TempDir()
	sandboxHome := t.TempDir()
	otherLegacy := filepath.Join(homeDir, legacySharedTmpRelPath, "deadbeefcafe")
	writeFileAt(t, filepath.Join(otherLegacy, "keep"), "x", time.Time{})
	writeFileAt(t, filepath.Join(legacySharedTmpPath(homeDir, sandboxHome), "junk"), "j", time.Time{})

	if err := prepareSharedTmp(homeDir, sandboxHome, nil); err != nil {
		t.Fatalf("prepareSharedTmp: %v", err)
	}

	if _, err := os.Stat(filepath.Join(otherLegacy, "keep")); err != nil {
		t.Errorf("another project's legacy content was removed: %v", err)
	}
}

// TestPrepareSharedTmp_RegistersBeforeObserving pins the ordering that makes
// two simultaneous starts safe: each has to be visible to the other, so both
// fall back to the conservative prune instead of both concluding they are alone.
func TestPrepareSharedTmp_RegistersBeforeObserving(t *testing.T) {
	homeDir := t.TempDir()
	sandboxHome := t.TempDir()

	if err := prepareSharedTmp(homeDir, sandboxHome, nil); err != nil {
		t.Fatalf("prepareSharedTmp: %v", err)
	}

	self := filepath.Join(sandboxHome, runDirName, strconv.Itoa(os.Getpid()))
	if _, err := os.Stat(self); err != nil {
		t.Errorf("process did not register its own run dir: %v", err)
	}
}

// TestHasLiveSiblingSession_IgnoresSelfAndDeadPIDs checks the two entries that
// must never count as a sibling.
func TestHasLiveSiblingSession_IgnoresSelfAndDeadPIDs(t *testing.T) {
	sandboxHome := t.TempDir()
	registerSibling(t, sandboxHome, os.Getpid())
	if hasLiveSiblingSession(sandboxHome) {
		t.Error("own run dir counted as a sibling")
	}

	registerSibling(t, sandboxHome, reapedPID(t))
	if hasLiveSiblingSession(sandboxHome) {
		t.Error("dead PID counted as a sibling")
	}

	registerSibling(t, sandboxHome, os.Getppid())
	if !hasLiveSiblingSession(sandboxHome) {
		t.Error("live sibling PID not detected")
	}
}

// TestHasLiveSiblingSession_UncertainAnswerBlocksWipe: an unreadable run
// directory leaves the question unanswered, and unanswered must not authorize
// deleting another session's files.
func TestHasLiveSiblingSession_UncertainAnswerBlocksWipe(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	sandboxHome := t.TempDir()
	runRoot := filepath.Join(sandboxHome, runDirName)
	if err := os.MkdirAll(runRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(runRoot, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(runRoot, 0o700) })

	if !hasLiveSiblingSession(sandboxHome) {
		t.Error("unreadable run dir must be treated as a live sibling")
	}
}

func TestPrepareSharedTmp_EmptyArgsIsNoop(t *testing.T) {
	if err := prepareSharedTmp("", "/host", nil); err != nil {
		t.Errorf("prepareSharedTmp(\"\", _) = %v, want nil", err)
	}
	if err := prepareSharedTmp("/home/alice", "", nil); err != nil {
		t.Errorf("prepareSharedTmp(_, \"\") = %v, want nil", err)
	}
}

func TestRevdiff_DeclaresSharedTmp(t *testing.T) {
	var tool Tool = &Revdiff{}
	if _, ok := tool.(ToolWithSharedTmp); !ok {
		t.Error("revdiff must declare ToolWithSharedTmp: its launcher writes sentinel and output files under $TMPDIR")
	}
	// The mount and the export are emitted centrally, never by the tool.
	if bs := (&Revdiff{}).Bindings("/home/alice", "/host/sessions/abc"); bs != nil {
		t.Errorf("Bindings must stay empty, got %v — a second emitter would mount the same destination twice", bs)
	}
	if envs := (&Revdiff{}).Environment("/home/alice", "/host/sessions/abc"); envs != nil {
		t.Errorf("Environment must stay empty, got %v", envs)
	}
}
