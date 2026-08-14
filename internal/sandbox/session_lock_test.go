package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestAcquireSession_ConcurrentLaunchesElectOnePrimary is the reproducer for
// the check-then-act race: sampling IsSessionActive and only later taking the
// lock let every launch started at the same moment conclude it was alone, so
// they all became primary and all wrote the same persistent overlay upper and
// work dirs.
func TestAcquireSession_ConcurrentLaunchesElectOnePrimary(t *testing.T) {
	root := t.TempDir()

	const launches = 32
	handles := make([]*SessionHandle, launches)
	errs := make([]error, launches)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range launches {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			handles[i], errs[i] = AcquireSession(root)
		}(i)
	}
	close(start)
	wg.Wait()

	primaries := 0
	for i := range launches {
		if errs[i] != nil {
			t.Fatalf("launch %d: AcquireSession failed: %v", i, errs[i])
		}
		defer func(h *SessionHandle) { _ = h.Release() }(handles[i])
		if handles[i].IsPrimary() {
			primaries++
		}
	}

	if primaries != 1 {
		t.Fatalf("primary sessions = %d, want exactly 1", primaries)
	}
}

func TestAcquireSession_SecondHolderIsNotPrimary(t *testing.T) {
	root := t.TempDir()

	first, err := AcquireSession(root)
	if err != nil {
		t.Fatalf("first AcquireSession failed: %v", err)
	}
	defer func() { _ = first.Release() }()

	if !first.IsPrimary() {
		t.Fatal("first session is not primary")
	}

	second, err := AcquireSession(root)
	if err != nil {
		t.Fatalf("second AcquireSession failed: %v", err)
	}
	defer func() { _ = second.Release() }()

	if second.IsPrimary() {
		t.Error("second session claimed the primary designation")
	}

	// The shared lock every session takes is what other commands observe.
	if !IsSessionActive(root) {
		t.Error("IsSessionActive is false while two sessions hold the sandbox")
	}
}

func TestSessionHandle_ReleaseFreesPrimaryAndIsIdempotent(t *testing.T) {
	root := t.TempDir()

	first, err := AcquireSession(root)
	if err != nil {
		t.Fatalf("first AcquireSession failed: %v", err)
	}
	if !first.IsPrimary() {
		t.Fatal("first session is not primary")
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
	// The --rm teardown releases the handle explicitly and the deferred
	// release then runs a second time; that must not error.
	if err := first.Release(); err != nil {
		t.Fatalf("second Release failed: %v", err)
	}
	if first.IsPrimary() {
		t.Error("released handle still reports itself primary")
	}
	if IsSessionActive(root) {
		t.Error("sandbox still reports an active session after release")
	}

	second, err := AcquireSession(root)
	if err != nil {
		t.Fatalf("AcquireSession after release failed: %v", err)
	}
	defer func() { _ = second.Release() }()

	if !second.IsPrimary() {
		t.Error("primary designation was not freed by Release")
	}
}

// TestDesignateSession_SecondHolderUsesSessionOverlay pins the consequence of
// losing the designation: the launch writes to session-scoped overlay dirs
// instead of the primary's persistent ones.
func TestDesignateSession_SecondHolderUsesSessionOverlay(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")

	primary := &Config{Isolation: IsolationBwrap, SandboxRoot: root, SandboxHome: home}
	primaryHandle, err := primary.DesignateSession()
	if err != nil {
		t.Fatalf("primary DesignateSession failed: %v", err)
	}
	defer func() { _ = primaryHandle.Release() }()

	if !primaryHandle.IsPrimary() {
		t.Fatal("first launch did not win the designation")
	}
	if primary.IsConcurrent || primary.SessionID != "" {
		t.Fatalf("primary was routed to a session overlay: concurrent=%v id=%q",
			primary.IsConcurrent, primary.SessionID)
	}

	concurrent := &Config{Isolation: IsolationBwrap, SandboxRoot: root, SandboxHome: home}
	concurrentHandle, err := concurrent.DesignateSession()
	if err != nil {
		t.Fatalf("concurrent DesignateSession failed: %v", err)
	}
	defer func() { _ = concurrentHandle.Release() }()

	if concurrentHandle.IsPrimary() {
		t.Error("second launch claimed the primary designation")
	}
	if !concurrent.IsConcurrent {
		t.Error("second launch was not marked concurrent")
	}
	if concurrent.SessionID == "" {
		t.Fatal("second launch has no session ID")
	}

	const dest = "/home/user/.cache/mise"
	primaryUpper, primaryWork, err := createOverlayDirs(home, dest, "", primary.SessionID)
	if err != nil {
		t.Fatalf("primary overlay dirs: %v", err)
	}
	concurrentUpper, concurrentWork, err := createOverlayDirs(home, dest, "", concurrent.SessionID)
	if err != nil {
		t.Fatalf("concurrent overlay dirs: %v", err)
	}

	if primaryUpper == concurrentUpper || primaryWork == concurrentWork {
		t.Fatalf("concurrent session shares the primary's overlay dirs: upper=%s work=%s",
			concurrentUpper, concurrentWork)
	}
	sessionBase := filepath.Join(home, "overlay", "sessions", concurrent.SessionID)
	if !strings.HasPrefix(concurrentUpper, sessionBase+string(os.PathSeparator)) {
		t.Errorf("concurrent upper %s is not under %s", concurrentUpper, sessionBase)
	}
}

// TestDesignateSession_NonBwrapKeepsPersistentOverlay guards the backend
// condition: docker and krun sandboxes are isolated by the container/microVM,
// so a second launch there is not rerouted onto session overlays.
func TestDesignateSession_NonBwrapKeepsPersistentOverlay(t *testing.T) {
	root := t.TempDir()

	first, err := AcquireSession(root)
	if err != nil {
		t.Fatalf("AcquireSession failed: %v", err)
	}
	defer func() { _ = first.Release() }()

	cfg := &Config{Isolation: IsolationDocker, SandboxRoot: root, SandboxHome: filepath.Join(root, "home")}
	handle, err := cfg.DesignateSession()
	if err != nil {
		t.Fatalf("DesignateSession failed: %v", err)
	}
	defer func() { _ = handle.Release() }()

	if cfg.IsConcurrent || cfg.SessionID != "" {
		t.Errorf("docker launch was routed to a session overlay: concurrent=%v id=%q",
			cfg.IsConcurrent, cfg.SessionID)
	}
}

func TestRemoveSandboxIfIdle_KeepsStateWhileAnotherHolderIsLive(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sandbox")
	statePath := filepath.Join(root, "home", "overlay")
	if err := os.MkdirAll(statePath, 0o755); err != nil {
		t.Fatalf("seed sandbox state: %v", err)
	}

	other, err := AcquireSession(root)
	if err != nil {
		t.Fatalf("AcquireSession failed: %v", err)
	}
	defer func() { _ = other.Release() }()

	removed, err := RemoveSandboxIfIdle(root)
	if err != nil {
		t.Fatalf("RemoveSandboxIfIdle failed: %v", err)
	}
	if removed {
		t.Error("removed sandbox state while another session held it")
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Errorf("sandbox state was removed under a live session: %v", err)
	}
}

func TestRemoveSandboxIfIdle_RemovesWhenNoHolderRemains(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sandbox")
	if err := os.MkdirAll(filepath.Join(root, "home", "overlay"), 0o755); err != nil {
		t.Fatalf("seed sandbox state: %v", err)
	}

	handle, err := AcquireSession(root)
	if err != nil {
		t.Fatalf("AcquireSession failed: %v", err)
	}
	if err := handle.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	removed, err := RemoveSandboxIfIdle(root)
	if err != nil {
		t.Fatalf("RemoveSandboxIfIdle failed: %v", err)
	}
	if !removed {
		t.Fatal("idle sandbox state was not removed")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("sandbox root still present after removal: %v", err)
	}
}

func TestRemoveSandboxIfIdle_MissingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "never-created")

	removed, err := RemoveSandboxIfIdle(root)
	if err != nil {
		t.Fatalf("RemoveSandboxIfIdle failed: %v", err)
	}
	if !removed {
		t.Error("missing sandbox root reported as still held")
	}
}
