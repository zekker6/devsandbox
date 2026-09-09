package herdrstate_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"devsandbox/internal/herdrstate"
	"devsandbox/internal/sandbox"
	"devsandbox/internal/sandbox/tools"
)

// newRecord names a sandbox root it never creates. A test that needs the
// root to exist, as Prune's live case does, creates it itself.
func newRecord(t *testing.T, paneID string) herdrstate.Record {
	t.Helper()
	projectDir := t.TempDir()
	return herdrstate.Record{
		PaneID:      paneID,
		Agent:       "claude",
		ProjectDir:  projectDir,
		SandboxRoot: filepath.Join(t.TempDir(), sandbox.GenerateSandboxName(projectDir)),
	}
}

// recordPath spells the file Save writes for paneID under dir.
func recordPath(dir, paneID string) string {
	sum := sha256.Sum256([]byte(paneID))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".json")
}

func TestDefaultStoreUsesXDGStateHome(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	store, err := herdrstate.DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore: %v", err)
	}

	want := filepath.Join(stateHome, "devsandbox", "herdr-panes")
	if store.Dir() != want {
		t.Fatalf("Dir = %q, want %q", store.Dir(), want)
	}

	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("stat store dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("store dir mode = %o, want 700", perm)
	}
}

func TestDefaultStoreFallsBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", home)

	store, err := herdrstate.DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore: %v", err)
	}
	want := filepath.Join(home, ".local", "state", "devsandbox", "herdr-panes")
	if store.Dir() != want {
		t.Fatalf("Dir = %q, want %q", store.Dir(), want)
	}
}

func TestSaveWritesHashedFileWithRestrictivePerms(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "herdr-panes")
	store := herdrstate.NewStore(dir)

	rec := newRecord(t, "w1F:p1C")
	if err := store.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	want := recordPath(dir, rec.PaneID)
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("stat record: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("record mode = %o, want 600", perm)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("store dir mode = %o, want 700", perm)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 2 || entries[0].Name() != ".lock" || entries[1].Name() != filepath.Base(want) {
		t.Fatalf("got entries %v, want one record and the store lock", entries)
	}
}

func TestSaveHostilePaneIDStaysInsideStoreDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "herdr-panes")
	store := herdrstate.NewStore(dir)

	for _, paneID := range []string{
		"../../etc/passwd",
		"..",
		"/absolute/pane",
		"a/b/c",
		strings.Repeat("x", 4096),
	} {
		rec := newRecord(t, paneID)
		if err := store.Save(rec); err != nil {
			t.Fatalf("Save(%q): %v", paneID, err)
		}
		got, err := store.Load(paneID)
		if err != nil {
			t.Fatalf("Load(%q): %v", paneID, err)
		}
		if got.PaneID != paneID {
			t.Errorf("PaneID = %q, want %q", got.PaneID, paneID)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 6 {
		t.Fatalf("got %d entries, want 5 records and the store lock", len(entries))
	}
	for _, e := range entries {
		if e.Name() == ".lock" {
			continue
		}
		if e.IsDir() {
			t.Errorf("record %q is a directory", e.Name())
		}
		if !strings.HasSuffix(e.Name(), ".json") {
			t.Errorf("record %q is not a .json file", e.Name())
		}
	}

	// Nothing escaped into the parent of the store directory.
	parents, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	if len(parents) != 1 || parents[0].Name() != "herdr-panes" {
		t.Fatalf("store directory's parent has unexpected entries: %v", parents)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	store := herdrstate.NewStore(t.TempDir())
	rec := newRecord(t, "w1F:p1C")
	rec.UpdatedAt = time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)

	if err := store.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load(rec.PaneID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got.Version != herdrstate.Version {
		t.Errorf("Version = %d, want %d", got.Version, herdrstate.Version)
	}
	if got.PaneID != rec.PaneID || got.Agent != rec.Agent {
		t.Errorf("identity = %q/%q, want %q/%q", got.PaneID, got.Agent, rec.PaneID, rec.Agent)
	}
	if got.ProjectDir != rec.ProjectDir || got.SandboxRoot != rec.SandboxRoot {
		t.Errorf("paths = %q/%q, want %q/%q", got.ProjectDir, got.SandboxRoot, rec.ProjectDir, rec.SandboxRoot)
	}
	if !got.UpdatedAt.Equal(rec.UpdatedAt) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, rec.UpdatedAt)
	}
}

func TestSaveStampsUpdatedAtAndKeepsIdentity(t *testing.T) {
	store := herdrstate.NewStore(t.TempDir())
	rec := newRecord(t, "w1F:p1C")

	if err := store.Save(rec); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	first, err := store.Load(rec.PaneID)
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}
	if first.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt not stamped")
	}

	if err := store.Save(rec); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	second, err := store.Load(rec.PaneID)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}

	if second.UpdatedAt.Before(first.UpdatedAt) {
		t.Errorf("UpdatedAt went backwards: %v then %v", first.UpdatedAt, second.UpdatedAt)
	}
	if second.PaneID != first.PaneID || second.Agent != first.Agent ||
		second.ProjectDir != first.ProjectDir || second.SandboxRoot != first.SandboxRoot {
		t.Errorf("re-save changed identity: %+v then %+v", first, second)
	}
}

// TestConcurrentPanesKeepSeparateMappings covers the case the rollout notes
// call out as most likely to surprise: several panes running agents in the SAME
// project. The synthetic home is shared per project, so nothing but the pane ID
// distinguishes the records — a filename derived from anything coarser would let
// one pane's mapping silently replace another's and send a restore to the wrong
// sandbox root.
func TestConcurrentPanesKeepSeparateMappings(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "herdr-panes")
	store := herdrstate.NewStore(dir)

	projectDir := t.TempDir()
	base := filepath.Join(t.TempDir(), sandbox.GenerateSandboxName(projectDir))

	panes := []struct {
		id          string
		agent       string
		sandboxRoot string
	}{
		{id: "w1F:p1C", agent: "claude", sandboxRoot: base},
		{id: "w1F:p2D", agent: "claude", sandboxRoot: base},
		{id: "w2A:p1C", agent: "codex", sandboxRoot: base},
		// A worktree launch in the same project: same pane naming scheme, a
		// sandbox root a plain re-entry would not derive.
		{id: "w2A:p9Z", agent: "pi", sandboxRoot: filepath.Join(t.TempDir(), "repo-root-derived")},
	}

	var wg sync.WaitGroup
	for _, p := range panes {
		wg.Go(func() {
			rec := herdrstate.Record{
				PaneID:      p.id,
				Agent:       p.agent,
				ProjectDir:  projectDir,
				SandboxRoot: p.sandboxRoot,
			}
			if err := store.Save(rec); err != nil {
				t.Errorf("Save(%q): %v", p.id, err)
			}
		})
	}
	wg.Wait()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != len(panes)+1 {
		t.Fatalf("got %d entries, want %d records and the store lock", len(entries), len(panes))
	}

	for _, p := range panes {
		got, err := store.Load(p.id)
		if err != nil {
			t.Fatalf("Load(%q): %v", p.id, err)
		}
		if got.PaneID != p.id || got.Agent != p.agent || got.SandboxRoot != p.sandboxRoot {
			t.Errorf("pane %q loaded %q/%q/%q", p.id, got.PaneID, got.Agent, got.SandboxRoot)
		}
		if err := herdrstate.Validate(got, p.id, p.agent); err != nil {
			t.Errorf("Validate(%q): %v", p.id, err)
		}
		// Another pane's ID must never validate against this record, even
		// though every other field matches.
		for _, other := range panes {
			if other.id == p.id {
				continue
			}
			if err := herdrstate.Validate(got, other.id, p.agent); err == nil {
				t.Errorf("record for %q validated for pane %q", p.id, other.id)
			}
		}
	}

	// Re-saving one pane leaves the others untouched.
	if err := store.Save(herdrstate.Record{
		PaneID:      panes[0].id,
		Agent:       panes[0].agent,
		ProjectDir:  projectDir,
		SandboxRoot: panes[0].sandboxRoot,
	}); err != nil {
		t.Fatalf("re-Save: %v", err)
	}
	for _, p := range panes[1:] {
		got, err := store.Load(p.id)
		if err != nil {
			t.Fatalf("Load(%q) after re-save: %v", p.id, err)
		}
		if got.SandboxRoot != p.sandboxRoot {
			t.Errorf("pane %q sandbox root changed to %q", p.id, got.SandboxRoot)
		}
	}
}

func TestSaveRejectsIncompleteRecords(t *testing.T) {
	store := herdrstate.NewStore(t.TempDir())
	base := newRecord(t, "w1F:p1C")

	tests := []struct {
		name   string
		mutate func(r *herdrstate.Record)
	}{
		{"empty pane ID", func(r *herdrstate.Record) { r.PaneID = "" }},
		{"empty agent", func(r *herdrstate.Record) { r.Agent = "" }},
		{"relative project dir", func(r *herdrstate.Record) { r.ProjectDir = "relative/path" }},
		{"empty project dir", func(r *herdrstate.Record) { r.ProjectDir = "" }},
		{"relative sandbox root", func(r *herdrstate.Record) { r.SandboxRoot = "relative/root" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := base
			tt.mutate(&rec)
			if err := store.Save(rec); err == nil {
				t.Fatal("Save accepted an invalid record")
			}
		})
	}
}

func TestLoadMissingRecord(t *testing.T) {
	store := herdrstate.NewStore(t.TempDir())

	if _, err := store.Load("no-such-pane"); !errors.Is(err, herdrstate.ErrNotFound) {
		t.Fatalf("Load = %v, want ErrNotFound", err)
	}
	if _, err := store.Load(""); !errors.Is(err, herdrstate.ErrNotFound) {
		t.Fatalf("Load(\"\") = %v, want ErrNotFound", err)
	}
}

func TestLoadRejectsMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	store := herdrstate.NewStore(dir)
	rec := newRecord(t, "w1F:p1C")
	if err := store.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path := recordPath(dir, rec.PaneID)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt record: %v", err)
	}

	_, err := store.Load(rec.PaneID)
	if err == nil {
		t.Fatal("Load accepted malformed JSON")
	}
	if errors.Is(err, herdrstate.ErrNotFound) {
		t.Fatal("malformed JSON reported as ErrNotFound")
	}
}

// TestLoadNeverObservesPartialWrite hammers Save while Load runs. The
// temp-file-plus-rename in Save means a reader sees either the previous record
// or the new one, never a truncated file.
func TestLoadNeverObservesPartialWrite(t *testing.T) {
	store := herdrstate.NewStore(t.TempDir())
	rec := newRecord(t, "w1F:p1C")
	if err := store.Save(rec); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	wg.Go(func() {
		for range 200 {
			if err := store.Save(rec); err != nil {
				errCh <- err
				return
			}
		}
	})

	wg.Go(func() {
		for range 200 {
			got, err := store.Load(rec.PaneID)
			if err != nil {
				errCh <- err
				return
			}
			if got.PaneID != rec.PaneID || got.Version != herdrstate.Version {
				errCh <- errors.New("partial record observed")
				return
			}
		}
	})

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent access: %v", err)
	}
}

func TestValidate(t *testing.T) {
	projectDir := t.TempDir()
	removed := t.TempDir()
	if err := os.RemoveAll(removed); err != nil {
		t.Fatalf("remove project dir: %v", err)
	}
	file := filepath.Join(projectDir, "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	valid := herdrstate.Record{
		Version:     herdrstate.Version,
		PaneID:      "w1F:p1C",
		Agent:       "claude",
		ProjectDir:  projectDir,
		SandboxRoot: "/state/devsandbox/proj-abc",
	}

	tests := []struct {
		name    string
		rec     herdrstate.Record
		paneID  string
		agent   string
		wantErr bool
	}{
		{"valid", valid, "w1F:p1C", "claude", false},
		{"wrong version", func() herdrstate.Record { r := valid; r.Version = 2; return r }(), "w1F:p1C", "claude", true},
		{"zero version", func() herdrstate.Record { r := valid; r.Version = 0; return r }(), "w1F:p1C", "claude", true},
		{"mismatched pane", valid, "w2F:p9Z", "claude", true},
		{"empty caller pane", valid, "", "claude", true},
		{"mismatched agent", valid, "w1F:p1C", "pi", true},
		{"empty caller agent", valid, "w1F:p1C", "", true},
		{"relative project dir", func() herdrstate.Record { r := valid; r.ProjectDir = "rel/path"; return r }(), "w1F:p1C", "claude", true},
		{"removed project dir", func() herdrstate.Record { r := valid; r.ProjectDir = removed; return r }(), "w1F:p1C", "claude", true},
		{"project dir is a file", func() herdrstate.Record { r := valid; r.ProjectDir = file; return r }(), "w1F:p1C", "claude", true},
		{"relative sandbox root", func() herdrstate.Record { r := valid; r.SandboxRoot = "rel"; return r }(), "w1F:p1C", "claude", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := herdrstate.Validate(tt.rec, tt.paneID, tt.agent)
			if tt.wantErr && err == nil {
				t.Fatal("Validate accepted an invalid record")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestDerivesSameSandboxRoot(t *testing.T) {
	base := "/home/alice/.local/share/devsandbox"
	projectDir := "/home/alice/code/proj"
	repoRoot := "/home/alice/code/repo"
	worktreeDir := filepath.Join(base, sandbox.GenerateSandboxName(repoRoot), "worktrees", "ds-x")

	plain := herdrstate.Record{
		Version:     herdrstate.Version,
		PaneID:      "w1F:p1C",
		Agent:       "claude",
		ProjectDir:  projectDir,
		SandboxRoot: filepath.Join(base, sandbox.GenerateSandboxName(projectDir)),
	}
	// A --worktree launch: ProjectDir is the worktree path while SandboxRoot
	// stays derived from the repo root, so re-entry from the worktree would
	// open a different synthetic home.
	worktree := herdrstate.Record{
		Version:     herdrstate.Version,
		PaneID:      "w1F:p1C",
		Agent:       "claude",
		ProjectDir:  worktreeDir,
		SandboxRoot: filepath.Join(base, sandbox.GenerateSandboxName(repoRoot)),
	}

	tests := []struct {
		name string
		rec  herdrstate.Record
		cwd  string
		want bool
	}{
		{"plain launch from its project dir", plain, projectDir, true},
		{"plain launch with trailing slash", plain, projectDir + "/", true},
		{"plain launch from another dir", plain, "/home/alice/code/other", false},
		{"worktree launch from the worktree", worktree, worktreeDir, false},
		{"worktree launch from the repo root", worktree, repoRoot, true},
		{"relative cwd", plain, "code/proj", false},
		{"empty cwd", plain, "", false},
		{"empty sandbox root", herdrstate.Record{ProjectDir: projectDir}, projectDir, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := herdrstate.DerivesSameSandboxRoot(tt.rec, tt.cwd); got != tt.want {
				t.Fatalf("DerivesSameSandboxRoot = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestNoToolBindingExposesStoreDir is the invariant the whole design rests on:
// the pane store must not be reachable from inside the sandbox. Checking every
// registered tool's bindings - rather than only the two paths herdr relocates -
// catches a future tool that binds ~/.local/state or the whole home.
func TestNoToolBindingExposesStoreDir(t *testing.T) {
	home := "/home/alice"
	sandboxHome := "/home/alice/.local/share/devsandbox/proj-abc/home"
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", home)

	storeDir := herdrstate.DefaultDir(home)

	for _, tool := range tools.All() {
		for _, b := range tool.Bindings(home, sandboxHome) {
			if b.Source == "" {
				continue
			}
			if isAncestorOrEqual(b.Source, storeDir) {
				t.Errorf("tool %q binds %q, which exposes the pane store at %q",
					tool.Name(), b.Source, storeDir)
			}
		}
	}
}

func isAncestorOrEqual(dir, path string) bool {
	dir = filepath.Clean(dir)
	path = filepath.Clean(path)
	if dir == path {
		return true
	}
	return strings.HasPrefix(path, dir+string(filepath.Separator))
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestPruneRemovesRecordWhoseSandboxRootIsGone(t *testing.T) {
	dir := t.TempDir()
	store := herdrstate.NewStore(dir)

	gone := newRecord(t, "w1F:p1C")
	live := newRecord(t, "w1F:p2D")
	if err := os.MkdirAll(live.SandboxRoot, 0o755); err != nil {
		t.Fatalf("create live sandbox root: %v", err)
	}
	for _, rec := range []herdrstate.Record{gone, live} {
		if err := store.Save(rec); err != nil {
			t.Fatalf("Save(%q): %v", rec.PaneID, err)
		}
	}

	n, err := herdrstate.Prune(dir)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}
	if _, err := store.Load(gone.PaneID); !errors.Is(err, herdrstate.ErrNotFound) {
		t.Errorf("record whose sandbox root is gone survived: Load = %v", err)
	}
	if _, err := store.Load(live.PaneID); err != nil {
		t.Errorf("record with a live sandbox root was removed: Load = %v", err)
	}

	n, err = herdrstate.Prune(dir)
	if err != nil || n != 0 {
		t.Errorf("second Prune = (%d, %v), want (0, nil): nothing left to reclaim", n, err)
	}
}

// TestPruneKeepsLiveSandboxRootRegardlessOfAge pins the absence of an age
// backstop: a pane can stay open for months, and deleting its record would
// silently disable the resume guard, so only the sandbox root going away may
// remove a record.
func TestPruneKeepsLiveSandboxRootRegardlessOfAge(t *testing.T) {
	dir := t.TempDir()
	store := herdrstate.NewStore(dir)
	rec := newRecord(t, "w1F:p1C")
	if err := os.MkdirAll(rec.SandboxRoot, 0o755); err != nil {
		t.Fatalf("create sandbox root: %v", err)
	}
	old := time.Now().Add(-400 * 24 * time.Hour)
	rec.UpdatedAt = old
	if err := store.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	path := recordPath(dir, rec.PaneID)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("backdate record: %v", err)
	}

	n, err := herdrstate.Prune(dir)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 0 {
		t.Errorf("removed = %d, want 0", n)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("a record over a year old with a live sandbox root was removed: %v", err)
	}
}

// TestPruneKeepsRecordsItCannotInterpret writes files the sweep cannot read
// as one of its own records, each naming a sandbox root that does not exist,
// and asserts every one survives and is reported by path - while entries
// that are not records at all are neither removed nor reported.
func TestPruneKeepsRecordsItCannotInterpret(t *testing.T) {
	dir := t.TempDir()
	store := herdrstate.NewStore(dir)
	gone := newRecord(t, "w1F:p1C")

	malformed := recordPath(dir, "malformed")
	if err := os.WriteFile(malformed, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write malformed record: %v", err)
	}
	otherVersion := recordPath(dir, "other-version")
	writeJSON(t, otherVersion, map[string]any{
		"version": herdrstate.Version + 1, "pane_id": "other-version", "agent": "claude",
		"project_dir": gone.ProjectDir, "sandbox_root": gone.SandboxRoot,
	})
	relativeRoot := recordPath(dir, "relative-root")
	writeJSON(t, relativeRoot, map[string]any{
		"version": herdrstate.Version, "pane_id": "relative-root", "agent": "claude",
		"project_dir": gone.ProjectDir, "sandbox_root": "relative/root",
	})
	kept := []string{malformed, otherVersion, relativeRoot}

	if os.Geteuid() != 0 {
		unreadable := newRecord(t, "unreadable")
		if err := store.Save(unreadable); err != nil {
			t.Fatalf("Save unreadable: %v", err)
		}
		path := recordPath(dir, unreadable.PaneID)
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
		kept = append(kept, path)
	}

	stray := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(stray, []byte("not a record"), 0o600); err != nil {
		t.Fatalf("write stray file: %v", err)
	}
	subdir := filepath.Join(dir, "subdir.json")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	n, err := herdrstate.Prune(dir)
	if n != 0 {
		t.Errorf("removed = %d, want 0", n)
	}
	if err == nil {
		t.Fatal("Prune returned nil, want every uninterpretable record reported")
	}
	for _, path := range kept {
		if _, serr := os.Stat(path); serr != nil {
			t.Errorf("%s was removed: %v", filepath.Base(path), serr)
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("error does not name %s:\n%v", filepath.Base(path), err)
		}
	}
	for _, path := range []string{stray, subdir} {
		if _, serr := os.Stat(path); serr != nil {
			t.Errorf("%s, which is not a record, was removed: %v", filepath.Base(path), serr)
		}
		if strings.Contains(err.Error(), path) {
			t.Errorf("error reports %s, which is not a record:\n%v", filepath.Base(path), err)
		}
	}
}

func TestPruneReportsRemovalFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	store := herdrstate.NewStore(dir)
	rec := newRecord(t, "w1F:p1C")
	if err := store.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	path := recordPath(dir, rec.PaneID)
	n, err := herdrstate.Prune(dir)
	if n != 0 {
		t.Errorf("removed = %d, want 0", n)
	}
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("Prune = %v, want an error naming %s", err, path)
	}
	if _, serr := os.Stat(path); serr != nil {
		t.Errorf("record vanished despite the reported failure: %v", serr)
	}
}

func TestPruneMissingDirIsNothingToDo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-created")
	n, err := herdrstate.Prune(dir)
	if err != nil || n != 0 {
		t.Fatalf("Prune on a missing directory = (%d, %v), want (0, nil)", n, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Prune created the missing directory: %v", err)
	}
}
