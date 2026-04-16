package session_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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
