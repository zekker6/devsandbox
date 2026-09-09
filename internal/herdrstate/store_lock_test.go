package herdrstate_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"devsandbox/internal/fsutil"
	"devsandbox/internal/herdrstate"
)

// Wait for an actual flock syscall, not merely for a goroutine to start. The
// deadline only bounds a broken implementation; elapsed time never proves exclusion.
func waitForStoreLock(t *testing.T, caller string, done <-chan error) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	buf := make([]byte, 1<<20)
	for {
		for _, stack := range strings.Split(string(buf[:runtime.Stack(buf, true)]), "\n\n") {
			if strings.Contains(stack, caller+"(") && strings.Contains(stack, "syscall.Flock(") {
				return
			}
		}
		select {
		case err := <-done:
			t.Fatalf("%s completed while the store lock was held: %v", caller, err)
		case <-deadline.C:
			t.Fatalf("%s did not reach the store lock", caller)
		default:
			runtime.Gosched()
		}
	}
}

func holdStoreLock(t *testing.T, dir string) *fsutil.FileLock {
	t.Helper()
	lock, err := fsutil.AcquireFileLock(filepath.Join(dir, ".lock"))
	if err != nil {
		t.Fatalf("acquire store lock: %v", err)
	}
	t.Cleanup(func() {
		if err := lock.Release(); err != nil {
			t.Errorf("release store lock: %v", err)
		}
	})
	return lock
}

func TestSaveWaitsForPruneDecisionAcrossStores(t *testing.T) {
	dir := t.TempDir()
	inspector := herdrstate.NewStore(dir)
	writer := herdrstate.NewStore(dir)
	old := newRecord(t, "pane")
	if err := inspector.Save(old); err != nil {
		t.Fatal(err)
	}
	live := old
	live.SandboxRoot = t.TempDir()

	lock := holdStoreLock(t, dir)
	stale, err := inspector.Load(old.PaneID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale.SandboxRoot); !os.IsNotExist(err) {
		t.Fatalf("expected an orphaned record, stat: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- writer.Save(live) }()
	waitForStoreLock(t, "devsandbox/internal/herdrstate.(*Store).Save", done)
	// Complete the stale inspection's unlink while Save is known to be blocked.
	if err := os.Remove(recordPath(dir, old.PaneID)); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := inspector.Load(live.PaneID)
	if err != nil || got.SandboxRoot != live.SandboxRoot {
		t.Fatalf("replacement record = %+v, %v", got, err)
	}
}

func TestPruneReadsReplacementOnlyAfterStoreLock(t *testing.T) {
	dir := t.TempDir()
	store := herdrstate.NewStore(dir)
	old := newRecord(t, "pane")
	if err := store.Save(old); err != nil {
		t.Fatal(err)
	}
	lock := holdStoreLock(t, dir)
	lockInfo, err := os.Stat(filepath.Join(dir, ".lock"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	var removed int
	go func() {
		var err error
		removed, err = herdrstate.Prune(dir)
		done <- err
	}()
	waitForStoreLock(t, "devsandbox/internal/herdrstate.Prune", done)

	// Finish a Save's atomic replacement while holding its lock. Prune must
	// inspect these bytes, not reuse an orphan decision made before locking.
	live := old
	live.Version = herdrstate.Version
	live.SandboxRoot = t.TempDir()
	data, err := json.Marshal(live)
	if err != nil {
		t.Fatal(err)
	}
	if err := fsutil.WriteFileAtomic(recordPath(dir, live.PaneID), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil || removed != 0 {
		t.Fatalf("Prune = (%d, %v), want (0, nil)", removed, err)
	}
	got, err := herdrstate.NewStore(dir).Load(live.PaneID)
	if err != nil || got.SandboxRoot != live.SandboxRoot {
		t.Fatalf("replacement record = %+v, %v", got, err)
	}
	after, err := os.Stat(filepath.Join(dir, ".lock"))
	if err != nil || !os.SameFile(lockInfo, after) {
		t.Fatalf("store lock inode changed after release: %v", err)
	}
}

func TestStoreReleasesLockAfterOperationErrors(t *testing.T) {
	for _, operation := range []string{"save", "prune"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			store := herdrstate.NewStore(dir)
			rec := newRecord(t, "pane")
			path := recordPath(dir, rec.PaneID)
			var err error
			if operation == "save" {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				err = store.Save(rec)
			} else {
				if err := store.Save(rec); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(dir, "invalid.json")
				if err := os.WriteFile(path, []byte("not JSON"), 0o600); err != nil {
					t.Fatal(err)
				}
				var removed int
				removed, err = herdrstate.Prune(dir)
				if removed != 1 {
					t.Errorf("Prune removed %d records, want 1 despite the invalid record", removed)
				}
				if _, statErr := os.Stat(path); statErr != nil {
					t.Errorf("invalid record was removed: %v", statErr)
				}
			}
			if err == nil || !strings.Contains(err.Error(), path) {
				t.Errorf("%s error = %v, want record path %s", operation, err, path)
			}
			lock, err := fsutil.TryFileLock(filepath.Join(dir, ".lock"))
			if err != nil {
				t.Fatalf("%s left the store locked: %v", operation, err)
			}
			if err := lock.Release(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStoreReportsLockErrorsWithoutChangingRecords(t *testing.T) {
	for _, operation := range []string{"save", "prune"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			store := herdrstate.NewStore(dir)
			rec := newRecord(t, "pane")
			if err := store.Save(rec); err != nil {
				t.Fatal(err)
			}
			lockPath := filepath.Join(dir, ".lock")
			if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := os.Mkdir(lockPath, 0o700); err != nil {
				t.Fatal(err)
			}
			var err error
			if operation == "save" {
				replacement := rec
				replacement.SandboxRoot = t.TempDir()
				err = store.Save(replacement)
			} else {
				var removed int
				removed, err = herdrstate.Prune(dir)
				if removed != 0 {
					t.Errorf("Prune removed %d records without its lock", removed)
				}
			}
			if err == nil || !strings.Contains(err.Error(), lockPath) {
				t.Errorf("%s error = %v, want lock path %s", operation, err, lockPath)
			}
			got, err := store.Load(rec.PaneID)
			if err != nil || got.SandboxRoot != rec.SandboxRoot {
				t.Fatalf("original record changed: %+v, %v", got, err)
			}
		})
	}
}
