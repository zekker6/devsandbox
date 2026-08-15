package overlay

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// writeFile is a small helper — the test uses it often.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mkWhiteout creates an overlayfs whiteout marker - a character device with
// rdev 0 - at path. mknod of a device node needs CAP_MKNOD over the filesystem,
// which an ordinary CI user does not hold, so the test skips rather than fails
// when the kernel refuses. The layerMerge tests below cover the same pruning
// without any privilege.
func mkWhiteout(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mknod(path, syscall.S_IFCHR|0o600, 0); err != nil {
		t.Skipf("cannot create whiteout device at %s (needs CAP_MKNOD): %v", path, err)
	}
}

// relPaths lists a plan's operations in emission order.
func relPaths(ops []Operation) []string {
	out := make([]string, 0, len(ops))
	for _, op := range ops {
		out = append(out, op.RelPath)
	}
	return out
}

func TestBuildPlan_WhiteoutPrunesEarlierDescendants(t *testing.T) {
	tmp := t.TempDir()
	upperA := filepath.Join(tmp, "upperA")
	upperB := filepath.Join(tmp, "upperB")
	host := filepath.Join(tmp, "host")
	writeFile(t, filepath.Join(host, "d", "file"), "host")
	writeFile(t, filepath.Join(upperA, "d", "file"), "A")
	writeFile(t, filepath.Join(upperA, "d", "sub", "deep"), "A")
	writeFile(t, filepath.Join(upperA, "keep.txt"), "A")
	mkWhiteout(t, filepath.Join(upperB, "d"))

	sources := []UpperSource{
		{Kind: UpperPrimary, Path: upperA, SandboxID: "s1", SourceLabel: "s1:primary"},
		{Kind: UpperSession, Path: upperB, SandboxID: "s1", SessionID: "abc", SourceLabel: "s1:session/abc"},
	}
	plan, err := BuildPlan(sources, host)
	if err != nil {
		t.Fatal(err)
	}

	prefix := "d" + string(filepath.Separator)
	for _, op := range plan.Operations {
		if strings.HasPrefix(op.RelPath, prefix) {
			t.Errorf("stale descendant of whiteouted directory: %+v", op)
		}
	}
	for id, ops := range plan.BySandbox {
		for _, op := range ops {
			if strings.HasPrefix(op.RelPath, prefix) {
				t.Errorf("stale descendant in BySandbox[%s]: %+v", id, op)
			}
		}
	}

	var sawDelete, sawKeep bool
	for _, op := range plan.Operations {
		switch op.RelPath {
		case "d":
			if op.Kind != OpDelete {
				t.Errorf("want OpDelete for d, got %v", op.Kind)
			}
			sawDelete = true
		case "keep.txt":
			sawKeep = true
		}
	}
	if !sawDelete {
		t.Errorf("no delete for whiteouted d; ops=%v", relPaths(plan.Operations))
	}
	if !sawKeep {
		t.Errorf("unrelated entry dropped; ops=%v", relPaths(plan.Operations))
	}
}

func TestBuildPlan_WhiteoutThenRecreatedByLaterUpper(t *testing.T) {
	tmp := t.TempDir()
	upperA := filepath.Join(tmp, "upperA")
	upperB := filepath.Join(tmp, "upperB")
	upperC := filepath.Join(tmp, "upperC")
	host := filepath.Join(tmp, "host")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(upperA, "d", "old.txt"), "A")
	mkWhiteout(t, filepath.Join(upperB, "d"))
	writeFile(t, filepath.Join(upperC, "d", "new.txt"), "C")

	sources := []UpperSource{
		{Kind: UpperPrimary, Path: upperA, SandboxID: "s1"},
		{Kind: UpperSession, Path: upperB, SandboxID: "s1", SessionID: "b"},
		{Kind: UpperSession, Path: upperC, SandboxID: "s1", SessionID: "c"},
	}
	plan, err := BuildPlan(sources, host)
	if err != nil {
		t.Fatal(err)
	}

	got := relPaths(plan.Operations)
	if slices.Contains(got, filepath.Join("d", "old.txt")) {
		t.Errorf("entry hidden by an intermediate whiteout was planned; ops=%v", got)
	}
	if !slices.Contains(got, filepath.Join("d", "new.txt")) {
		t.Errorf("entry from the recreating upper missing; ops=%v", got)
	}
	for _, op := range plan.Operations {
		if op.RelPath == "d" && op.Kind == OpDelete {
			t.Errorf("directory recreated by a later upper must not be deleted: %+v", op)
		}
	}
}

func TestBuildPlan_DeterministicOperationOrder(t *testing.T) {
	tmp := t.TempDir()
	upper := filepath.Join(tmp, "upper")
	host := filepath.Join(tmp, "host")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"a.txt", "b.txt",
		"c/1.txt", "c/2.txt", "c/d/3.txt", "c/d/e/4.txt",
		"f/g.txt", "h.txt",
		"i/j/k.txt", "i/j/l.txt",
		"m.txt", "n/o.txt",
	} {
		writeFile(t, filepath.Join(upper, filepath.FromSlash(rel)), rel)
	}

	sources := []UpperSource{{Kind: UpperPrimary, Path: upper, SandboxID: "s1"}}
	var first []string
	for i := range 5 {
		plan, err := BuildPlan(sources, host)
		if err != nil {
			t.Fatal(err)
		}
		got := relPaths(plan.Operations)
		if i == 0 {
			first = got
			continue
		}
		if !slices.Equal(first, got) {
			t.Fatalf("operation order not deterministic:\nrun 0: %v\nrun %d: %v", first, i, got)
		}
	}

	index := make(map[string]int, len(first))
	for i, rel := range first {
		index[rel] = i
	}
	for rel, idx := range index {
		parent := filepath.Dir(rel)
		if parent == "." {
			continue
		}
		pIdx, ok := index[parent]
		if !ok {
			t.Errorf("no operation for parent %q of %q", parent, rel)
			continue
		}
		if pIdx > idx {
			t.Errorf("parent %q (index %d) emitted after child %q (index %d)", parent, pIdx, rel, idx)
		}
	}
}

func TestLayerMerge_WhiteoutPrunesDescendants(t *testing.T) {
	m := newLayerMerge()
	m.add(layerEntry{rel: "d", isDir: true})
	m.add(layerEntry{rel: filepath.Join("d", "file")})
	m.add(layerEntry{rel: filepath.Join("d", "sub", "deep")})
	m.add(layerEntry{rel: "d-sibling", isDir: true})
	m.add(layerEntry{rel: "other", isDir: true})

	m.add(layerEntry{rel: "d", isWhiteout: true})

	want := []string{"d", "d-sibling", "other"}
	if got := m.rels(); !slices.Equal(got, want) {
		t.Fatalf("after whiteout of d: got %v, want %v", got, want)
	}
	if e := m.byRel["d"]; !e.isWhiteout {
		t.Errorf("whiteout entry did not win for d: %+v", e)
	}
}

func TestLayerMerge_OpaqueDirPrunesDescendants(t *testing.T) {
	m := newLayerMerge()
	m.add(layerEntry{rel: "d", isDir: true})
	m.add(layerEntry{rel: filepath.Join("d", "old.txt")})

	// A later upper recreates d, opaque, with its own child.
	m.add(layerEntry{rel: "d", isOpaque: true})
	m.add(layerEntry{rel: filepath.Join("d", "new.txt")})

	want := []string{"d", filepath.Join("d", "new.txt")}
	if got := m.rels(); !slices.Equal(got, want) {
		t.Fatalf("after opaque d: got %v, want %v", got, want)
	}
}

func TestLayerMerge_PlainDirDoesNotPrune(t *testing.T) {
	m := newLayerMerge()
	m.add(layerEntry{rel: "d", isDir: true})
	m.add(layerEntry{rel: filepath.Join("d", "old.txt")})

	// A non-opaque directory in a later upper merges with the lower ones.
	m.add(layerEntry{rel: "d", isDir: true})
	m.add(layerEntry{rel: filepath.Join("d", "new.txt")})

	want := []string{"d", filepath.Join("d", "new.txt"), filepath.Join("d", "old.txt")}
	if got := m.rels(); !slices.Equal(got, want) {
		t.Fatalf("plain directory pruned lower entries: got %v, want %v", got, want)
	}
}

// TestLayerMerge_NonDirShadowingPrunes covers the third way a higher upper
// hides a lower subtree, the one that carries no marker: `rm -rf d && echo x >
// d` leaves `d` a regular file with its whiteout replaced, and overlayfs stops
// merging the path because only one side is a directory. Keeping `d/old.txt`
// here emitted a plan that wrote `d` as a file and then tried to create a child
// under it, dying at `mkdir d: not a directory` on the first run and on every
// retry after it.
func TestLayerMerge_NonDirShadowingPrunes(t *testing.T) {
	m := newLayerMerge()
	m.add(layerEntry{rel: "d", isDir: true})
	m.add(layerEntry{rel: filepath.Join("d", "old.txt")})

	m.add(layerEntry{rel: "d"})

	want := []string{"d"}
	if got := m.rels(); !slices.Equal(got, want) {
		t.Fatalf("non-directory over a directory: got %v, want %v", got, want)
	}
}

// TestBuildPlan_NonDirInLaterUpperHidesEarlierDir is the same case end to end,
// and it is the one that mattered: the plan used to carry both `d` (a file) and
// `d/old.txt`, so Apply created the file and then failed ENOTDIR creating a
// child under it - aborting with earlier operations committed and failing
// identically on every retry, against the resumability Apply documents.
func TestBuildPlan_NonDirInLaterUpperHidesEarlierDir(t *testing.T) {
	for _, tc := range []struct {
		name  string
		place func(t *testing.T, path string)
	}{
		{"regular file", func(t *testing.T, path string) { writeFile(t, path, "shadow") }},
		{"symlink", func(t *testing.T, path string) {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/etc/hostname", path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			upperA := filepath.Join(tmp, "upperA")
			upperB := filepath.Join(tmp, "upperB")
			host := filepath.Join(tmp, "host")
			if err := os.MkdirAll(host, 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(upperA, "d", "old.txt"), "A")
			tc.place(t, filepath.Join(upperB, "d"))

			plan, err := BuildPlan([]UpperSource{
				{Kind: UpperPrimary, Path: upperA, SandboxID: "s1"},
				{Kind: UpperSession, Path: upperB, SandboxID: "s1", SessionID: "b"},
			}, host)
			if err != nil {
				t.Fatal(err)
			}

			got := relPaths(plan.Operations)
			if slices.Contains(got, filepath.Join("d", "old.txt")) {
				t.Errorf("entry hidden by a non-directory in a later upper was planned; ops=%v", got)
			}
			if err := Apply(plan); err != nil {
				t.Fatalf("Apply on a plan with a shadowed directory: %v", err)
			}
		})
	}
}

func TestBuildPlan_SingleUpper_Create(t *testing.T) {
	tmp := t.TempDir()
	upper := filepath.Join(tmp, "upper")
	host := filepath.Join(tmp, "host")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(upper, "a.jsonl"), "new")

	sources := []UpperSource{{Kind: UpperPrimary, Path: upper, SandboxID: "s1"}}
	plan, err := BuildPlan(sources, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 1 {
		t.Fatalf("want 1 op, got %d: %+v", len(plan.Operations), plan.Operations)
	}
	op := plan.Operations[0]
	if op.Kind != OpCreate || op.RelPath != "a.jsonl" || op.Bytes != 3 {
		t.Errorf("unexpected op: %+v", op)
	}
}

func TestBuildPlan_Overwrite_WhenHostHasFile(t *testing.T) {
	tmp := t.TempDir()
	upper := filepath.Join(tmp, "upper")
	host := filepath.Join(tmp, "host")
	writeFile(t, filepath.Join(host, "a.jsonl"), "old")
	writeFile(t, filepath.Join(upper, "a.jsonl"), "new")

	sources := []UpperSource{{Kind: UpperPrimary, Path: upper, SandboxID: "s1"}}
	plan, err := BuildPlan(sources, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 1 || plan.Operations[0].Kind != OpOverwrite {
		t.Fatalf("want OpOverwrite, got %+v", plan.Operations)
	}
}

func TestBuildPlan_StackedUppers_LastWins(t *testing.T) {
	tmp := t.TempDir()
	upperA := filepath.Join(tmp, "upperA")
	upperB := filepath.Join(tmp, "upperB")
	host := filepath.Join(tmp, "host")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(upperA, "m.md"), "A-version")
	writeFile(t, filepath.Join(upperB, "m.md"), "B-version")

	sources := []UpperSource{
		{Kind: UpperPrimary, Path: upperA, SandboxID: "s1", SourceLabel: "s1:primary"},
		{Kind: UpperSession, Path: upperB, SandboxID: "s1", SessionID: "abc", SourceLabel: "s1:session/abc"},
	}
	plan, err := BuildPlan(sources, host)
	if err != nil {
		t.Fatal(err)
	}

	// Find the m.md operation (the plan may also include a parent dir op).
	var found *Operation
	for i := range plan.Operations {
		if plan.Operations[i].RelPath == "m.md" {
			found = &plan.Operations[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no op for m.md; ops=%+v", plan.Operations)
	}
	if found.Source != filepath.Join(upperB, "m.md") {
		t.Errorf("later upper should win, got source=%s", found.Source)
	}
}

func TestBuildPlan_MissingHost_PlansCreate(t *testing.T) {
	tmp := t.TempDir()
	upper := filepath.Join(tmp, "upper")
	host := filepath.Join(tmp, "host-not-yet") // doesn't exist
	writeFile(t, filepath.Join(upper, "m.md"), "x")

	sources := []UpperSource{{Kind: UpperPrimary, Path: upper, SandboxID: "s1"}}
	plan, err := BuildPlan(sources, host)
	if err != nil {
		t.Fatal(err)
	}
	var mOp *Operation
	for i := range plan.Operations {
		if plan.Operations[i].RelPath == "m.md" {
			mOp = &plan.Operations[i]
			break
		}
	}
	if mOp == nil || mOp.Kind != OpCreate {
		t.Fatalf("missing host should plan Create for m.md, got %+v", plan.Operations)
	}
}

func TestBuildPlan_Symlink_PreservedAsSymlink(t *testing.T) {
	tmp := t.TempDir()
	upper := filepath.Join(tmp, "upper")
	host := filepath.Join(tmp, "host")
	if err := os.MkdirAll(upper, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target-file", filepath.Join(upper, "link")); err != nil {
		t.Fatal(err)
	}

	sources := []UpperSource{{Kind: UpperPrimary, Path: upper, SandboxID: "s1"}}
	plan, err := BuildPlan(sources, host)
	if err != nil {
		t.Fatal(err)
	}
	var linkOp *Operation
	for i := range plan.Operations {
		if plan.Operations[i].RelPath == "link" {
			linkOp = &plan.Operations[i]
			break
		}
	}
	if linkOp == nil {
		t.Fatalf("no op for link; ops=%+v", plan.Operations)
	}
	if !linkOp.IsSymlink || linkOp.LinkTarget != "target-file" {
		t.Errorf("expected symlink with target-file, got %+v", *linkOp)
	}
}

func TestBuildPlan_GroupedBySandbox(t *testing.T) {
	tmp := t.TempDir()
	upperS1 := filepath.Join(tmp, "s1-upper")
	upperS2 := filepath.Join(tmp, "s2-upper")
	host := filepath.Join(tmp, "host")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(upperS1, "s1.jsonl"), "x")
	writeFile(t, filepath.Join(upperS2, "s2.jsonl"), "y")

	sources := []UpperSource{
		{Kind: UpperPrimary, Path: upperS1, SandboxID: "s1"},
		{Kind: UpperPrimary, Path: upperS2, SandboxID: "s2"},
	}
	plan, err := BuildPlan(sources, host)
	if err != nil {
		t.Fatal(err)
	}
	// Count only file ops per sandbox (parent-dir ops may be present).
	var s1Files, s2Files int
	for _, op := range plan.BySandbox["s1"] {
		if op.RelPath == "s1.jsonl" {
			s1Files++
		}
	}
	for _, op := range plan.BySandbox["s2"] {
		if op.RelPath == "s2.jsonl" {
			s2Files++
		}
	}
	if s1Files != 1 || s2Files != 1 {
		t.Fatalf("grouping wrong: s1Files=%d s2Files=%d BySandbox=%+v", s1Files, s2Files, plan.BySandbox)
	}
}

// TestBuildPlan_SkipsNonRegularEntries pins the entries the applier cannot
// promote. A FIFO makes copyFile's O_RDONLY open block forever, and a socket
// fails ENXIO partway through Apply and dies at the same operation on every
// retry - both reachable because the upper is sandbox-writable.
func TestBuildPlan_SkipsNonRegularEntries(t *testing.T) {
	upper := t.TempDir()
	host := t.TempDir()

	fifo := filepath.Join(upper, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(upper, "real.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("seed regular file: %v", err)
	}

	plan, err := BuildPlan([]UpperSource{{Path: upper, SandboxID: "s"}}, host)
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	for _, op := range plan.Operations {
		if op.RelPath == "pipe" {
			t.Errorf("planned an operation for a FIFO: %+v", op)
		}
	}
	if len(plan.Operations) != 1 || plan.Operations[0].RelPath != "real.txt" {
		t.Fatalf("regular file was not planned alongside the skip: %+v", plan.Operations)
	}

	// The plan must also apply cleanly - a FIFO reaching copyFile would hang
	// here rather than fail.
	if err := Apply(plan); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(host, "real.txt")); err != nil || string(got) != "keep" {
		t.Fatalf("regular file not migrated: %q %v", got, err)
	}
}

// TestBuildPlan_MarksHostDirReplacement covers what the dry-run preview has to
// say out loud: applying a file or a symlink over a host directory removes that
// directory recursively, including entries no operation in the plan puts back.
func TestBuildPlan_MarksHostDirReplacement(t *testing.T) {
	tmp := t.TempDir()
	upper := filepath.Join(tmp, "upper")
	host := filepath.Join(tmp, "host")

	// file over host directory
	writeFile(t, filepath.Join(upper, "conf"), "now a file")
	writeFile(t, filepath.Join(host, "conf", "keep.txt"), "host-only data")

	// symlink over host directory
	if err := os.MkdirAll(filepath.Join(upper, "link"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(upper, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/elsewhere", filepath.Join(upper, "link")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(host, "link", "keep.txt"), "host-only data")

	// file over host file, and directory over host directory: neither replaces
	writeFile(t, filepath.Join(upper, "plain.txt"), "new")
	writeFile(t, filepath.Join(host, "plain.txt"), "old")
	writeFile(t, filepath.Join(upper, "d", "x.txt"), "new")
	writeFile(t, filepath.Join(host, "d", "y.txt"), "old")

	plan, err := BuildPlan([]UpperSource{{Kind: UpperPrimary, Path: upper, SandboxID: "s1"}}, host)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	want := map[string]bool{
		"conf":      true,
		"link":      true,
		"plain.txt": false,
		"d":         false,
		"d/x.txt":   false,
	}
	for _, op := range plan.Operations {
		expected, known := want[op.RelPath]
		if !known {
			continue
		}
		if op.ReplacesHostDir != expected {
			t.Errorf("%s: ReplacesHostDir = %v, want %v", op.RelPath, op.ReplacesHostDir, expected)
		}
		delete(want, op.RelPath)
	}
	for rel := range want {
		t.Errorf("no operation emitted for %q", rel)
	}

	if got := plan.DirReplacements(); got != 2 {
		t.Errorf("DirReplacements() = %d, want 2", got)
	}
}

// TestBuildPlan_RuntimeEntryInLaterUpperHidesEarlierDir extends
// TestBuildPlan_NonDirInLaterUpperHidesEarlierDir to the forms the applier
// cannot promote.
//
// Skipping such an entry is not the same as ignoring it: a FIFO or a socket is
// still a non-directory in a higher upper, so it masks the lower subtree even
// though nothing is planned in its place. Returning early without pruning
// migrated the earlier upper's `d` and every `d/*` back onto the host - exactly
// the files the sandbox deleted with `rm -rf d`.
func TestBuildPlan_RuntimeEntryInLaterUpperHidesEarlierDir(t *testing.T) {
	for _, tc := range []struct {
		name  string
		place func(t *testing.T, path string) error
	}{
		{"fifo", func(_ *testing.T, path string) error { return syscall.Mkfifo(path, 0o600) }},
		{"socket", func(_ *testing.T, path string) error {
			l, err := net.Listen("unix", path)
			if err != nil {
				return err
			}
			// Closing a unix listener unlinks its socket by default, which would
			// leave the upper with no entry at all and quietly turn this into a
			// test of nothing. The socket has to outlive the listener, the way
			// the ones an agent leaves behind in a sandbox home do.
			if ul, ok := l.(*net.UnixListener); ok {
				ul.SetUnlinkOnClose(false)
			}
			return l.Close()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			upperA := filepath.Join(tmp, "upperA")
			upperB := filepath.Join(tmp, "upperB")
			host := filepath.Join(tmp, "host")
			if err := os.MkdirAll(host, 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(upperA, "d", "old.txt"), "A")
			if err := os.MkdirAll(upperB, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := tc.place(t, filepath.Join(upperB, "d")); err != nil {
				t.Skipf("cannot create %s: %v", tc.name, err)
			}

			plan, err := BuildPlan([]UpperSource{
				{Kind: UpperPrimary, Path: upperA, SandboxID: "s1"},
				{Kind: UpperSession, Path: upperB, SandboxID: "s1", SessionID: "b"},
			}, host)
			if err != nil {
				t.Fatal(err)
			}

			got := relPaths(plan.Operations)
			for _, rel := range []string{"d", filepath.Join("d", "old.txt")} {
				if slices.Contains(got, rel) {
					t.Errorf("entry %q hidden by a %s in a later upper was planned; ops=%v", rel, tc.name, got)
				}
			}
			if err := Apply(plan); err != nil {
				t.Fatalf("Apply: %v", err)
			}
		})
	}
}
