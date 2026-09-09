package session_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"devsandbox/internal/procstate"
	"devsandbox/internal/session"
)

func newTestStore(t *testing.T) *session.Store {
	t.Helper()
	dir := t.TempDir()
	return session.NewStore(dir)
}

func makeSession(name string) *session.Session {
	return &session.Session{
		Name:      name,
		PID:       os.Getpid(),
		NetworkNS: "/proc/self/ns/net",
		StartedAt: time.Now().UTC().Truncate(time.Second),
		WorkDir:   "/tmp/work/" + name,
		ProxyPort: 8080,
	}
}

func TestStore_RegisterAndGet(t *testing.T) {
	store := newTestStore(t)

	sess := makeSession("mybox")
	if err := store.Register(sess); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := store.Get("mybox")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.Name != sess.Name {
		t.Errorf("Name: got %q, want %q", got.Name, sess.Name)
	}
	if got.PID != sess.PID {
		t.Errorf("PID: got %d, want %d", got.PID, sess.PID)
	}
	if got.NetworkNS != sess.NetworkNS {
		t.Errorf("NetworkNS: got %q, want %q", got.NetworkNS, sess.NetworkNS)
	}
	if !got.StartedAt.Equal(sess.StartedAt) {
		t.Errorf("StartedAt: got %v, want %v", got.StartedAt, sess.StartedAt)
	}
	if got.WorkDir != sess.WorkDir {
		t.Errorf("WorkDir: got %q, want %q", got.WorkDir, sess.WorkDir)
	}
	if got.ProxyPort != sess.ProxyPort {
		t.Errorf("ProxyPort: got %d, want %d", got.ProxyPort, sess.ProxyPort)
	}
}

func TestStore_RegisterDuplicateName(t *testing.T) {
	store := newTestStore(t)

	sess := makeSession("duplicate")
	if err := store.Register(sess); err != nil {
		t.Fatalf("first Register: %v", err)
	}

	// Second register with same name and live PID should fail.
	sess2 := makeSession("duplicate")
	if err := store.Register(sess2); err == nil {
		t.Fatal("expected error on duplicate name with live PID, got nil")
	}
}

func TestStore_List(t *testing.T) {
	store := newTestStore(t)

	if err := store.Register(makeSession("box-a")); err != nil {
		t.Fatalf("Register box-a: %v", err)
	}
	if err := store.Register(makeSession("box-b")); err != nil {
		t.Fatalf("Register box-b: %v", err)
	}

	sessions, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("List: got %d sessions, want 2", len(sessions))
	}

	names := map[string]bool{}
	for _, s := range sessions {
		names[s.Name] = true
	}
	if !names["box-a"] || !names["box-b"] {
		t.Errorf("List: missing expected session names, got %v", names)
	}
}

func TestStore_Remove(t *testing.T) {
	store := newTestStore(t)

	if err := store.Register(makeSession("removeme")); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := store.Remove("removeme"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := store.Get("removeme"); err == nil {
		t.Fatal("expected error after Remove, got nil")
	}
}

func TestStore_Update(t *testing.T) {
	store := newTestStore(t)

	sess := makeSession("updateme")
	if err := store.Register(sess); err != nil {
		t.Fatalf("Register: %v", err)
	}

	sess.ForwardedPorts = []session.ForwardedPort{
		{HostPort: 9000, SandboxPort: 80, Bind: "127.0.0.1", Protocol: "tcp"},
	}
	if err := store.Update(sess); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := store.Get("updateme")
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if len(got.ForwardedPorts) != 1 {
		t.Fatalf("ForwardedPorts: got %d, want 1", len(got.ForwardedPorts))
	}
	fp := got.ForwardedPorts[0]
	if fp.HostPort != 9000 || fp.SandboxPort != 80 || fp.Bind != "127.0.0.1" || fp.Protocol != "tcp" {
		t.Errorf("ForwardedPort mismatch: %+v", fp)
	}
}

func TestStore_CleanStale(t *testing.T) {
	store := newTestStore(t)

	// PID 999999999 should not exist on any system.
	stale := &session.Session{
		Name:      "stale-box",
		PID:       999999999,
		StartedAt: time.Now().UTC(),
		WorkDir:   "/tmp/stale",
	}
	if err := store.Register(stale); err != nil {
		t.Fatalf("Register stale: %v", err)
	}

	removed := store.CleanStale()
	if removed != 1 {
		t.Errorf("CleanStale: removed %d, want 1", removed)
	}

	if _, err := store.Get("stale-box"); err == nil {
		t.Fatal("expected stale session to be gone, but Get succeeded")
	}
}

func TestStore_AutoName(t *testing.T) {
	store := newTestStore(t)

	// First name from /some/path/myproject should be "myproject".
	name := store.AutoName("/some/path/myproject")
	if name != "myproject" {
		t.Errorf("AutoName: got %q, want %q", name, "myproject")
	}

	// Register a live session with that name.
	sess := makeSession("myproject")
	sess.WorkDir = "/some/path/myproject"
	if err := store.Register(sess); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Second call should return "myproject-2".
	name2 := store.AutoName("/some/path/myproject")
	if name2 != "myproject-2" {
		t.Errorf("AutoName collision -2: got %q, want %q", name2, "myproject-2")
	}

	// Register that too.
	sess2 := makeSession("myproject-2")
	if err := store.Register(sess2); err != nil {
		t.Fatalf("Register -2: %v", err)
	}

	// Third call should return "myproject-3".
	name3 := store.AutoName("/some/path/myproject")
	if name3 != "myproject-3" {
		t.Errorf("AutoName collision -3: got %q, want %q", name3, "myproject-3")
	}
}

func TestStore_FindSingle(t *testing.T) {
	t.Run("no sessions", func(t *testing.T) {
		store := newTestStore(t)
		if _, err := store.FindSingle(); err == nil {
			t.Fatal("expected error with 0 sessions, got nil")
		}
	})

	t.Run("one live session", func(t *testing.T) {
		store := newTestStore(t)
		sess := makeSession("only-one")
		if err := store.Register(sess); err != nil {
			t.Fatalf("Register: %v", err)
		}
		got, err := store.FindSingle()
		if err != nil {
			t.Fatalf("FindSingle: %v", err)
		}
		if got.Name != "only-one" {
			t.Errorf("FindSingle: got %q, want %q", got.Name, "only-one")
		}
	})

	t.Run("multiple live sessions", func(t *testing.T) {
		store := newTestStore(t)
		if err := store.Register(makeSession("one")); err != nil {
			t.Fatalf("Register one: %v", err)
		}
		if err := store.Register(makeSession("two")); err != nil {
			t.Fatalf("Register two: %v", err)
		}
		if _, err := store.FindSingle(); err == nil {
			t.Fatal("expected error with >1 sessions, got nil")
		}
	})

	t.Run("stale sessions excluded", func(t *testing.T) {
		store := newTestStore(t)
		stale := &session.Session{
			Name:      "stale",
			PID:       999999999,
			StartedAt: time.Now().UTC(),
			WorkDir:   "/tmp/stale",
		}
		if err := store.Register(stale); err != nil {
			t.Fatalf("Register stale: %v", err)
		}

		// Write it directly to bypass the live-PID check on Register.
		live := makeSession("live-one")
		if err := store.Register(live); err != nil {
			t.Fatalf("Register live: %v", err)
		}

		got, err := store.FindSingle()
		if err != nil {
			t.Fatalf("FindSingle with one live + one stale: %v", err)
		}
		if got.Name != "live-one" {
			t.Errorf("FindSingle: got %q, want %q", got.Name, "live-one")
		}
	})
}

func TestStore_FindByWorkDir_NoMatch(t *testing.T) {
	store := newTestStore(t)

	sess := makeSession("alpha")
	sess.WorkDir = t.TempDir()
	if err := store.Register(sess); err != nil {
		t.Fatalf("Register: %v", err)
	}

	matches, err := store.FindByWorkDir(t.TempDir())
	if err != nil {
		t.Fatalf("FindByWorkDir: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(matches))
	}
}

func TestStore_FindByWorkDir_SingleMatch(t *testing.T) {
	store := newTestStore(t)

	work := t.TempDir()
	sess := makeSession("alpha")
	sess.WorkDir = work
	if err := store.Register(sess); err != nil {
		t.Fatalf("Register: %v", err)
	}
	other := makeSession("beta")
	other.WorkDir = t.TempDir()
	if err := store.Register(other); err != nil {
		t.Fatalf("Register beta: %v", err)
	}

	matches, err := store.FindByWorkDir(work)
	if err != nil {
		t.Fatalf("FindByWorkDir: %v", err)
	}
	if len(matches) != 1 || matches[0].Name != "alpha" {
		t.Fatalf("expected single match [alpha], got %+v", matches)
	}
}

func TestStore_FindByWorkDir_MultipleMatches(t *testing.T) {
	store := newTestStore(t)

	work := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		sess := makeSession(name)
		sess.WorkDir = work
		if err := store.Register(sess); err != nil {
			t.Fatalf("Register %s: %v", name, err)
		}
	}

	matches, err := store.FindByWorkDir(work)
	if err != nil {
		t.Fatalf("FindByWorkDir: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matches))
	}
}

func TestStore_FindByWorkDir_SkipsStale(t *testing.T) {
	store := newTestStore(t)

	work := t.TempDir()
	live := makeSession("live")
	live.WorkDir = work
	if err := store.Register(live); err != nil {
		t.Fatalf("Register live: %v", err)
	}
	stale := makeSession("stale")
	stale.WorkDir = work
	stale.PID = 1 << 30 // unlikely to be a live PID
	if err := store.Register(stale); err != nil {
		t.Fatalf("Register stale: %v", err)
	}

	matches, err := store.FindByWorkDir(work)
	if err != nil {
		t.Fatalf("FindByWorkDir: %v", err)
	}
	if len(matches) != 1 || matches[0].Name != "live" {
		t.Fatalf("expected single match [live], got %+v", matches)
	}
}

func TestStore_FindByWorkDir_NormalizesSymlinks(t *testing.T) {
	store := newTestStore(t)

	real := t.TempDir()
	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	sess := makeSession("alpha")
	sess.WorkDir = real
	if err := store.Register(sess); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Query using the symlink path.
	matches, err := store.FindByWorkDir(link)
	if err != nil {
		t.Fatalf("FindByWorkDir: %v", err)
	}
	if len(matches) != 1 || matches[0].Name != "alpha" {
		t.Fatalf("expected single match [alpha], got %+v", matches)
	}
}

func TestDefaultStore(t *testing.T) {
	// Override XDG_STATE_HOME so we don't pollute real state dir.
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	store, err := session.DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore: %v", err)
	}

	expected := filepath.Join(tmp, "devsandbox", "sessions")
	// Verify the directory was created.
	if _, err := os.Stat(expected); err != nil {
		t.Errorf("expected directory %q to exist: %v", expected, err)
	}
	_ = store
}

func TestStore_ListForSandbox(t *testing.T) {
	store := newTestStore(t)
	a := makeSession("a")
	a.WorkDir = "/tmp/sbox/home"
	b := makeSession("b")
	b.WorkDir = "/other"
	b.Worktree = &session.WorktreeInfo{Path: "/tmp/sbox/worktrees/x"}
	c := makeSession("c")
	c.WorkDir = "/nope"
	for _, s := range []*session.Session{a, b, c} {
		if err := store.Register(s); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.ListForSandbox("/tmp/sbox")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range got {
		names[s.Name] = true
	}
	if !names["a"] || !names["b"] || names["c"] {
		t.Errorf("got names = %v, want {a, b}", names)
	}
}

func TestStore_RoundTripWorktree(t *testing.T) {
	store := newTestStore(t)
	sess := makeSession("wtbox")
	sess.Worktree = &session.WorktreeInfo{
		Path:         "/home/alice/.local/share/devsandbox/myproj-abcd/worktrees/feat-x",
		Branch:       "feat/x",
		RepoRoot:     "/home/alice/code/myproj",
		RemoveOnExit: true,
	}
	if err := store.Register(sess); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := store.Get(sess.Name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Worktree == nil {
		t.Fatalf("Worktree lost on round trip")
	}
	if got.Worktree.Branch != "feat/x" || !got.Worktree.RemoveOnExit {
		t.Errorf("bad Worktree: %+v", got.Worktree)
	}
	if got.Worktree.Path != sess.Worktree.Path || got.Worktree.RepoRoot != sess.Worktree.RepoRoot {
		t.Errorf("path/repo mismatch: %+v", got.Worktree)
	}
}

// pidAnsweringEPERM returns pid 1 once it has confirmed that a signal-0 probe
// of it answers EPERM - the uncertain probe result every caller must read as
// alive. Gating on the euid alone is not enough: inside a PID namespace pid 1
// is the sandbox's own init and the probe succeeds, so the test would pass
// without exercising the case. Skipping names the reason.
func pidAnsweringEPERM(t *testing.T) int {
	t.Helper()
	if err := syscall.Kill(1, 0); !errors.Is(err, syscall.EPERM) {
		t.Skipf("pid 1 does not answer EPERM here (kill(1, 0) = %v): root, or own pid namespace", err)
	}
	return 1
}

func TestStore_Register_UncertainPIDHoldsName(t *testing.T) {
	store := newTestStore(t)
	held := makeSession("held")
	held.PID = pidAnsweringEPERM(t)
	if err := store.Register(held); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := store.Register(makeSession("held")); err == nil {
		t.Fatal("expected Register to refuse a name held by a session whose pid answers EPERM")
	}
	got, err := store.Get("held")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PID != held.PID {
		t.Errorf("record was overwritten: PID = %d, want %d", got.PID, held.PID)
	}
}

func TestStore_ListLiveAndAutoName_UncertainPIDIsLive(t *testing.T) {
	store := newTestStore(t)
	held := makeSession("myproject")
	held.PID = pidAnsweringEPERM(t)
	if err := store.Register(held); err != nil {
		t.Fatalf("Register: %v", err)
	}

	live, err := store.ListLive()
	if err != nil {
		t.Fatalf("ListLive: %v", err)
	}
	if len(live) != 1 || live[0].Name != "myproject" {
		t.Fatalf("ListLive returned %d sessions, want only the one whose pid answers EPERM", len(live))
	}
	if name := store.AutoName("/some/path/myproject"); name != "myproject-2" {
		t.Errorf("AutoName = %q, want %q: the held name must count as taken", name, "myproject-2")
	}
}

func TestStore_CleanStale_UncertainPIDIsKept(t *testing.T) {
	store := newTestStore(t)
	held := makeSession("held")
	held.PID = pidAnsweringEPERM(t)
	if err := store.Register(held); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if removed := store.CleanStale(); removed != 0 {
		t.Errorf("CleanStale removed %d, want 0", removed)
	}
	if _, err := store.Get("held"); err != nil {
		t.Errorf("expected the record to survive, got %v", err)
	}
}

// ageRecord backdates the record file for name so the store reads it as
// untouched since age ago.
func ageRecord(t *testing.T, store *session.Store, name string, age time.Duration) {
	t.Helper()
	path := filepath.Join(store.Dir(), name+".json")
	then := time.Now().Add(-age)
	if err := os.Chtimes(path, then, then); err != nil {
		t.Fatalf("Chtimes %s: %v", path, err)
	}
}

// TestStore_CleanStaleErr_ReportsUnparseableRecord: a record the store cannot
// parse has no identifiable owner, so it is kept - but it must be reported.
// List skips such a file silently, and going through List left a record
// nothing would ever reclaim in a location the reclaim catalogue reports as
// swept: never removed, never counted, never named in an error.
func TestStore_CleanStaleErr_ReportsUnparseableRecord(t *testing.T) {
	store := newTestStore(t)

	// One record that is genuinely stale, so the sweep has work to do, and one
	// that is corrupt: the corrupt one must not hide the other.
	dead := makeSession("dead")
	dead.PID = 999999999 // unlikely to be a live PID
	if err := store.Register(dead); err != nil {
		t.Fatalf("Register: %v", err)
	}
	corrupt := filepath.Join(store.Dir(), "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt record: %v", err)
	}

	removed, err := store.CleanStaleErr()
	if err == nil {
		t.Fatalf("CleanStaleErr returned no error, want the corrupt record reported")
	}
	if !strings.Contains(err.Error(), "corrupt") {
		t.Errorf("error = %v, want it to name the corrupt record", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1: the corrupt record must not stop the sweep", removed)
	}
	if _, err := os.Stat(corrupt); err != nil {
		t.Errorf("the corrupt record was removed; an unknown owner must not authorize a deletion: %v", err)
	}
}

func TestStore_CleanStaleErr_SurfacesListFailure(t *testing.T) {
	// A regular file in place of a path component makes ReadDir fail with
	// ENOTDIR, which is a real failure to read the store rather than the
	// missing directory of a host that has never run a session.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	store := session.NewStore(filepath.Join(blocker, "sessions"))

	n, err := store.CleanStaleErr()
	if err == nil {
		t.Fatal("CleanStaleErr returned nil error for an unreadable store, want the List failure")
	}
	if n != 0 {
		t.Errorf("removed = %d, want 0", n)
	}
	if got := store.CleanStale(); got != 0 {
		t.Errorf("CleanStale = %d, want 0 on the same failure", got)
	}
}

func TestStore_CleanStaleErr_MissingDirIsEmpty(t *testing.T) {
	store := session.NewStore(filepath.Join(t.TempDir(), "never", "created"))

	n, err := store.CleanStaleErr()
	if err != nil {
		t.Fatalf("CleanStaleErr on a missing store: %v, want nil", err)
	}
	if n != 0 {
		t.Errorf("removed = %d, want 0", n)
	}
}

func TestStore_CleanStaleErr_DeadPIDGoesRegardlessOfAge(t *testing.T) {
	store := newTestStore(t)
	dead := makeSession("dead")
	dead.PID = 999999999
	if err := store.Register(dead); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := store.Register(makeSession("live")); err != nil {
		t.Fatalf("Register live: %v", err)
	}

	n, err := store.CleanStaleErr()
	if err != nil {
		t.Fatalf("CleanStaleErr: %v", err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}
	if _, err := store.Get("dead"); err == nil {
		t.Error("record of a dead pid survived")
	}
	if _, err := store.Get("live"); err != nil {
		t.Errorf("record of a live pid was removed: %v", err)
	}
}

// A record whose pid answers EPERM - one recycled by another user's process -
// is kept by the probe, so the age backstop is the only thing that reclaims
// it: kept while the file is younger than 30 days, removed once it is older.
func TestStore_CleanStaleErr_UncertainPIDBackstop(t *testing.T) {
	store := newTestStore(t)
	held := makeSession("held")
	held.PID = pidAnsweringEPERM(t)
	if err := store.Register(held); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ageRecord(t, store, "held", 29*24*time.Hour)
	n, err := store.CleanStaleErr()
	if err != nil {
		t.Fatalf("CleanStaleErr: %v", err)
	}
	if n != 0 {
		t.Errorf("removed = %d, want 0 for a record younger than the backstop", n)
	}
	if _, err := store.Get("held"); err != nil {
		t.Fatalf("record younger than the backstop was removed: %v", err)
	}

	ageRecord(t, store, "held", 31*24*time.Hour)
	n, err = store.CleanStaleErr()
	if err != nil {
		t.Fatalf("CleanStaleErr: %v", err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1 for a record past the backstop", n)
	}
	if _, err := store.Get("held"); err == nil {
		t.Error("record past the backstop survived")
	}
}

// The same rule with the probe injected, so the backstop stays covered where
// pid 1 does not answer EPERM and the case above skips - inside a PID
// namespace, which is how the sandboxed test runner runs.
func TestStore_CleanStaleErr_UncertainPIDBackstop_Injected(t *testing.T) {
	store := newTestStore(t)
	if err := store.Register(makeSession("young")); err != nil {
		t.Fatalf("Register young: %v", err)
	}
	if err := store.Register(makeSession("old")); err != nil {
		t.Fatalf("Register old: %v", err)
	}
	store.SetProbe(func(int) procstate.State { return procstate.Unknown })
	ageRecord(t, store, "young", 29*24*time.Hour)
	ageRecord(t, store, "old", 31*24*time.Hour)

	n, err := store.CleanStaleErr()
	if err != nil {
		t.Fatalf("CleanStaleErr: %v", err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}
	if _, err := store.Get("young"); err != nil {
		t.Errorf("record younger than the backstop was removed: %v", err)
	}
	if _, err := store.Get("old"); err == nil {
		t.Error("record past the backstop survived")
	}
}

// The backstop is for a pid the probe cannot resolve, and only for that. A pid
// the kernel confirms is running keeps its record however old the file is: the
// record is rewritten only when the session's forwarded ports change, so age
// says nothing about whether the session is still there - and removing it
// drops a running session from `devsandbox sessions`, makes `devsandbox
// forward` unable to resolve it, and frees its name for the next launch to
// register while it is still holding it.
func TestStore_CleanStaleErr_LivePIDIsNeverAgedOut(t *testing.T) {
	store := newTestStore(t)
	if err := store.Register(makeSession("ancient")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ageRecord(t, store, "ancient", 365*24*time.Hour)

	n, err := store.CleanStaleErr()
	if err != nil {
		t.Fatalf("CleanStaleErr: %v", err)
	}
	if n != 0 {
		t.Errorf("removed = %d, want 0: the pid is the test process itself", n)
	}
	if _, err := store.Get("ancient"); err != nil {
		t.Errorf("record of a running session was reclaimed by age: %v", err)
	}
}

// The sweep acts on the name it enumerated, never on the one inside the file.
// Reading the record's own name had it stat and remove a different session's
// file - counting that as reclaimed while leaving the record it was looking at
// in place, to be found again by every later sweep.
func TestStore_CleanStaleErr_ActsOnTheEnumeratedName(t *testing.T) {
	store := newTestStore(t)
	live := makeSession("live")
	if err := store.Register(live); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// A record filed under one name whose body claims another, with a pid
	// nothing owns: the sweep must take this file and leave "live" alone.
	impostor := makeSession("live")
	impostor.PID = 999999999
	data, err := json.Marshal(impostor)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir(), "impostor.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	n, err := store.CleanStaleErr()
	if err != nil {
		t.Fatalf("CleanStaleErr: %v", err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(store.Dir(), "impostor.json")); !os.IsNotExist(err) {
		t.Errorf("the stale record was left in place: stat = %v", err)
	}
	if _, err := store.Get("live"); err != nil {
		t.Errorf("the live session's record was removed in its place: %v", err)
	}
}

func TestStore_CleanStaleErr_ReportsFailedRemoval(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	store := newTestStore(t)
	dead := makeSession("dead")
	dead.PID = 999999999
	if err := store.Register(dead); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := os.Chmod(store.Dir(), 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(store.Dir(), 0o700) })

	n, err := store.CleanStaleErr()
	if err == nil {
		t.Fatal("CleanStaleErr returned nil error for a record it could not remove")
	}
	if n != 0 {
		t.Errorf("removed = %d, want 0", n)
	}
}

func TestStore_CleanStaleErr_PreservesWorktreeCleanup(t *testing.T) {
	for _, state := range []procstate.State{procstate.Dead, procstate.Unknown} {
		t.Run(state.String(), func(t *testing.T) {
			store := newTestStore(t)
			store.SetProbe(func(int) procstate.State { return state })
			checkout := filepath.Join(t.TempDir(), "checkout")
			if err := os.Mkdir(checkout, 0o700); err != nil {
				t.Fatal(err)
			}
			sess := makeSession("worktree")
			sess.Worktree = &session.WorktreeInfo{RepoRoot: t.TempDir(), Path: checkout}
			if err := store.Register(sess); err != nil {
				t.Fatal(err)
			}
			ageRecord(t, store, sess.Name, 365*24*time.Hour)

			if n, err := store.CleanStaleErr(); n != 0 || err != nil {
				t.Fatalf("sweep existing checkout = %d, %v; want 0, nil", n, err)
			}
			if _, err := store.Get(sess.Name); err != nil {
				t.Fatalf("worktree cleanup record lost: %v", err)
			}
			if err := os.Remove(checkout); err != nil {
				t.Fatal(err)
			}
			if n, err := store.CleanStaleErr(); n != 1 || err != nil {
				t.Fatalf("sweep removed checkout = %d, %v; want 1, nil", n, err)
			}
		})
	}
}

func TestStore_CleanStaleErr_WorktreeStatFailure(t *testing.T) {
	store := newTestStore(t)
	store.SetProbe(func(int) procstate.State { return procstate.Dead })
	checkout := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink(checkout, checkout); err != nil {
		t.Fatal(err)
	}
	sess := makeSession("worktree")
	sess.Worktree = &session.WorktreeInfo{RepoRoot: t.TempDir(), Path: checkout}
	if err := store.Register(sess); err != nil {
		t.Fatal(err)
	}
	if err := store.Register(makeSession("ordinary")); err != nil {
		t.Fatal(err)
	}
	n, err := store.CleanStaleErr()
	if n != 1 || !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("sweep = %d, %v; want 1 and ELOOP", n, err)
	}
	if !strings.Contains(err.Error(), checkout) || !strings.Contains(err.Error(), sess.Name) {
		t.Errorf("error does not identify the record and checkout: %v", err)
	}
	if _, err := store.Get(sess.Name); err != nil {
		t.Fatalf("cleanup record lost after stat failure: %v", err)
	}
}

func TestStore_CleanStaleErr_IncompleteWorktreeIsReclaimed(t *testing.T) {
	for _, wt := range []*session.WorktreeInfo{
		{Path: t.TempDir()},
		{RepoRoot: t.TempDir()},
	} {
		store := newTestStore(t)
		store.SetProbe(func(int) procstate.State { return procstate.Dead })
		sess := makeSession("incomplete")
		sess.Worktree = wt
		if err := store.Register(sess); err != nil {
			t.Fatal(err)
		}
		if n, err := store.CleanStaleErr(); n != 1 || err != nil {
			t.Fatalf("sweep incomplete worktree %+v = %d, %v; want 1, nil", wt, n, err)
		}
	}
}

func TestStore_Dir(t *testing.T) {
	dir := t.TempDir()
	if got := session.NewStore(dir).Dir(); got != dir {
		t.Errorf("Dir = %q, want %q", got, dir)
	}
}

// Sets process environment, so it must not call t.Parallel().
func TestDefaultDir(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "user")

	t.Setenv("XDG_STATE_HOME", "")
	if got, want := session.DefaultDir(home), filepath.Join(home, ".local", "state", "devsandbox", "sessions"); got != want {
		t.Errorf("DefaultDir with XDG_STATE_HOME unset = %q, want %q", got, want)
	}

	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	if got, want := session.DefaultDir(home), filepath.Join(state, "devsandbox", "sessions"); got != want {
		t.Errorf("DefaultDir with XDG_STATE_HOME set = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(state, "devsandbox")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("DefaultDir created something under the state home: stat = %v", err)
	}
}

// TestCleanStaleErr_RecordRemovedMidSweep covers the record that disappears
// between the listing and the read. Every launch, `devsandbox sessions` and
// `devsandbox forward` sweep this directory too, so a prune running alongside
// one of them finds a name it can no longer read - which is the outcome this
// sweep wanted, not a failure to report. A dangling symlink reproduces it
// without a second process: ReadDir lists the name and ReadFile answers
// ENOENT, exactly as it does after a concurrent removal.
func TestCleanStaleErr_RecordRemovedMidSweep(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "nothing-here"), filepath.Join(dir, "vanished.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	removed, err := session.NewStore(dir).CleanStaleErr()
	if err != nil {
		t.Fatalf("CleanStaleErr = %v, want nil: a record another sweep removed is not this sweep's failure", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
}
